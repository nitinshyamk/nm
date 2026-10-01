package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// NodeKind says what a tree row represents, which decides what can be done to
// it: only a task can be entered or deleted, only a file can be previewed.
type NodeKind int

// The kinds of row the tree holds.
const (
	NodeGroup  NodeKind = iota // a heading such as "Needs input"; not selectable
	NodeTask                   // a task directory with a readable .nm-task.json
	NodeOrphan                 // a directory under the tasks root that is not a task
	NodeDir                    // artifacts/, input/, scratch/, escalations/, or deeper
	NodeFile
)

// Node is one entry in the tree. Children are filled in when a directory is
// expanded and are nil until then, so opening a task costs one ReadDir rather
// than a walk of everything beneath it.
type Node struct {
	Kind     NodeKind
	Label    string // what the row says
	Path     string // absolute, canonicalized; "" for a group heading
	Size     int64  // files only
	Children []*Node
	Loaded   bool // the children have been read, even if there are none

	// Status is the per-task columns: agent state, git badges, pull requests.
	// Nil while the git survey for this task is still in flight, which is what
	// the spinner in the status column stands for.
	Status *RowStatus

	// Detail is what the right pane shows for a task: repos, prompt, workplan.
	Detail *TaskDetail
}

// RowStatus is the resolved state of one task, in column order.
type RowStatus struct {
	Agent  string  // "working", "blocked", "done"; "" when no agent
	Class  int     // agent.Class ordinal, for the badge color
	Badges []Badge // git status chips, or a single "clean"
	PRs    string  // "#12 #34", or ""
	Stale  bool    // the last refresh failed, so these numbers are old
}

// TaskDetail is everything the right pane shows about a task, all of it already
// known from .nm-task.json so selecting a task costs no I/O.
type TaskDetail struct {
	Dir      string
	Repos    []RepoLine
	Prompt   string
	Workplan string
	TaskID   string
	TaskFile string
	Note     string // why a row is not a task, for an orphan
}

// RepoLine is one repository inside a task.
type RepoLine struct {
	Name   string
	Branch string
	Base   string
	PR     string
	Err    string // the status read failed, and saying so beats showing "clean"
}

// treeRow is a flattened, renderable line: a node plus how deep it sits.
//
// Named for the tree rather than taking the bare name, because Row in this
// package is already the picker's selectable entry.
type treeRow struct {
	Node     *Node
	Depth    int
	Expanded bool
	HasKids  bool
}

// tree holds the nodes and which paths are open. Expansion is keyed by path
// rather than by position so it survives a refresh that reorders rows — a task
// whose agent finishes moves group, and it must not collapse when it does.
type tree struct {
	roots    []*Node
	expanded map[string]bool
	rows     []treeRow // rebuilt by reflow

	// filter narrows the rows. It lives here rather than in the model so there is
	// only ever one list of rows: the cursor indexes the same slice that gets
	// drawn, and a filter cannot leave the highlight on a row nobody can see.
	filter string
}

func newTree() *tree {
	return &tree{expanded: make(map[string]bool)}
}

// canonical normalizes a path for use as a key. Paths that reach nm from
// different places are spelled differently — a bare != on a path has been a bug
// every time it appeared in this repo — and expansion state is compared across
// refreshes, so the key has to be stable.
func canonical(path string) string {
	if path == "" {
		return ""
	}
	// Deliberately not EvalSymlinks: this runs on every row of every reflow, and
	// a stat per row per frame is too much for a key that only has to be
	// consistent with itself. Clean plus a case fold on Windows gets separator,
	// "..", and drive-letter case, which is what differs in practice here.
	out := filepath.Clean(path)
	if filepath.Separator == '\\' {
		out = strings.ToLower(out)
	}
	return out
}

// setRoots replaces the tree's contents, keeping expansion state.
func (t *tree) setRoots(roots []*Node) {
	t.roots = roots
	t.reflow()
}

func (t *tree) isExpanded(n *Node) bool {
	if n.Path == "" {
		return true // group headings are always open
	}
	return t.expanded[canonical(n.Path)]
}

func (t *tree) expand(n *Node)   { t.expanded[canonical(n.Path)] = true }
func (t *tree) collapse(n *Node) { delete(t.expanded, canonical(n.Path)) }

// canExpand reports whether a row opens into anything.
func canExpand(n *Node) bool {
	switch n.Kind {
	case NodeTask, NodeDir, NodeOrphan:
		return true
	default:
		return false
	}
}

