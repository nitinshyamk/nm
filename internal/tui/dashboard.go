package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Source is everything the dashboard needs from the rest of nm.
//
// An interface, and a narrow one, because the model must be testable without a
// task root, a git binary, or the developer's live claude sessions — the same
// reason workplan.Agents and task.Forge exist. internal/cli implements it; the
// tests implement it in memory.
type Source interface {
	// Tasks lists everything under the tasks root. Cheap by construction: a
	// ReadDir plus one small file per task, no subprocess, so the tree can be on
	// screen before anything slow has started.
	Tasks() ([]*Node, error)

	// Status reads one task's git state. This is the slow one — a git spawn per
	// repository — so it is called per task and streamed back rather than
	// gathered up and waited on.
	Status(dir string) (RowStatus, error)

	// Sessions lists the claude agents nm knows about.
	Sessions() ([]SessionInfo, error)

	// Logs returns a background session's recent output, already stripped of the
	// escape codes claude's own interface draws itself with.
	Logs(id string) (string, error)

	// Files reads one level of a directory, for expanding a row.
	Files(dir string) ([]*Node, error)

	// Read returns a file's content for the preview, capped at RenderCap, along
	// with the whole file's size and modification time.
	Read(path string) (content []byte, size int64, modTime time.Time, err error)
}

// SessionInfo is one claude agent, as the dashboard shows it.
type SessionInfo struct {
	ID       string
	Describe string // "working", "needs reply"
	Class    int    // agent.Class ordinal
	Name     string
	Dir      string // the session's working directory
	Age      string // "12m", already rendered
	Live     bool   // worth asking for logs; a finished session has none
}

// DashboardConfig describes a dashboard.
type DashboardConfig struct {
	Title  string
	Source Source

	// StatusEvery, SessionsEvery, and TailEvery are the refresh cadences. Fields
	// rather than constants so a test can drive a refresh without sleeping for one.
	StatusEvery   time.Duration
	SessionsEvery time.Duration
	TailEvery     time.Duration

	// PreviewDelay debounces the preview, so holding the cursor key scrolls
	// instead of formatting every file it passes over.
	PreviewDelay time.Duration
}

// Cadence defaults. Git status is cheap enough to re-read often; the session
// query costs ~2s against a real machine's worth of agents, so it goes slower.
// The tail re-reads the selected agent's output often enough to feel live.
const (
	DefaultStatusEvery   = 10 * time.Second
	DefaultSessionsEvery = 30 * time.Second
	DefaultTailEvery     = 3 * time.Second
	DefaultPreviewDelay  = 80 * time.Millisecond
)

// DashboardOutcome is what the dashboard was asked to do on the way out.
type DashboardOutcome struct {
	// Action is "", "select", "open", "agent", "attach", "new", or "delete".
	// "attach" hands the terminal to the task's live agent; "new" creates a task
	// and so carries no Dir.
	Action string
	Dir    string // the task the action applies to; empty for "new"
}

// RunDashboard shows the dashboard and blocks until the user leaves.
//
// Unlike the picker this takes the alt screen: it is a view you work in rather
// than a pane you answer, and at a terminal's full height it would otherwise
// bury the scrollback it was drawn over.
func RunDashboard(cfg DashboardConfig) (DashboardOutcome, error) {
	final, err := tea.NewProgram(newDashboard(cfg), tea.WithAltScreen()).Run()
	if err != nil {
		return DashboardOutcome{}, err
	}
	result, ok := final.(dashboard)
	if !ok {
		return DashboardOutcome{}, fmt.Errorf("unexpected model %T returned from the dashboard", final)
	}
	return result.outcome, nil
}

// focus is which pane the keys act on.
type focus int

const (
	focusTree focus = iota
	focusPreview
)

type dashboard struct {
	cfg  DashboardConfig
	keys keyMap
	help help.Model

	tree   *tree
	cursor int
	top    int // first visible row, for scrolling

	sessions []SessionInfo
	tail     string // the fetched agent log, already stripped
	tailFor  string // which session the tail belongs to

	render  *Renderer
	preview Preview
	pvp     viewport.Model
	pvPath  string // what the preview pane is currently showing

	spin  spinner.Model
	focus focus

	// generation counts refreshes. A reply carrying an old number is dropped,
	// so a slow answer cannot overwrite a fresher one.
	generation int

	// refreshing is true from the moment a refresh is asked for until the session
	// list it kicked off answers. It drives the highlighted "working" indicator in
	// the title, so a background refresh is visible rather than silent.
	refreshing bool

	filtering bool
	filter    string

	confirm     *Node
	confirmYes  int
	showAllKeys bool

	status  string // the echo line: errors, prefixes, what just happened
	stale   bool   // the last refresh failed; the numbers on screen are old
	width   int
	height  int
	ready   bool
	outcome DashboardOutcome
	done    bool
}

