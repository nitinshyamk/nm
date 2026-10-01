package tui

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// fakeSource stands in for the tasks root, git, and claude. The dashboard is
// tested against this and never against the real thing: a test that read the
// developer's own sessions would answer differently on every machine, and one
// that shelled out to git would be too slow to run on every change.
type fakeSource struct {
	tasks    []*Node
	tasksErr error

	status    map[string]RowStatus
	statusErr map[string]error

	sessions    []SessionInfo
	sessionsErr error

	files map[string][]*Node
	logs  map[string]string

	content map[string][]byte
	sizes   map[string]int64
	readErr map[string]error

	// calls counts what was asked for, so a test can prove the debounce and the
	// generation guard actually prevent work rather than merely hide it.
	statusCalls  int
	sessionCalls int
	readCalls    int
	logCalls     int
}

func (f *fakeSource) Tasks() ([]*Node, error) {
	if f.tasksErr != nil {
		return nil, f.tasksErr
	}
	return cloneNodes(f.tasks), nil
}

func (f *fakeSource) Status(dir string) (RowStatus, error) {
	f.statusCalls++
	if err := f.statusErr[dir]; err != nil {
		return RowStatus{}, err
	}
	return f.status[dir], nil
}

func (f *fakeSource) Sessions() ([]SessionInfo, error) {
	f.sessionCalls++
	if f.sessionsErr != nil {
		return nil, f.sessionsErr
	}
	return f.sessions, nil
}

// Logs mirrors the real source's contract: stripping is the source's job, done
// in internal/agent where the subprocess lives, so the model receives text it can
// put straight on screen.
func (f *fakeSource) Logs(id string) (string, error) {
	f.logCalls++
	body, ok := f.logs[id]
	if !ok {
		return "", errors.New("job not found")
	}
	return ansi.Strip(body), nil
}

func (f *fakeSource) Files(dir string) ([]*Node, error) {
	return cloneNodes(f.files[dir]), nil
}

func (f *fakeSource) Read(path string) ([]byte, int64, time.Time, error) {
	f.readCalls++
	if err := f.readErr[path]; err != nil {
		return nil, 0, time.Time{}, err
	}
	content := f.content[path]
	size := int64(len(content))
	if declared, ok := f.sizes[path]; ok {
		size = declared
	}
	return content, size, time.Unix(0, 0), nil
}

// cloneNodes deep-copies a tree, because the model annotates nodes in place and
// a shared fixture would leak state between assertions.
func cloneNodes(in []*Node) []*Node {
	out := make([]*Node, 0, len(in))
	for _, n := range in {
		copied := *n
		copied.Children = cloneNodes(n.Children)
		out = append(out, &copied)
	}
	return out
}

func dir(parts ...string) string {
	return filepath.Join(append([]string{string(filepath.Separator) + "tasks"}, parts...)...)
}

// sampleSource is two groups of tasks with files underneath, plus a directory
// that is not a task.
func sampleSource() *fakeSource {
	alpha, beta, orphan := dir("alpha-111111"), dir("beta-222222"), dir("stray")

	return &fakeSource{
		tasks: []*Node{
			{Kind: NodeGroup, Label: "Needs input", Loaded: true, Children: []*Node{
				{
					Kind: NodeTask, Label: "alpha-111111", Path: alpha,
					Detail: &TaskDetail{Dir: alpha, Prompt: "make the thing work"},
				},
			}},
			{Kind: NodeGroup, Label: "Working", Loaded: true, Children: []*Node{
				{
					Kind: NodeTask, Label: "beta-222222", Path: beta,
					Detail: &TaskDetail{Dir: beta},
				},
			}},
			{Kind: NodeGroup, Label: "Unrecognized", Loaded: true, Children: []*Node{
				{
					Kind: NodeOrphan, Label: "stray", Path: orphan,
					Detail: &TaskDetail{Dir: orphan, Note: "no .nm-task.json — nm cannot manage this directory"},
				},
			}},
		},
		status: map[string]RowStatus{
			alpha: {Agent: "needs reply", Class: 0, Badges: []Badge{{Text: "✎1", Kind: BadgeWarn}}, PRs: "#7"},
			beta:  {Agent: "working", Class: 3, Badges: []Badge{{Text: "clean", Kind: BadgeOK}}},
		},
		statusErr: map[string]error{},
		sessions: []SessionInfo{
			{ID: "aaa111", Describe: "needs reply", Class: 0, Name: "nm-alpha", Dir: alpha, Age: "12m", Live: true},
			{ID: "bbb222", Describe: "done", Class: 1, Name: "nm-beta", Dir: beta, Age: "2h"},
		},
		files: map[string][]*Node{
			alpha: {
				{Kind: NodeDir, Label: "artifacts", Path: filepath.Join(alpha, "artifacts")},
				{Kind: NodeDir, Label: "input", Path: filepath.Join(alpha, "input")},
			},
			filepath.Join(alpha, "artifacts"): {
				{Kind: NodeFile, Label: "notes.md", Path: filepath.Join(alpha, "artifacts", "notes.md"), Size: 24, Loaded: true},
			},
		},
		logs: map[string]string{
			"aaa111": "\x1b[K\x1b[38;2;8;145;178m● Both gaps resolved\x1b[m\n  Ran 6 shell commands\n",
		},
		content: map[string][]byte{
			filepath.Join(alpha, "artifacts", "notes.md"): []byte("# Notes\n\nthe body\n"),
		},
		sizes:   map[string]int64{},
		readErr: map[string]error{},
	}
}