// reflow rebuilds the visible rows from the expansion state. Children of a
// collapsed node are skipped entirely, so the cursor walks tasks without wading
// through the files of one that happens to be open.
//
// With a filter set the tree flattens to just the matching tasks: a hierarchy
// pruned to what matched is harder to read than a list, and the point of typing
// a filter is to get to one task.
func (t *tree) reflow() {
	t.rows = t.rows[:0]

	if needle := strings.ToLower(strings.TrimSpace(t.filter)); needle != "" {
		var match func(nodes []*Node)
		match = func(nodes []*Node) {
			for _, n := range nodes {
				if n.Kind != NodeGroup && strings.Contains(strings.ToLower(n.Label), needle) {
					t.rows = append(t.rows, treeRow{Node: n, HasKids: canExpand(n)})
					continue
				}
				match(n.Children)
			}
		}
		match(t.roots)
		return
	}

	var walk func(nodes []*Node, depth int)
	walk = func(nodes []*Node, depth int) {
		for _, n := range nodes {
			open := t.isExpanded(n)
			t.rows = append(t.rows, treeRow{
				Node:     n,
				Depth:    depth,
				Expanded: open,
				HasKids:  canExpand(n),
			})
			if open && len(n.Children) > 0 {
				walk(n.Children, depth+1)
			}
		}
	}
	walk(t.roots, 0)
}

// find returns the row index of a path, or -1. Used to keep the cursor on the
// same task across a refresh.
func (t *tree) find(path string) int {
	want := canonical(path)
	for i, row := range t.rows {
		if row.Node.Path != "" && canonical(row.Node.Path) == want {
			return i
		}
	}
	return -1
}

// parentOf returns the row index holding the given row's parent, or -1.
func (t *tree) parentOf(index int) int {
	if index < 0 || index >= len(t.rows) {
		return -1
	}
	depth := t.rows[index].Depth
	for i := index - 1; i >= 0; i-- {
		if t.rows[i].Depth < depth {
			return i
		}
	}
	return -1
}

// Column widths for the status table. The name column takes whatever is left,
// so these are the fixed costs subtracted from the pane.
const (
	colAgent = 9
	colGit   = 12
	colPR    = 7
	colSize  = 8
)

// columnPlan is which status columns fit the pane. They drop right to left:
// pull requests first, because the number is also in the detail pane, then
// size, then git, leaving the name and the agent — the two that say whether a
// task needs you.
type columnPlan struct {
	agent bool
	git   bool
	pr    bool
	size  bool
	name  int // cells available to the name column
}

// planColumns decides which columns fit a pane of the given width.
func planColumns(width int) columnPlan {
	const minName = 16
	plan := columnPlan{agent: true, git: true, pr: true, size: true}
	for {
		used := 0
		if plan.agent {
			used += colAgent
		}
		if plan.git {
			used += colGit
		}
		if plan.pr {
			used += colPR
		}
		if plan.size {
			used += colSize
		}
		if name := width - used; name >= minName {
			plan.name = name
			return plan
		}
		switch {
		case plan.pr:
			plan.pr = false
		case plan.size:
			plan.size = false
		case plan.git:
			plan.git = false
		case plan.agent:
			plan.agent = false
		default:
			plan.name = max(width, 1)
			return plan
		}
	}
}

// header is the column legend drawn above the rows.
func (p columnPlan) header() string {
	var b strings.Builder
	b.WriteString(pad("NAME", p.name))
	if p.agent {
		b.WriteString(pad("AGENT", colAgent))
	}
	if p.git {
		b.WriteString(pad("GIT", colGit))
	}
	if p.size {
		b.WriteString(padLeft("SIZE", colSize))
	}
	if p.pr {
		b.WriteString(padLeft("PR", colPR))
	}
	return styleColumnHead.Render(b.String())
}