func newDashboard(cfg DashboardConfig) dashboard {
	if cfg.StatusEvery == 0 {
		cfg.StatusEvery = DefaultStatusEvery
	}
	if cfg.SessionsEvery == 0 {
		cfg.SessionsEvery = DefaultSessionsEvery
	}
	if cfg.TailEvery == 0 {
		cfg.TailEvery = DefaultTailEvery
	}
	if cfg.PreviewDelay == 0 {
		cfg.PreviewDelay = DefaultPreviewDelay
	}

	spin := spinner.New(spinner.WithSpinner(spinner.MiniDot), spinner.WithStyle(styleSpinner))
	return dashboard{
		cfg:    cfg,
		keys:   defaultKeyMap(),
		help:   help.New(),
		tree:   newTree(),
		render: NewRenderer(),
		spin:   spin,
		width:  100,
		height: 30,
	}
}

// Messages the dashboard sends itself. Every one that carries data from a slow
// read also carries the generation it was asked for.
type (
	tasksMsg struct {
		nodes []*Node
		err   error
		gen   int
	}
	statusMsg struct {
		dir    string
		status RowStatus
		err    error
		gen    int
	}
	sessionsMsg struct {
		sessions []SessionInfo
		err      error
		gen      int
	}
	filesMsg struct {
		dir   string
		nodes []*Node
		err   error
	}
	previewMsg struct {
		path    string
		content []byte
		size    int64
		modTime time.Time
		err     error
	}
	tailMsg struct {
		id   string
		body string
		err  error
	}
	previewDueMsg  struct{ path string }
	tailDueMsg     struct{ id string }
	statusTickMsg  struct{}
	sessionTickMsg struct{}
	tailTickMsg    struct{}
)

func (m dashboard) Init() tea.Cmd {
	return tea.Batch(
		m.loadTasks(m.generation),
		m.loadSessions(m.generation),
		m.spin.Tick,
		tea.Tick(m.cfg.StatusEvery, func(time.Time) tea.Msg { return statusTickMsg{} }),
		tea.Tick(m.cfg.SessionsEvery, func(time.Time) tea.Msg { return sessionTickMsg{} }),
		tea.Tick(m.cfg.TailEvery, func(time.Time) tea.Msg { return tailTickMsg{} }),
	)
}

func (m dashboard) loadTasks(gen int) tea.Cmd {
	return func() tea.Msg {
		nodes, err := m.cfg.Source.Tasks()
		return tasksMsg{nodes: nodes, err: err, gen: gen}
	}
}

func (m dashboard) loadSessions(gen int) tea.Cmd {
	return func() tea.Msg {
		sessions, err := m.cfg.Source.Sessions()
		return sessionsMsg{sessions: sessions, err: err, gen: gen}
	}
}