// board builds a sized dashboard whose ticks are long enough never to fire
// during a test.
func board(src Source) dashboard {
	m := newDashboard(DashboardConfig{
		Title:         "tasks in /tasks",
		Source:        src,
		StatusEvery:   time.Hour,
		SessionsEvery: time.Hour,
		PreviewDelay:  time.Millisecond,
	})
	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	return next.(dashboard)
}

// step feeds one message in and returns the model plus whatever command came
// back, so a test can run the command and feed its result in turn.
func step(t *testing.T, m dashboard, msg tea.Msg) (dashboard, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	out, ok := next.(dashboard)
	if !ok {
		t.Fatalf("Update returned %T", next)
	}
	return out, cmd
}

// drain runs a command and feeds every message it produces back into the model.
//
// The picker's test driver runs a command and looks only for tea.QuitMsg, which
// is enough for a model that answers immediately. This one has to see the
// results: the whole design is that rows arrive after the view does.
func drain(t *testing.T, m dashboard, cmd tea.Cmd) dashboard {
	t.Helper()
	for _, msg := range collect(t, cmd) {
		switch msg.(type) {
		case spinner.TickMsg, statusTickMsg, sessionTickMsg:
			continue // timers would recur forever
		}
		var next tea.Cmd
		m, next = step(t, m, msg)
		if next != nil {
			m = drain(t, m, next)
		}
	}
	return m
}