// renderRow draws one row with its columns aligned. spinner is the frame to
// show where a status has not arrived yet; it occupies the same slot the value
// will, so nothing shifts when the survey lands.
func renderRow(row treeRow, plan columnPlan, selected bool, spinner string) string {
	name := treeName(row, plan.name)

	var b strings.Builder
	b.WriteString(name)

	if row.Node.Kind == NodeGroup {
		return styleGroupRow.Render(b.String())
	}

	status := row.Node.Status
	isTask := row.Node.Kind == NodeTask
	pending := status == nil && isTask

	if plan.agent {
		switch {
		case pending:
			b.WriteString(pad(spinner, colAgent))
		case status != nil && status.Agent != "":
			b.WriteString(badgeStyle(classBadgeKind(status.Class)).Render(pad(trim(status.Agent, colAgent-1), colAgent)))
		case isTask:
			// Only a task can have an agent, so only a task says it has none. A dash
			// against a file would read as a value that failed to load.
			b.WriteString(pad("—", colAgent))
		default:
			b.WriteString(pad("", colAgent))
		}
	}
	if plan.git {
		switch {
		case pending:
			b.WriteString(pad(spinner, colGit))
		case status != nil:
			b.WriteString(padBadges(status.Badges, colGit, status.Stale))
		default:
			b.WriteString(pad("", colGit))
		}
	}
	if plan.size {
		if row.Node.Kind == NodeFile {
			b.WriteString(padLeft(humanSize(row.Node.Size), colSize))
		} else {
			b.WriteString(pad("", colSize))
		}
	}
	if plan.pr {
		if status != nil && status.PRs != "" {
			b.WriteString(padLeft(badgeStyle(BadgeOK).Render(status.PRs), colPR))
		} else {
			b.WriteString(pad("", colPR))
		}
	}

	line := strings.TrimRight(b.String(), " ")
	if selected {
		return styleSelected.Render(line)
	}
	return line
}

// treeName renders the indent, the fold marker, and the label, clipped to the
// name column so the status columns stay aligned.
func treeName(row treeRow, width int) string {
	indent := strings.Repeat("  ", row.Depth)

	marker := "  "
	if row.HasKids {
		if row.Expanded {
			marker = "▾ "
		} else {
			marker = "▸ "
		}
	}

	label := row.Node.Label
	if row.Node.Kind == NodeDir {
		// An open directory says how much is in it, which is the difference
		// between an empty artifacts/ and one worth looking in.
		label += "/" + fileCount(row.Expanded, len(row.Node.Children))
	}

	prefix := indent + marker
	// One cell short of the column, so a clipped name never butts up against the
	// status beside it.
	body := trim(label, max(width-lipgloss.Width(prefix)-1, 1))

	switch row.Node.Kind {
	case NodeGroup:
		body = styleGroupRow.Render(body)
	case NodeOrphan:
		body = styleMuted.Render(body)
	case NodeDir:
		body = styleDirName.Render(body)
	}
	return pad(prefix+body, width)
}

// padBadges fits status chips into the git column, dropping any that do not fit
// rather than pushing the later columns out of line.
func padBadges(badges []Badge, width int, stale bool) string {
	if len(badges) == 0 {
		return pad("", width)
	}
	var parts []string
	used := 0
	for _, badge := range badges {
		w := lipgloss.Width(badge.Text) + 1
		if used+w > width {
			break
		}
		style := badgeStyle(badge.Kind)
		if stale {
			style = styleMuted
		}
		parts = append(parts, style.Render(badge.Text))
		used += w
	}
	return pad(strings.Join(parts, " "), width)
}

// pad and padLeft align a cell to a column, measuring in display cells rather
// than bytes: the badges and fold markers are multi-byte, and alignment is the
// whole point of the columns.
func pad(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

func padLeft(s string, width int) string {
	if gap := width - lipgloss.Width(s); gap > 0 {
		return strings.Repeat(" ", gap) + s
	}
	return s
}

// trim clips a label to fit, keeping the front. Unlike a path, the informative
// end of a task or file name is the beginning.
func trim(s string, width int) string {
	if width <= 1 {
		return ""
	}
	if lipgloss.Width(s) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes))+1 > width {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// Agent class ordinals, mirroring internal/agent's Class constants. This package
// does not import that one — a view should not depend on the thing that shells out
// to claude — so the caller passes the number and the names live here.
const (
	classNeedsInput = 0
	classDone       = 1
	classWorking    = 3
	classNone       = 4
)

// classBadgeKind maps an agent class ordinal to a badge color.
func classBadgeKind(class int) BadgeKind {
	switch class {
	case classNeedsInput:
		return BadgeDanger
	case classDone:
		return BadgeOK
	case classWorking:
		return BadgeInfo
	default:
		return BadgeNeutral
	}
}

// fileCount annotates an open directory with how much is inside it. A closed one
// says nothing, because nothing has been read yet and "(0)" would be a claim
// rather than a count.
func fileCount(expanded bool, n int) string {
	if !expanded || n == 0 {
		return ""
	}
	return fmt.Sprintf(" (%d)", n)
}