// loadStatus fans out one command per task. They are independent, so bubbletea
// runs them concurrently and each row fills in as its own answer arrives.
func (m dashboard) loadStatus(gen int) tea.Cmd {
	var cmds []tea.Cmd
	for _, node := range m.tree.roots {
		for _, child := range node.Children {
			if child.Kind != NodeTask {
				continue
			}
			dir := child.Path
			cmds = append(cmds, func() tea.Msg {
				st, err := m.cfg.Source.Status(dir)
				return statusMsg{dir: dir, status: st, err: err, gen: gen}
			})
		}
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m dashboard) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tasksMsg:
		if msg.gen != m.generation {
			return m, nil
		}
		if msg.err != nil {
			// Keep whatever is on screen: a failed read is not news that the tasks
			// are gone.
			m.status, m.stale = "could not read the tasks root: "+msg.err.Error(), true
			return m, nil
		}
		m.adoptTasks(msg.nodes)
		return m, tea.Batch(m.loadStatus(m.generation), m.schedulePreview())

	case statusMsg:
		if msg.gen != m.generation {
			return m, nil
		}
		m.applyStatus(msg)
		return m, nil

	case sessionsMsg:
		if msg.gen != m.generation {
			return m, nil
		}
		// The session list is the last of the three reads a refresh fans out, so its
		// arrival is where the "working" indicator comes down.
		m.refreshing = false
		if msg.err != nil {
			// A claude that cannot be queried must not empty the pane.
			m.stale = true
			return m, nil
		}
		m.sessions = msg.sessions
		m.applySessions()
		// A fresh session list can change which agent is selected or revive one that
		// had finished, so re-read the tail for whatever the cursor is on now.
		return m, m.fetchTail()

	case filesMsg:
		m.applyFiles(msg)
		return m, nil

	case previewDueMsg:
		if node := m.selected(); node != nil && node.Path == msg.path && node.Kind == NodeFile {
			return m, m.readPreview(node.Path)
		}
		return m, nil

	case tailDueMsg:
		// Only fetch if the cursor is still on the same agent; moving straight past
		// a row must not spawn a `claude logs` for it.
		if session := m.sessionForSelection(); session != nil && session.ID == msg.id {
			return m, m.fetchTail()
		}
		return m, nil

	case previewMsg:
		m.applyPreview(msg)
		return m, nil

	case tailMsg:
		if msg.err != nil {
			m.tail, m.tailFor = "could not read the agent log: "+msg.err.Error(), msg.id
			return m, nil
		}
		m.tail, m.tailFor = msg.body, msg.id
		return m, nil

	case statusTickMsg:
		return m, tea.Batch(
			m.loadStatus(m.generation),
			tea.Tick(m.cfg.StatusEvery, func(time.Time) tea.Msg { return statusTickMsg{} }),
		)

	case sessionTickMsg:
		return m, tea.Batch(
			m.loadSessions(m.generation),
			tea.Tick(m.cfg.SessionsEvery, func(time.Time) tea.Msg { return sessionTickMsg{} }),
		)

	case tailTickMsg:
		return m, tea.Batch(
			m.fetchTail(),
			tea.Tick(m.cfg.TailEvery, func(time.Time) tea.Msg { return tailTickMsg{} }),
		)

	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

// adoptTasks installs a fresh listing without throwing away what the old one
// had learned.
//
// A refresh re-reads the tasks root, which produces nodes with no status and no
// children. Taking them as-is would blank every column back to a spinner and
// close every directory the reader had opened, ten seconds after they opened it.
// So the status and the already-read children are carried across by path, and the
// cursor is put back on whatever it was on.
func (m *dashboard) adoptTasks(nodes []*Node) {
	was := ""
	if node := m.selected(); node != nil {
		was = node.Path
	}

	type carried struct {
		status   *RowStatus
		children []*Node
		loaded   bool
	}
	known := make(map[string]carried)
	var collect func(ns []*Node)
	collect = func(ns []*Node) {
		for _, n := range ns {
			if n.Path != "" && (n.Status != nil || n.Loaded) {
				known[canonical(n.Path)] = carried{status: n.Status, children: n.Children, loaded: n.Loaded}
			}
			collect(n.Children)
		}
	}
	collect(m.tree.roots)

	var restore func(ns []*Node)
	restore = func(ns []*Node) {
		for _, n := range ns {
			if prior, ok := known[canonical(n.Path)]; ok {
				if n.Status == nil {
					n.Status = prior.status
				}
				// Only a node the fresh listing left empty takes the old children:
				// a directory the source has re-read knows better than we do.
				if !n.Loaded && prior.loaded {
					n.Children, n.Loaded = prior.children, true
				}
			}
			restore(n.Children)
		}
	}
	restore(nodes)

	m.tree.setRoots(nodes)
	m.stale = false
	if was != "" {
		if at := m.tree.find(was); at >= 0 {
			m.cursor = at
			m.clampCursor()
			return
		}
	}
	m.cursor = m.firstSelectable()
	m.clampCursor()
}

// applySessions fills the agent column from the session list.
//
// The source answers once per refresh for the whole view rather than once per
// task, so matching rows to sessions happens here. A task row keeps whatever git
// numbers it has; only the agent half is rewritten.
func (m *dashboard) applySessions() {
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Kind == NodeTask {
				agentName, class := "", classNone
				if session := m.sessionFor(n.Path); session != nil {
					agentName, class = session.Describe, session.Class
				}
				if n.Status == nil {
					n.Status = &RowStatus{}
				}
				n.Status.Agent, n.Status.Class = agentName, class
			}
			walk(n.Children)
		}
	}
	walk(m.tree.roots)
}

// applyStatus files one task's git state, or marks the row stale when the read
// failed without discarding the numbers already there.
func (m *dashboard) applyStatus(msg statusMsg) {
	want := canonical(msg.dir)
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Path != "" && canonical(n.Path) == want {
				if msg.err != nil {
					if n.Status != nil {
						n.Status.Stale = true
					}
					return
				}
				st := msg.status
				// The agent column comes from Sessions, not from here, so a git
				// answer must not blank what the session list already filled in.
				if n.Status != nil && st.Agent == "" {
					st.Agent, st.Class = n.Status.Agent, n.Status.Class
				}
				n.Status = &st
				return
			}
			walk(n.Children)
		}
	}
	walk(m.tree.roots)
}

func (m *dashboard) applyFiles(msg filesMsg) {
	want := canonical(msg.dir)
	var walk func(ns []*Node)
	walk = func(ns []*Node) {
		for _, n := range ns {
			if n.Path != "" && canonical(n.Path) == want {
				n.Children, n.Loaded = msg.nodes, true
				return
			}
			walk(n.Children)
		}
	}
	walk(m.tree.roots)
	if msg.err != nil {
		m.status = "could not read " + msg.dir + ": " + msg.err.Error()
	}
	m.tree.reflow()
	m.clampCursor()
}

func (m *dashboard) applyPreview(msg previewMsg) {
	if msg.err != nil {
		m.preview = Preview{Body: styleMuted.Render("could not read the file: " + msg.err.Error())}
		m.pvPath = msg.path
		m.pvp.SetContent(m.preview.Body)
		m.pvp.GotoTop()
		return
	}
	m.preview = m.render.Render(msg.path, msg.content, msg.size, msg.modTime, m.previewWidth())
	m.pvPath = msg.path
	m.pvp.SetContent(m.preview.Body)
	m.pvp.GotoTop()
}