// collect flattens a command into the messages it yields, running a batch's
// members and waiting only briefly on anything timer-backed.
func collect(t *testing.T, cmd tea.Cmd) []tea.Msg {
	t.Helper()
	if cmd == nil {
		return nil
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()

	select {
	case msg := <-done:
		switch batch := msg.(type) {
		case nil:
			return nil
		case tea.BatchMsg:
			var out []tea.Msg
			for _, sub := range batch {
				out = append(out, collect(t, sub)...)
			}
			return out
		default:
			return []tea.Msg{msg}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a command did not produce a message")
		return nil
	}
}

// loaded is a dashboard with its tasks, statuses, and sessions already in — the
// steady state, after everything Init kicked off has answered.
func loaded(t *testing.T, src Source) dashboard {
	t.Helper()
	m := board(src)
	m = drain(t, m, m.loadTasks(m.generation))
	return drain(t, m, m.loadSessions(m.generation))
}

func TestTreePaintsBeforeStatusArrives(t *testing.T) {
	// The whole point of the rewrite: the folders are on screen before anything
	// slow has answered, with a spinner standing in for what has not.
	src := sampleSource()
	m := board(src)
	m, _ = step(t, m, tasksMsg{nodes: cloneNodes(src.tasks), gen: m.generation})

	view := m.View()
	for _, want := range []string{"alpha-111111", "beta-222222", "Needs input", "Working"} {
		if !strings.Contains(view, want) {
			t.Errorf("the tree does not show %q before status arrived:\n%s", want, view)
		}
	}
	if frame := m.spin.View(); !strings.Contains(view, frame) {
		t.Errorf("no spinner %q where a pending status should be:\n%s", frame, view)
	}
}

func TestTheFirstFrameCostsNoSlowReads(t *testing.T) {
	// The tree is on screen before git or claude has been asked anything. This is
	// the whole reason the view is built the way it is, and it is easy to undo by
	// accident: grouping rows by live agent state once put a ~600ms subprocess in
	// front of the first frame.
	src := sampleSource()
	m := board(src)

	// Only the task listing is fed in — not the commands it returns, which are the
	// slow reads. What is on screen at this point is what the user sees first.
	for _, msg := range collect(t, m.loadTasks(m.generation)) {
		m, _ = step(t, m, msg)
	}

	if src.statusCalls != 0 {
		t.Errorf("painting the tree ran %d git reads, want 0", src.statusCalls)
	}
	if src.sessionCalls != 0 {
		t.Errorf("painting the tree ran %d session queries, want 0", src.sessionCalls)
	}
	if src.readCalls != 0 {
		t.Errorf("painting the tree read %d files, want 0", src.readCalls)
	}
	if view := m.View(); !strings.Contains(view, "alpha-111111") {
		t.Errorf("the tree is not on screen before the slow reads:\n%s", view)
	}
}

func TestAgentColumnComesFromOneSessionQuery(t *testing.T) {
	// Sessions are fetched once for the whole view and matched to rows here; asking
	// per task would spawn one claude per task for an answer that cannot differ
	// between them.
	src := sampleSource()
	m := loaded(t, src)

	if src.sessionCalls != 1 {
		t.Errorf("filling the agent column took %d session queries, want 1", src.sessionCalls)
	}
	if view := plain(m.View()); !strings.Contains(view, "needs reply") {
		t.Errorf("the agent column is empty after the session list arrived:\n%s", view)
	}
}

func TestGitStatusDoesNotBlankTheAgentColumn(t *testing.T) {
	// The two halves of a row arrive from different reads, and whichever lands
	// second must not erase the first.
	src := sampleSource()
	m := loaded(t, src)
	alpha := dir("alpha-111111")

	m, _ = step(t, m, statusMsg{dir: alpha, status: src.status[alpha], gen: m.generation})
	view := plain(m.View())
	if !strings.Contains(view, "needs r") {
		t.Errorf("a git answer blanked the agent column:\n%s", view)
	}
	if !strings.Contains(view, "✎1") {
		t.Errorf("the git column is missing:\n%s", view)
	}
}

func TestStatusFillsOneTaskAtATime(t *testing.T) {
	src := sampleSource()
	m := board(src)
	m, _ = step(t, m, tasksMsg{nodes: cloneNodes(src.tasks), gen: m.generation})

	alpha := dir("alpha-111111")
	m, _ = step(t, m, statusMsg{dir: alpha, status: src.status[alpha], gen: m.generation})

	view := m.View()
	if !strings.Contains(view, "✎1") {
		t.Errorf("alpha's git badge is missing after its status arrived:\n%s", view)
	}
	if !strings.Contains(view, "#7") {
		t.Errorf("alpha's pull request is missing:\n%s", view)
	}
	// beta has not answered, so it must still be pending rather than claiming to
	// be clean.
	if strings.Contains(view, "clean") {
		t.Errorf("beta reports clean before its status arrived:\n%s", view)
	}
}

func TestStaleGenerationIsIgnored(t *testing.T) {
	// A refresh bumps the generation. An answer to the previous one is late by
	// definition and must not overwrite what is now on screen.
	src := sampleSource()
	m := loaded(t, src)
	alpha := dir("alpha-111111")
	m, _ = step(t, m, statusMsg{dir: alpha, status: src.status[alpha], gen: m.generation})

	before := m.View()
	m, _ = step(t, m, statusMsg{
		dir:    alpha,
		status: RowStatus{Badges: []Badge{{Text: "✎99", Kind: BadgeWarn}}},
		gen:    m.generation - 1,
	})
	if after := m.View(); after != before {
		t.Errorf("a stale-generation status changed the view:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestFailedPollKeepsTheLastGoodState(t *testing.T) {
	// A git or claude failure must never blank the view: the numbers from a
	// minute ago are more useful than nothing, as long as it says they are old.
	src := sampleSource()
	m := loaded(t, src)
	alpha := dir("alpha-111111")
	m, _ = step(t, m, statusMsg{dir: alpha, status: src.status[alpha], gen: m.generation})
	m, _ = step(t, m, statusMsg{dir: alpha, err: errors.New("git exploded"), gen: m.generation})

	if view := m.View(); !strings.Contains(view, "✎1") {
		t.Errorf("a failed refresh discarded the previous status:\n%s", view)
	}

	m, _ = step(t, m, sessionsMsg{err: errors.New("claude is not answering"), gen: m.generation})
	if !m.stale {
		t.Error("a failed session query did not mark the view stale")
	}
	if view := m.View(); !strings.Contains(view, "alpha-111111") {
		t.Errorf("a failed session query hid the tasks:\n%s", view)
	}
}

func TestTasksErrorDoesNotEmptyTheTree(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)
	m, _ = step(t, m, tasksMsg{err: errors.New("root vanished"), gen: m.generation})

	if view := m.View(); !strings.Contains(view, "alpha-111111") {
		t.Errorf("a failed task read emptied the tree:\n%s", view)
	}
	if !m.stale {
		t.Error("a failed task read did not mark the view stale")
	}
}

func TestStatusColumnsAlign(t *testing.T) {
	// Columns are the reason this view is readable at a glance, and they are
	// measured in display cells: the badges and fold markers are multi-byte, so
	// byte-based padding would misalign exactly the rows carrying information.
	src := sampleSource()
	m := loaded(t, src)
	for taskDir, st := range src.status {
		m, _ = step(t, m, statusMsg{dir: taskDir, status: st, gen: m.generation})
	}

	plan := planColumns(m.treeWidth() - 2)
	var widths []int
	for _, row := range m.tree.rows {
		if row.Node.Kind != NodeTask {
			continue
		}
		name := treeName(row, plan.name)
		widths = append(widths, lipgloss.Width(name))
	}
	if len(widths) < 2 {
		t.Fatalf("expected at least two task rows, got %d", len(widths))
	}
	for i, got := range widths {
		if got != plan.name {
			t.Errorf("task row %d name column is %d cells, want %d", i, got, plan.name)
		}
	}
}

func TestColumnsDropFromTheRightAsItNarrows(t *testing.T) {
	wide := planColumns(90)
	if !wide.agent || !wide.git || !wide.pr {
		t.Errorf("a wide pane should show every column: %+v", wide)
	}
	narrow := planColumns(40)
	if narrow.pr {
		t.Errorf("the pull request column should go first when narrow: %+v", narrow)
	}
	if !narrow.agent {
		t.Errorf("the agent column should be the last to go: %+v", narrow)
	}
	tiny := planColumns(18)
	if tiny.name < 1 {
		t.Errorf("the name column must always have room: %+v", tiny)
	}
}

func TestExpandAndCollapse(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))

	if hasRow(m, "artifacts") {
		t.Errorf("a task is expanded before being opened:\n%s", rowDump(m))
	}

	m, cmd := step(t, m, pressKey("ctrl+f"))
	m = drain(t, m, cmd)
	if !hasRow(m, "artifacts") {
		t.Errorf("C-f did not open the task:\n%s", rowDump(m))
	}

	m, _ = step(t, m, pressKey("ctrl+b"))
	if hasRow(m, "artifacts") {
		t.Errorf("C-b did not close the task:\n%s", rowDump(m))
	}
}

func TestExpansionSurvivesARefreshThatReordersRows(t *testing.T) {
	// A task whose agent finishes moves to another group. Keying expansion on the
	// path rather than the row means it does not collapse when that happens.
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))
	m, cmd := step(t, m, pressKey("ctrl+f"))
	m = drain(t, m, cmd)

	reordered := cloneNodes(src.tasks)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	m, _ = step(t, m, tasksMsg{nodes: reordered, gen: m.generation})

	if !hasRow(m, "artifacts") {
		t.Errorf("the open task collapsed when the list was reordered:\n%s", rowDump(m))
	}
	if node := m.selected(); node == nil || node.Path != dir("alpha-111111") {
		t.Errorf("the cursor did not stay on alpha across the refresh: %+v", node)
	}
}

func TestCursorSkipsChildrenOfCollapsedTasksAndHeadings(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)

	// Walking down from the top must land on tasks, never on a group heading and
	// never on a file inside a task that is closed.
	m, _ = step(t, m, pressKey("alt+<"))
	seen := []string{}
	for range m.tree.rows {
		if node := m.selected(); node != nil {
			if node.Kind == NodeGroup {
				t.Fatalf("the cursor landed on the group heading %q", node.Label)
			}
			seen = append(seen, node.Label)
		}
		m, _ = step(t, m, pressKey("ctrl+n"))
	}
	for _, label := range seen {
		if label == "artifacts" || label == "notes.md" {
			t.Errorf("the cursor walked into a collapsed task's contents: %v", seen)
		}
	}
}

func TestUnrecognizedDirectoriesAreShown(t *testing.T) {
	// A directory with no .nm-task.json is invisible to every other nm command,
	// which is how a worktree ends up stranded. The dashboard says it is there.
	m := loaded(t, sampleSource())
	view := m.View()
	if !strings.Contains(view, "Unrecognized") || !strings.Contains(view, "stray") {
		t.Errorf("a directory that is not a task was dropped:\n%s", view)
	}
}

func TestPreviewRendersAFileAndTruncatesALargeOne(t *testing.T) {
	src := sampleSource()
	notes := filepath.Join(dir("alpha-111111"), "artifacts", "notes.md")

	m := loaded(t, src)
	m = expandTo(t, m, dir("alpha-111111"), filepath.Join(dir("alpha-111111"), "artifacts"))
	m = selectPath(t, m, notes)

	m, _ = step(t, m, previewMsg{
		path: notes, content: src.content[notes], size: int64(len(src.content[notes])),
	})
	// Stripped before matching: glamour styles each word, so the rendered bytes
	// carry escape sequences between them and a raw substring search would miss
	// text that is plainly on screen.
	if view := plain(m.View()); !strings.Contains(view, "the body") {
		t.Errorf("the preview does not show the file:\n%s", view)
	}

	// A file bigger than the cap reports that it is showing only the beginning,
	// rather than silently pretending the rest is not there.
	m, _ = step(t, m, previewMsg{
		path: notes, content: src.content[notes], size: 4 * 1024 * 1024,
	})
	if view := plain(m.View()); !strings.Contains(view, "showing the first") {
		t.Errorf("a truncated preview does not say so:\n%s", view)
	}
}