func (m dashboard) readPreview(path string) tea.Cmd {
	return func() tea.Msg {
		content, size, modTime, err := m.cfg.Source.Read(path)
		return previewMsg{path: path, content: content, size: size, modTime: modTime, err: err}
	}
}

// schedulePreview arms the debounce for whatever the cursor is on. Nothing is
// read until the timer fires and the cursor is still there.
func (m dashboard) schedulePreview() tea.Cmd {
	node := m.selected()
	if node == nil || node.Kind != NodeFile {
		return nil
	}
	path := node.Path
	return tea.Tick(m.cfg.PreviewDelay, func(time.Time) tea.Msg {
		return previewDueMsg{path: path}
	})
}

// scheduleTail arms the same debounce for the agent working in the selected
// task, so the log pane follows the cursor without a `claude logs` per row while
// a movement key is held. Only a task with its tail not already on screen is
// worth scheduling; the periodic tick keeps the shown one current.
func (m dashboard) scheduleTail() tea.Cmd {
	session := m.sessionForSelection()
	if session == nil || session.ID == m.tailFor {
		return nil
	}
	id := session.ID
	return tea.Tick(m.cfg.PreviewDelay, func(time.Time) tea.Msg {
		return tailDueMsg{id: id}
	})
}

func (m dashboard) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case m.confirm != nil:
		return m.updateConfirm(msg)
	case m.filtering:
		return m.updateFilter(msg)
	}

	keyStr := msg.String()

	switch {
	case key.Matches(msg, m.keys.Quit):
		m.done = true
		return m, tea.Quit

	case key.Matches(msg, m.keys.Abort):
		m.status, m.showAllKeys = "", false
		return m, nil

	case key.Matches(msg, m.keys.Help):
		m.showAllKeys = !m.showAllKeys
		m.help.ShowAll = m.showAllKeys
		m.layout()
		return m, nil

	case key.Matches(msg, m.keys.OtherPane):
		m.focus = m.otherPane()
		return m, nil

	case key.Matches(msg, m.keys.Find):
		m.filtering = true
		return m, nil

	case key.Matches(msg, m.keys.Refresh):
		m.generation++
		m.refreshing = true
		m.status = ""
		return m, tea.Batch(m.loadTasks(m.generation), m.loadSessions(m.generation))

	case key.Matches(msg, m.keys.ScrollDown):
		m.pvp.ScrollDown(3)
		return m, nil

	case key.Matches(msg, m.keys.ScrollUp):
		m.pvp.ScrollUp(3)
		return m, nil
	}

	if m.focus == focusPreview {
		switch {
		case key.Matches(msg, m.keys.Down):
			m.pvp.ScrollDown(1)
			return m, nil
		case key.Matches(msg, m.keys.Up):
			m.pvp.ScrollUp(1)
			return m, nil
		case key.Matches(msg, m.keys.PageDown):
			m.pvp.PageDown()
			return m, nil
		case key.Matches(msg, m.keys.PageUp):
			m.pvp.PageUp()
			return m, nil
		}
	}

	switch {
	case key.Matches(msg, m.keys.Down):
		return m.moveBy(1)
	case key.Matches(msg, m.keys.Up):
		return m.moveBy(-1)
	case key.Matches(msg, m.keys.PageDown):
		return m.moveBy(m.pageSize())
	case key.Matches(msg, m.keys.PageUp):
		return m.moveBy(-m.pageSize())
	case key.Matches(msg, m.keys.Top):
		return m.moveTo(m.firstSelectable())
	case key.Matches(msg, m.keys.Bottom):
		return m.moveTo(len(m.tree.rows) - 1)
	case key.Matches(msg, m.keys.Unfold):
		return m.unfold()
	case key.Matches(msg, m.keys.Fold):
		return m.fold()
	}

	// New is not tied to a row: it creates a task rather than acting on one, so
	// it is handled before the row actions and works even on an empty root.
	if key.Matches(msg, m.keys.New) {
		m.outcome = DashboardOutcome{Action: "new"}
		m.done = true
		return m, tea.Quit
	}

	// Actions apply to the task a row belongs to, so acting on a file inside a
	// task does the obvious thing rather than nothing.
	switch {
	case key.Matches(msg, m.keys.Enter):
		return m.act("select", keyStr)
	case key.Matches(msg, m.keys.Open):
		return m.act("open", keyStr)
	case key.Matches(msg, m.keys.Agent):
		return m.act("agent", keyStr)
	case key.Matches(msg, m.keys.Attach):
		// Interacting means handing the terminal to the task's agent and coming
		// back to the dashboard afterward. The caller attaches to a live session or
		// starts a fresh one when there is none, so this one action covers both; it
		// differs from `a` in that it returns here rather than leaving the shell in
		// the task directory.
		return m.act("attach", keyStr)
	case key.Matches(msg, m.keys.Delete):
		if task := m.selectedTask(); task != nil {
			m.confirm, m.confirmYes = task, 0
		}
		return m, nil
	}
	return m, nil
}