// plain strips styling so an assertion can look for text rather than for the
// bytes a particular theme happens to wrap it in.
func plain(s string) string { return ansi.Strip(s) }

func TestPreviewIsDebouncedAndSkipsNonFiles(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)

	// A task row is not a file, so nothing should be read for it.
	if cmd := m.schedulePreview(); cmd != nil {
		t.Error("moving onto a task row scheduled a file read")
	}
	if src.readCalls != 0 {
		t.Errorf("a task row caused %d reads", src.readCalls)
	}

	// The due message only reads if the cursor is still on the same file, so
	// scrolling past a file never formats it.
	notes := filepath.Join(dir("alpha-111111"), "artifacts", "notes.md")
	m, cmd := step(t, m, previewDueMsg{path: notes})
	if cmd != nil {
		t.Error("a due preview for a file the cursor has left still read it")
	}
	_ = m
}

func TestBinaryAndHugeFilesDoNotHang(t *testing.T) {
	r := NewRenderer()

	binary := append([]byte("PNG\x00\x01\x02"), make([]byte, 100)...)
	out := r.Render("image.png", binary, int64(len(binary)), time.Unix(0, 0), 60)
	if !out.Binary {
		t.Error("a file with NUL bytes was not treated as binary")
	}
	if strings.Contains(out.Body, "\x00") {
		t.Error("the preview put raw NUL bytes on screen")
	}

	// RenderCap is what keeps the 2MB artifacts in a real task root from stalling
	// the view; the renderer is handed a capped read and reports the true size.
	big := []byte(strings.Repeat("# Heading\n\ntext\n\n", 400))
	out = r.Render("big.md", big, 4*1024*1024, time.Unix(0, 0), 60)
	if !out.Truncated {
		t.Error("a capped read was not reported as truncated")
	}
}

func TestSourceFilesAreSyntaxHighlighted(t *testing.T) {
	// A code file comes back colored: the body carries ANSI escapes it did not
	// have raw, and the plain text is still all there. TypeScript, TOML, and C++
	// are the languages the request named; each has a chroma lexer.
	r := NewRenderer()
	cases := []struct {
		path string
		code string
		want string
	}{
		{"app.ts", "const greeting: string = \"hi\"\n", "greeting"},
		{"Cargo.toml", "[package]\nname = \"demo\"\n", "package"},
		{"main.cpp", "#include <vector>\nint main() { return 0; }\n", "vector"},
		{"ui.tsx", "export const App = () => <div/>\n", "App"},
	}
	for _, tc := range cases {
		body := []byte(tc.code)
		out := r.Render(tc.path, body, int64(len(body)), time.Unix(0, 0), 80)
		if !strings.Contains(out.Body, "\x1b[") {
			t.Errorf("%s was not colored: no escape codes in\n%q", tc.path, out.Body)
		}
		if !strings.Contains(plain(out.Body), tc.want) {
			t.Errorf("%s lost its text: %q not in\n%q", tc.path, tc.want, plain(out.Body))
		}
	}
}

func TestUnknownAndPlainFilesAreShownRaw(t *testing.T) {
	// A file with no lexer must come back byte-for-byte: wrapping plain text in a
	// highlight pass would add escape codes for no gain, and a log read as code
	// looks worse than one left alone.
	r := NewRenderer()
	plainText := "just some words\nwith no language\n"
	out := r.Render("notes.txt", []byte(plainText), int64(len(plainText)), time.Unix(0, 0), 80)
	if out.Body != plainText {
		t.Errorf("a plain file was altered:\n got %q\nwant %q", out.Body, plainText)
	}
}

func TestPreviewCacheAvoidsReformatting(t *testing.T) {
	r := NewRenderer()
	body := []byte("# Title\n\nparagraph\n")
	first := r.Render("a.md", body, int64(len(body)), time.Unix(0, 0), 60)
	second := r.Render("a.md", body, int64(len(body)), time.Unix(0, 0), 60)
	if first.Body != second.Body {
		t.Error("the same file formatted differently twice")
	}
	// A rewrite must not serve the old copy: an agent writing into artifacts/ is
	// the normal case here.
	changed := r.Render("a.md", []byte("# Other\n"), 8, time.Unix(1, 0), 60)
	if changed.Body == first.Body {
		t.Error("a modified file served the cached render")
	}
}

func TestGroupHeadingsAppearOnceInClassOrder(t *testing.T) {
	m := loaded(t, sampleSource())
	view := m.View()
	for _, heading := range []string{"Needs input", "Working", "Unrecognized"} {
		if n := strings.Count(view, heading); n != 1 {
			t.Errorf("heading %q appears %d times, want 1:\n%s", heading, n, view)
		}
	}
	needs, working := strings.Index(view, "Needs input"), strings.Index(view, "Working")
	stray := strings.Index(view, "Unrecognized")
	if needs >= working || working >= stray {
		t.Errorf("headings are out of order: needs=%d working=%d unrecognized=%d", needs, working, stray)
	}
}

func TestDeleteAsksFirstAndNoSingleKeyDoesIt(t *testing.T) {
	src := sampleSource()
	for _, key := range []string{"d", "x", "delete", "enter", "y"} {
		m := loaded(t, src)
		m = focusTask(t, m, dir("alpha-111111"))
		m, _ = step(t, m, pressKey(key))
		if m.done && m.outcome.Action == "delete" {
			t.Errorf("pressing %q alone deleted a task", key)
		}
	}

	// Confirming takes a deliberate second step.
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))
	m, _ = step(t, m, pressKey("d"))
	if m.confirm == nil {
		t.Fatal("d did not open the confirmation")
	}
	if view := m.View(); !strings.Contains(view, "Delete") {
		t.Errorf("the confirmation does not say what it will do:\n%s", view)
	}
	m, _ = step(t, m, pressKey("right"))
	m, _ = step(t, m, pressKey("enter"))
	if !m.done || m.outcome.Action != "delete" {
		t.Errorf("confirming did not return a delete: done=%v outcome=%+v", m.done, m.outcome)
	}
	if m.outcome.Dir != dir("alpha-111111") {
		t.Errorf("the delete names %q, want alpha's directory", m.outcome.Dir)
	}
}

func TestNewTaskReturnsAnOutcomeWithoutADir(t *testing.T) {
	// Creating a task is not an action on a row, so it works from anywhere and
	// carries no directory for the caller to reload.
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))

	m, _ = step(t, m, pressKey("n"))
	if !m.done {
		t.Fatal("n did not leave the dashboard")
	}
	if m.outcome.Action != "new" {
		t.Errorf("n returned %+v, want a \"new\" action", m.outcome)
	}
	if m.outcome.Dir != "" {
		t.Errorf("new task carried a directory %q, want none", m.outcome.Dir)
	}
}

func TestNewTaskAffordanceIsVisible(t *testing.T) {
	// The nav entry is drawn in the pane, so the action is discoverable without
	// opening the help legend.
	m := loaded(t, sampleSource())
	if view := plain(m.View()); !strings.Contains(view, "new task") {
		t.Errorf("the tree pane does not advertise creating a task:\n%s", view)
	}
}

func TestInteractReturnsAttachForTheSelectedTask(t *testing.T) {
	// `i` talks to the task's agent and comes back to the dashboard. It returns
	// the one "attach" action for both a live agent and a task without one — the
	// caller attaches or starts a session as needed — so the view always reopens
	// afterward, unlike `a` which leaves the shell in the task.
	src := sampleSource()

	// A task with a live agent.
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))
	m, _ = step(t, m, pressKey("i"))
	if m.outcome.Action != "attach" {
		t.Errorf("i on a live agent returned %+v, want attach", m.outcome)
	}
	if m.outcome.Dir != dir("alpha-111111") {
		t.Errorf("attach names %q, want alpha", m.outcome.Dir)
	}

	// A task with no session still returns attach, aimed at the owning task; the
	// caller starts a fresh session.
	src2 := sampleSource()
	src2.sessions = src2.sessions[:1] // keep only alpha's
	m2 := loaded(t, src2)
	m2 = focusTask(t, m2, dir("beta-222222"))
	m2, _ = step(t, m2, pressKey("i"))
	if m2.outcome.Action != "attach" {
		t.Errorf("i on a task with no agent returned %+v, want attach", m2.outcome)
	}
	if m2.outcome.Dir != dir("beta-222222") {
		t.Errorf("attach names %q, want beta", m2.outcome.Dir)
	}
}

func TestCtrlCQuitsAndCtrlGDoesNot(t *testing.T) {
	// Quit is a plain C-c now, not an emacs chord. C-g is cancel: it clears the
	// echo line and never leaves the view.
	src := sampleSource()

	m := loaded(t, src)
	m, _ = step(t, m, pressKey("ctrl+c"))
	if !m.done {
		t.Error("C-c did not quit")
	}

	m = loaded(t, src)
	m, _ = step(t, m, pressKey("ctrl+g"))
	if m.done {
		t.Error("C-g quit the dashboard; it should only cancel")
	}

	// q still quits, as it always has.
	m = loaded(t, src)
	m, _ = step(t, m, pressKey("q"))
	if !m.done {
		t.Error("q did not quit")
	}
}

func TestEmacsAndViKeysBothMove(t *testing.T) {
	src := sampleSource()
	for _, keys := range [][]string{{"ctrl+n"}, {"j"}, {"down"}} {
		m := loaded(t, src)
		m, _ = step(t, m, pressKey("alt+<"))
		start := m.cursor
		for _, k := range keys {
			m, _ = step(t, m, pressKey(k))
		}
		if m.cursor == start {
			t.Errorf("%v did not move the cursor", keys)
		}
	}
}