func (m dashboard) updateFilter(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+g":
		m.filtering, m.filter = false, ""
	case "enter":
		m.filtering = false
	case "backspace":
		if m.filter != "" {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
		}
	default:
		if len(msg.Runes) == 0 {
			return m, nil
		}
		m.filter += string(msg.Runes)
	}

	// The tree owns the filter so the cursor and the drawn rows are the same
	// list; re-flowing here is what keeps the highlight on something visible.
	m.tree.filter = m.filter
	m.tree.reflow()
	m.cursor = m.skipHeadings(clamp(m.cursor, 0, len(m.tree.rows)-1), true)
	m.clampCursor()
	return m, m.schedulePreview()
}

func (m dashboard) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+g", "q":
		m.confirm = nil
		return m, nil
	case "left", "right", "tab", "h", "l", "ctrl+f", "ctrl+b":
		m.confirmYes = 1 - m.confirmYes
		return m, nil
	case "enter":
		if m.confirmYes == 0 {
			m.confirm = nil
			return m, nil
		}
		m.outcome = DashboardOutcome{Action: "delete", Dir: m.confirm.Path}
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

// act leaves the dashboard with an action for the caller to carry out. The
// terminal has to come back first — `claude attach` and the shell's cd protocol
// both need it — so these quit rather than running anything here.
func (m dashboard) act(action, _ string) (tea.Model, tea.Cmd) {
	task := m.selectedTask()
	if task == nil {
		return m, nil
	}
	m.outcome = DashboardOutcome{Action: action, Dir: task.Path}
	m.done = true
	return m, tea.Quit
}

func (m dashboard) fetchTail() tea.Cmd {
	session := m.sessionForSelection()
	if session == nil {
		return nil
	}
	if !session.Live {
		return func() tea.Msg {
			return tailMsg{id: session.ID, body: styleMuted.Render("this agent has finished; it has no log to read")}
		}
	}
	id := session.ID
	return func() tea.Msg {
		body, err := m.cfg.Source.Logs(id)
		return tailMsg{id: id, body: body, err: err}
	}
}

func (m dashboard) moveBy(delta int) (tea.Model, tea.Cmd) {
	return m.moveTo(m.cursor + delta)
}

func (m dashboard) moveTo(index int) (tea.Model, tea.Cmd) {
	before := m.cursor
	m.cursor = clamp(index, 0, len(m.tree.rows)-1)
	m.cursor = m.skipHeadings(m.cursor, index >= before)
	m.clampCursor()
	if m.cursor == before {
		return m, nil
	}
	return m, tea.Batch(m.schedulePreview(), m.scheduleTail())
}

// skipHeadings moves off a group heading, which is a label rather than
// something to act on.
func (m dashboard) skipHeadings(index int, forward bool) int {
	for index >= 0 && index < len(m.tree.rows) && m.tree.rows[index].Node.Kind == NodeGroup {
		if forward {
			index++
		} else {
			index--
		}
	}
	if index < 0 {
		return m.firstSelectable()
	}
	if index >= len(m.tree.rows) {
		return clamp(len(m.tree.rows)-1, 0, len(m.tree.rows)-1)
	}
	return index
}

func (m dashboard) firstSelectable() int {
	for i, row := range m.tree.rows {
		if row.Node.Kind != NodeGroup {
			return i
		}
	}
	return 0
}

// unfold opens the row, reading its contents the first time. On a row already
// open it steps to the first child, so holding the key walks inward.
func (m dashboard) unfold() (tea.Model, tea.Cmd) {
	node := m.selected()
	if node == nil || !canExpand(node) {
		return m, nil
	}
	if m.tree.isExpanded(node) {
		if len(node.Children) > 0 {
			return m.moveBy(1)
		}
		return m, nil
	}

	m.tree.expand(node)
	m.tree.reflow()
	m.clampCursor()
	if node.Loaded {
		return m, nil
	}
	dir := node.Path
	return m, func() tea.Msg {
		nodes, err := m.cfg.Source.Files(dir)
		return filesMsg{dir: dir, nodes: nodes, err: err}
	}
}

// fold closes the row, or steps out to the parent when it is already closed.
func (m dashboard) fold() (tea.Model, tea.Cmd) {
	node := m.selected()
	if node == nil {
		return m, nil
	}
	if canExpand(node) && m.tree.isExpanded(node) {
		m.tree.collapse(node)
		m.tree.reflow()
		m.clampCursor()
		return m, nil
	}
	if parent := m.tree.parentOf(m.cursor); parent >= 0 {
		return m.moveTo(parent)
	}
	return m, nil
}

// otherPane is where focus goes next. There is only somewhere to go when the
// detail pane has something scrollable in it; focusing an empty pane would strand
// the cursor keys on nothing.
func (m dashboard) otherPane() focus {
	if m.focus == focusTree && m.pvPath != "" {
		return focusPreview
	}
	return focusTree
}

func (m dashboard) selected() *Node {
	if m.cursor < 0 || m.cursor >= len(m.tree.rows) {
		return nil
	}
	return m.tree.rows[m.cursor].Node
}

// selectedTask is the task a row belongs to: itself, or the task above it.
func (m dashboard) selectedTask() *Node {
	index := m.cursor
	for index >= 0 && index < len(m.tree.rows) {
		if node := m.tree.rows[index].Node; node.Kind == NodeTask {
			return node
		}
		next := m.tree.parentOf(index)
		if next == index || next < 0 {
			return nil
		}
		index = next
	}
	return nil
}

// sessionForSelection is the agent working in the selected task.
func (m dashboard) sessionForSelection() *SessionInfo {
	task := m.selectedTask()
	if task == nil {
		return nil
	}
	return m.sessionFor(task.Path)
}

// sessionFor is the agent working in a task directory, matched by where it is
// running: an agent started in a task works inside one of its worktrees, a level
// below the directory itself.
func (m dashboard) sessionFor(dir string) *SessionInfo {
	want := canonical(dir)
	for i := range m.sessions {
		if sameOrUnderPath(m.sessions[i].Dir, want) {
			return &m.sessions[i]
		}
	}
	return nil
}

// sameOrUnderPath reports whether dir is the task directory or inside it. root
// is already canonical; dir is canonicalized here, because a session's working
// directory comes from claude rather than from Go and is spelled its own way.
func sameOrUnderPath(dir, root string) bool {
	got := canonical(dir)
	if got == root {
		return true
	}
	return strings.HasPrefix(got, root+string(filepath.Separator))
}

func (m *dashboard) clampCursor() {
	m.cursor = clamp(m.cursor, 0, len(m.tree.rows)-1)
	size := m.pageSize()
	if m.cursor < m.top {
		m.top = m.cursor
	}
	if m.cursor >= m.top+size {
		m.top = m.cursor - size + 1
	}
	m.top = clamp(m.top, 0, max(len(m.tree.rows)-size, 0))
}

// Layout. The tree takes the left, the detail and the agents split the right.
// Below treeOnlyWidth there is not room for two panes, so the detail is reached
// by switching panes instead of being on screen beside the tree.
const (
	treeOnlyWidth = 60
	narrowWidth   = 100
	treeShare     = 45 // percent of the width the tree gets when both panes fit
)

func (m *dashboard) layout() {
	m.pvp.Width = max(m.previewWidth(), 10)
	m.pvp.Height = max(m.previewHeight(), 3)
	m.help.Width = m.width
}

func (m dashboard) twoPane() bool { return m.width >= treeOnlyWidth }

func (m dashboard) treeWidth() int {
	if !m.twoPane() {
		return m.width
	}
	w := m.width * treeShare / 100
	return clamp(w, 24, m.width-24)
}

func (m dashboard) previewWidth() int {
	if !m.twoPane() {
		return m.width - 4
	}
	// Two borders of two columns each, between the panes and at the edge.
	return max(m.width-m.treeWidth()-6, 10)
}

// chromeHeight is everything outside the panes: the help line, the echo line, and
// the one blank line between them.
const chromeHeight = 3

// bodyHeight is the content height of the left pane — its border excluded, since
// lipgloss draws that outside the height it is given.
func (m dashboard) bodyHeight() int {
	extra := 0
	if m.showAllKeys {
		extra = 4
	}
	return max(m.height-chromeHeight-2-extra, 3)
}

// previewHeight is the top-right pane's content height.
//
// The right column stacks two bordered panes against the left's one, so it spends
// two more lines on borders. Splitting bodyHeight evenly would make the right
// column two lines taller than the left; taking those two off first is what keeps
// the bottoms level.
func (m dashboard) previewHeight() int {
	return max((m.bodyHeight()-2)/2, 3)
}

// pageSize is how many task rows fit, after the pane's title line and the column
// header above them.
func (m dashboard) pageSize() int {
	return max(m.bodyHeight()-2, 1)
}

func (m dashboard) View() string {
	// Erased on the way out for the same reason the picker is: the action the
	// caller is about to take prints to the terminal, and a stale final frame
	// above it reads as part of the output.
	if m.done {
		return ""
	}

	plan := planColumns(m.treeWidth() - 2)
	left := m.renderTree(plan)

	var body string
	switch {
	case m.twoPane():
		right := lipgloss.JoinVertical(lipgloss.Left, m.renderDetail(), m.renderAgents())
		body = lipgloss.JoinHorizontal(lipgloss.Top, left, right)
	case m.focus == focusPreview:
		// Too narrow for two panes, so the detail replaces the tree rather than
		// being squeezed beside it.
		body = m.renderDetail()
	default:
		body = left
	}

	var b strings.Builder
	b.WriteString(body)
	b.WriteString("\n")
	if line := m.echoLine(); line != "" {
		b.WriteString(line + "\n")
	}
	b.WriteString(m.help.View(m.keys))

	if m.confirm != nil {
		return b.String() + "\n" + m.renderConfirm()
	}
	return b.String()
}