func TestAgentPaneAndLogTail(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))

	if view := m.View(); !strings.Contains(view, "aaa111") {
		t.Errorf("the agents pane does not name the session:\n%s", view)
	}

	m = drain(t, m, m.fetchTail())
	view := m.View()
	if !strings.Contains(view, "Both gaps resolved") {
		t.Errorf("the log tail is missing its content:\n%s", view)
	}
	// What claude records is a capture of its own interface; the escape codes must
	// not reach the pane.
	if strings.Contains(view, "\x1b[38;2;8;145;178m") {
		t.Errorf("raw escape codes leaked into the pane:\n%q", view)
	}
}

func TestFinishedAgentSaysItHasNoLog(t *testing.T) {
	// Most sessions on a real machine are finished, and claude has no log for
	// them. Saying so beats an error that looks like a bug.
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("beta-222222"))

	// Measure only beta's fetch: the live alpha agent's log is read on its own
	// during load now, so the counter is not zero to begin with.
	before := src.logCalls
	m = drain(t, m, m.fetchTail())

	if src.logCalls != before {
		t.Errorf("a finished agent was asked for logs %d times", src.logCalls-before)
	}
	if !strings.Contains(m.View(), "no log") {
		t.Errorf("the pane does not explain why there is no output:\n%s", m.View())
	}
}

func TestRefreshBumpsTheGenerationAndRefetches(t *testing.T) {
	src := sampleSource()
	m := loaded(t, src)
	before, sessionsBefore := m.generation, src.sessionCalls

	m, cmd := step(t, m, pressKey("ctrl+r"))
	if m.generation == before {
		t.Error("C-r did not bump the generation")
	}

	m = drain(t, m, cmd)
	if src.sessionCalls <= sessionsBefore {
		t.Error("C-r did not re-query the sessions")
	}
	// The refresh must land rather than be discarded as its own stale reply: the
	// generation it was issued under is the one still current.
	if view := m.View(); !strings.Contains(view, "alpha-111111") {
		t.Errorf("the tree is empty after a refresh:\n%s", view)
	}
}

func TestLayoutFitsTheTerminal(t *testing.T) {
	src := sampleSource()
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {60, 20}, {160, 50}} {
		m := newDashboard(DashboardConfig{
			Title: "tasks", Source: src,
			StatusEvery: time.Hour, SessionsEvery: time.Hour, PreviewDelay: time.Millisecond,
		})
		next, _ := m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		m = next.(dashboard)
		m, _ = step(t, m, tasksMsg{nodes: cloneNodes(src.tasks), gen: m.generation})

		view := m.View()
		lines := strings.Split(view, "\n")
		if len(lines) > size.h {
			t.Errorf("%dx%d: rendered %d lines, more than the terminal has",
				size.w, size.h, len(lines))
		}
		for i, line := range lines {
			if w := lipgloss.Width(line); w > size.w {
				t.Errorf("%dx%d: line %d is %d cells wide:\n%s",
					size.w, size.h, i, w, line)
			}
		}
	}
}

func TestQuitErasesTheView(t *testing.T) {
	// The caller prints after this returns — a directory to cd to, or the output
	// of a delete — and a stale final frame above that reads as part of it.
	m := loaded(t, sampleSource())
	m, _ = step(t, m, pressKey("q"))
	if !m.done {
		t.Fatal("q did not quit")
	}
	if view := m.View(); view != "" {
		t.Errorf("the dashboard still renders after quitting:\n%s", view)
	}
}

func TestActionsReportTheTaskNotTheFile(t *testing.T) {
	// Acting on a file inside a task should do the obvious thing: open the task
	// it belongs to, rather than nothing.
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))
	m, cmd := step(t, m, pressKey("ctrl+f"))
	m = drain(t, m, cmd)
	m = selectPath(t, m, filepath.Join(dir("alpha-111111"), "artifacts"))

	m, _ = step(t, m, pressKey("o"))
	if m.outcome.Action != "open" {
		t.Fatalf("o on a directory row returned %+v", m.outcome)
	}
	if m.outcome.Dir != dir("alpha-111111") {
		t.Errorf("the action names %q, want the owning task", m.outcome.Dir)
	}
}

func TestFilterNarrowsTheTree(t *testing.T) {
	m := loaded(t, sampleSource())
	m, _ = step(t, m, pressKey("ctrl+s"))
	for _, r := range "beta" {
		m, _ = step(t, m, pressKey(string(r)))
	}
	view := m.View()
	if !strings.Contains(view, "beta-222222") {
		t.Errorf("the filter hid the row that matches:\n%s", view)
	}
	if strings.Contains(view, "alpha-111111") {
		t.Errorf("the filter kept a row that does not match:\n%s", view)
	}
}

func TestTabAndShiftTabSwitchPanes(t *testing.T) {
	// There is only somewhere to switch to once the detail pane has something in
	// it, so give it a previewed file first.
	src := sampleSource()
	notes := filepath.Join(dir("alpha-111111"), "artifacts", "notes.md")
	m := loaded(t, src)
	m = expandTo(t, m, dir("alpha-111111"), filepath.Join(dir("alpha-111111"), "artifacts"))
	m = selectPath(t, m, notes)
	m, _ = step(t, m, previewMsg{path: notes, content: src.content[notes], size: int64(len(src.content[notes]))})

	if m.focus != focusTree {
		t.Fatalf("focus started on %v, want the tree", m.focus)
	}
	m, _ = step(t, m, pressKey("tab"))
	if m.focus != focusPreview {
		t.Errorf("tab did not move focus to the preview: %v", m.focus)
	}
	m, _ = step(t, m, pressKey("tab"))
	if m.focus != focusTree {
		t.Errorf("tab did not move focus back to the tree: %v", m.focus)
	}

	// Shift-Tab toggles the same way, so the key someone reaches for works.
	m, _ = step(t, m, pressKey("shift+tab"))
	if m.focus != focusPreview {
		t.Errorf("shift+tab did not move focus to the preview: %v", m.focus)
	}
}

func TestAgentLogLoadsWithoutAKeystroke(t *testing.T) {
	// The log used to wait for C-c C-l. Now it loads on its own: landing on a task
	// with a live agent is enough.
	src := sampleSource()
	m := loaded(t, src)
	m = focusTask(t, m, dir("alpha-111111"))

	// loadSessions ends by fetching the tail for the selection; drain that fetch
	// the way the runtime would, then confirm the output is on screen with no key
	// pressed.
	m = drain(t, m, m.loadSessions(m.generation))
	if src.logCalls == 0 {
		t.Error("the agent log was never fetched without a keystroke")
	}
	if view := plain(m.View()); !strings.Contains(view, "Both gaps resolved") {
		t.Errorf("the agent log did not auto-load into the pane:\n%s", view)
	}
}

func TestTitleShowsAHighlightedWorkingIndicator(t *testing.T) {
	// A visible, highlighted chip says when work is in flight — a running agent,
	// or a refresh the user just asked for.
	src := sampleSource()
	// Give one session the working class, so the chip has something to count.
	src.sessions[1].Class = classWorking
	m := loaded(t, src)

	if view := plain(m.View()); !strings.Contains(view, "1 working") {
		t.Errorf("the title does not count the working agent:\n%s", view)
	}

	// A refresh outranks the count and names itself.
	m, _ = step(t, m, pressKey("ctrl+r"))
	if !m.refreshing {
		t.Fatal("C-r did not raise the refreshing flag")
	}
	if view := plain(m.View()); !strings.Contains(view, "refreshing") {
		t.Errorf("a running refresh is not shown in the title:\n%s", view)
	}

	// And it comes down once the session list — the last read — lands.
	m, _ = step(t, m, sessionsMsg{sessions: src.sessions, gen: m.generation})
	if m.refreshing {
		t.Error("the refreshing indicator stayed up after the sessions arrived")
	}
}

// focusTask puts the cursor on a task by path.
func focusTask(t *testing.T, m dashboard, path string) dashboard {
	t.Helper()
	return selectPath(t, m, path)
}

// expandTo opens each path in turn, which is what reaching a file takes: a task
// lists its directories, and a directory lists its files, one level per step.
func expandTo(t *testing.T, m dashboard, paths ...string) dashboard {
	t.Helper()
	for _, path := range paths {
		m = selectPath(t, m, path)
		next, cmd := step(t, m, pressKey("ctrl+f"))
		m = drain(t, next, cmd)
	}
	return m
}

func selectPath(t *testing.T, m dashboard, path string) dashboard {
	t.Helper()
	at := m.tree.find(path)
	if at < 0 {
		t.Fatalf("no row for %s in:\n%s", path, rowDump(m))
	}
	m.cursor = at
	m.clampCursor()
	return m
}

// hasRow reports whether a label is among the visible rows.
//
// Asserted against the rows rather than against View(), because a name column
// clips what it draws — "artifacts/" renders as "artifact…" in a narrow pane — and
// a test for the model's structure should not fail over how wide the terminal is.
func hasRow(m dashboard, label string) bool {
	for _, row := range m.tree.rows {
		if row.Node.Label == label {
			return true
		}
	}
	return false
}

func rowDump(m dashboard) string {
	var b strings.Builder
	for _, row := range m.tree.rows {
		fmt.Fprintf(&b, "  %d %s %s\n", row.Depth, row.Node.Label, row.Node.Path)
	}
	return b.String()
}