func (m dashboard) echoLine() string {
	switch {
	case m.filtering || m.filter != "":
		return styleMuted.Render(fmt.Sprintf("find: %s_", m.filter))
	case m.status != "":
		return styleMuted.Render(trim(m.status, m.width))
	case m.stale:
		return styleWarnText.Render("showing the last good state; the most recent refresh failed")
	default:
		return ""
	}
}

func (m dashboard) renderTree(plan columnPlan) string {
	var b strings.Builder
	b.WriteString(plan.header() + "\n")

	rows := m.tree.rows
	size := m.pageSize()
	end := min(m.top+size, len(rows))
	for i := m.top; i < end; i++ {
		b.WriteString(renderRow(rows[i], plan, i == m.cursor, m.spin.View()) + "\n")
	}
	switch {
	case len(rows) > 0:
	case m.filter != "":
		b.WriteString(styleMuted.Render("nothing matches "+m.filter) + "\n")
	default:
		b.WriteString(styleMuted.Render("no tasks yet — press n to create one") + "\n")
	}

	// A standing nav entry for creating a task, so the action is discoverable from
	// the pane itself rather than only from the help legend. Drawn once under the
	// rows, dim like a legend, and hidden while filtering — n is a filter character
	// then, not a command.
	if !m.filtering && m.filter == "" {
		b.WriteString(styleDirName.Render("+ new task") + styleMuted.Render("  (n)") + "\n")
	}

	style := stylePane
	if m.focus == focusTree {
		style = stylePaneFocused
	}
	return style.Width(m.treeWidth() - 2).Height(m.bodyHeight()).Render(
		m.titleBar() + "\n" + b.String())
}

// titleBar is the tree pane's heading: the title, with a highlighted chip
// pushed to the right edge whenever work is in flight. The chip is the visible
// answer to "is anything happening" — a background refresh, or agents still
// working — rather than leaving that to the one spinner frame in a column.
func (m dashboard) titleBar() string {
	width := m.treeWidth() - 4
	chip := m.workingChip()
	title := stylePaneTitle.Render(trim(m.cfg.Title, max(width-lipgloss.Width(chip)-1, 1)))
	if chip == "" {
		return title
	}
	gap := width - lipgloss.Width(title) - lipgloss.Width(chip)
	if gap < 1 {
		gap = 1
	}
	return title + strings.Repeat(" ", gap) + chip
}

// workingChip is the highlighted indicator, or "" when nothing is in flight. A
// refresh outranks the agent count because it is the thing the user just asked
// for and is waiting on.
//
// The marker is a static ● rather than the spinner frame: the spinner carries
// its own color-and-reset codes, and nesting them inside the chip's reverse
// style would end the reverse at the first reset — the chip is the lit
// highlight, so it has to be one unbroken span.
func (m dashboard) workingChip() string {
	if m.refreshing {
		return styleWorking.Render("● refreshing")
	}
	if n := m.workingAgents(); n > 0 {
		return styleWorking.Render(fmt.Sprintf("● %d working", n))
	}
	return ""
}

// workingAgents counts the sessions claude reports as actively working, which is
// what the chip announces when no refresh is running.
func (m dashboard) workingAgents() int {
	n := 0
	for _, session := range m.sessions {
		if session.Class == classWorking {
			n++
		}
	}
	return n
}

func (m dashboard) renderDetail() string {
	node := m.selected()
	title := "detail"
	var body string

	switch {
	case node == nil:
		body = styleMuted.Render("nothing selected")
	case node.Kind == NodeFile:
		title = node.Label
		body = m.pvp.View()
		if m.preview.Truncated {
			body += "\n" + styleWarnText.Render(fmt.Sprintf("⎿ showing the first %s of %s",
				humanSize(RenderCap), humanSize(m.preview.Size)))
		}
	default:
		title = node.Label
		body = m.renderTaskDetail(node)
	}

	style := stylePane
	if m.focus == focusPreview {
		style = stylePaneFocused
	}
	return style.Width(m.previewWidth()).Height(m.previewHeight()).Render(
		stylePaneTitle.Render(trim(title, m.previewWidth()-2)) + "\n" + body)
}

func (m dashboard) renderTaskDetail(node *Node) string {
	detail := node.Detail
	if detail == nil {
		return styleMuted.Render(node.Path)
	}

	var b strings.Builder
	if detail.Note != "" {
		b.WriteString(styleWarnText.Render(detail.Note) + "\n\n")
	}
	if len(detail.Repos) > 0 {
		b.WriteString(styleColumnHead.Render("REPOS") + "\n")
		for _, repo := range detail.Repos {
			line := fmt.Sprintf("  %s  %s", repo.Name, styleMuted.Render(repo.Branch))
			if repo.PR != "" {
				line += "  " + badgeStyle(BadgeOK).Render(repo.PR)
			}
			if repo.Err != "" {
				line += "  " + styleDanger.Render("status unavailable")
			}
			b.WriteString(trim(line, m.previewWidth()-2) + "\n")
		}
	}
	if detail.Workplan != "" {
		b.WriteString("\n" + styleColumnHead.Render("WORKPLAN") + "\n")
		b.WriteString("  " + detail.Workplan + " · " + detail.TaskID + "\n")
	}
	if detail.Prompt != "" {
		b.WriteString("\n" + styleColumnHead.Render("PROMPT") + "\n")
		for _, line := range wrapText(detail.Prompt, m.previewWidth()-4, 6) {
			b.WriteString("  " + line + "\n")
		}
	}
	return b.String()
}

func (m dashboard) renderAgents() string {
	var b strings.Builder

	session := m.sessionForSelection()
	if session == nil {
		b.WriteString(styleMuted.Render("no agent here") + "\n")
	} else {
		badge := badgeStyle(classBadgeKind(session.Class))
		fmt.Fprintf(&b, "%s  %s  %s", badge.Render("●"), session.ID, badge.Render(session.Describe))
		if session.Age != "" {
			fmt.Fprintf(&b, "  %s", styleMuted.Render(session.Age))
		}
		b.WriteString("\n")
		if session.Name != "" {
			b.WriteString("  " + styleMuted.Render(trim(session.Name, m.previewWidth()-4)) + "\n")
		}
		if m.tailFor == session.ID && m.tail != "" {
			b.WriteString(styleMuted.Render(strings.Repeat("─", max(m.previewWidth()-2, 4))) + "\n")
			for _, line := range tailLines(m.tail, m.previewWidth()-2, m.agentsHeight()-4) {
				b.WriteString(line + "\n")
			}
		} else if session.Live {
			// The tail loads on its own and re-reads on a timer, so this is only on
			// screen for the moment before the first read lands.
			b.WriteString("\n" + styleMuted.Render("reading its recent output…") + "\n")
		}
	}

	return stylePane.Width(m.previewWidth()).Height(m.agentsHeight()).Render(
		stylePaneTitle.Render("agents") + "\n" + b.String())
}

// agentsHeight is the bottom-right pane, taking whatever the preview left after
// both panes' borders are accounted for.
func (m dashboard) agentsHeight() int {
	return max(m.bodyHeight()-2-m.previewHeight(), 3)
}

func (m dashboard) renderConfirm() string {
	node := m.confirm
	var b strings.Builder
	b.WriteString(styleTitle.Render("Delete "+node.Label+"?") + "\n")
	b.WriteString(styleMuted.Render(node.Path) + "\n")

	verb := "Delete"
	if node.Status != nil && len(node.Status.Badges) > 0 {
		var hazards []string
		for _, badge := range node.Status.Badges {
			if badge.Kind == BadgeWarn || badge.Kind == BadgeDanger {
				hazards = append(hazards, badge.Text)
			}
		}
		if len(hazards) > 0 {
			b.WriteString("\n" + styleDanger.Render("This will destroy work that exists nowhere else:") + "\n")
			b.WriteString(styleWarnText.Render("  • "+strings.Join(hazards, ", ")) + "\n")
			verb += " anyway"
		}
	}

	cancel, proceed := styleButton.Render("Cancel"), styleButton.Render(verb)
	if m.confirmYes == 0 {
		cancel = styleFocus.Render("Cancel")
	} else {
		proceed = styleFocus.Render(verb)
	}
	b.WriteString("\n" + lipgloss.JoinHorizontal(lipgloss.Top, cancel, "  ", proceed) + "\n")
	b.WriteString(styleHelp.Render("←/→ choose · enter confirm · C-g cancel"))
	return styleDialog.Render(b.String())
}

// tailLines takes the end of an agent's output, which is the part that says what
// it is doing now.
func tailLines(body string, width, limit int) []string {
	if limit < 1 {
		limit = 1
	}
	var kept []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(strings.ReplaceAll(line, "\r", ""), " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		kept = append(kept, trim(line, width))
	}
	if len(kept) > limit {
		kept = kept[len(kept)-limit:]
	}
	return kept
}

// wrapText breaks text into at most limit lines of the given width.
func wrapText(text string, width, limit int) []string {
	if width < 10 {
		width = 10
	}
	words := strings.Fields(strings.ReplaceAll(text, "\n", " "))
	var lines []string
	current := ""
	for _, word := range words {
		switch {
		case current == "":
			current = word
		case lipgloss.Width(current)+1+lipgloss.Width(word) <= width:
			current += " " + word
		default:
			lines = append(lines, current)
			if len(lines) == limit {
				return append(lines[:limit-1], trim(lines[limit-1]+" …", width))
			}
			current = word
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
