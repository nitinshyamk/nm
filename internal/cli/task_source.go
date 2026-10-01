package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nitinshyamk/nm/internal/agent"
	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/gitx"
	"github.com/nitinshyamk/nm/internal/task"
	"github.com/nitinshyamk/nm/internal/tui"
)

// taskSource is the dashboard's window onto the tasks root, the git worktrees
// under it, and the claude sessions working in them.
//
// It exists so the view itself touches none of those: the model is handed an
// interface it can be tested against, the same way workplan.Agents and
// task.Forge let their state machines be tested without launching anything.
type taskSource struct {
	cfg config.Config
}

var _ tui.Source = taskSource{}

// Tasks lists the tasks root, cheaply and on purpose.
//
// A ReadDir plus one small JSON file per task — about a millisecond for a real
// root — so the tree is on screen before the git survey or the session query has
// started. Everything expensive about a task arrives later, through Status.
func (s taskSource) Tasks() ([]*tui.Node, error) {
	root := s.cfg.Tasks()
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", root, err)
	}

	var (
		tasks   []task.Task
		orphans []*tui.Node
	)
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		t, loadErr := task.Load(dir)
		if loadErr != nil {
			// A directory under the tasks root with no readable .nm-task.json is
			// invisible to every other nm command, which is how worktrees end up
			// stranded where nothing will clean them up. The dashboard shows them.
			orphans = append(orphans, orphanNode(dir, loadErr))
			continue
		}
		tasks = append(tasks, t)
	}

	// Newest first within a group, matching the old listing.
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})

	return s.group(tasks, orphans), nil
}

// group buckets tasks under the headings the old list used.
//
// Deliberately without asking claude which agents are alive. Grouping by live
// session state would make the heading a task sits under depend on a ~600ms
// subprocess, and this is the call that has to return before the first frame —
// paying for it here would undo the whole point of the tree painting instantly.
// So the bucket comes from what the task file already records: a task that was
// given an agent goes under "Working", one that never was goes under "No agent",
// and the real state arrives with Status a moment later, in the AGENT column.
func (s taskSource) group(tasks []task.Task, orphans []*tui.Node) []*tui.Node {
	type bucket struct {
		label string
		kids  []*tui.Node
	}
	withAgent, without := bucket{label: "Working"}, bucket{label: "No agent"}

	for _, t := range tasks {
		node := s.taskNode(t)
		if t.Agent != nil {
			withAgent.kids = append(withAgent.kids, node)
		} else {
			without.kids = append(without.kids, node)
		}
	}

	var roots []*tui.Node
	for _, b := range []bucket{withAgent, without} {
		if len(b.kids) == 0 {
			continue
		}
		roots = append(roots, &tui.Node{
			Kind:     tui.NodeGroup,
			Label:    b.label,
			Children: b.kids,
			Loaded:   true,
		})
	}
	if len(orphans) > 0 {
		roots = append(roots, &tui.Node{
			Kind:     tui.NodeGroup,
			Label:    "Unrecognized",
			Children: orphans,
			Loaded:   true,
		})
	}
	return roots
}

// taskNode builds one task's row and the detail pane behind it. Everything here
// comes from the file already read, so selecting a task costs no I/O.
func (s taskSource) taskNode(t task.Task) *tui.Node {
	detail := &tui.TaskDetail{
		Dir:      t.Dir,
		Prompt:   t.Prompt,
		Workplan: t.Workplan,
		TaskID:   t.TaskID,
		TaskFile: t.TaskFile,
	}
	for _, r := range t.Repos {
		line := tui.RepoLine{Name: r.Name, Branch: r.Branch, Base: r.BaseBranch}
		if r.PRNumber > 0 {
			line.PR = "#" + strconv.Itoa(r.PRNumber)
		}
		detail.Repos = append(detail.Repos, line)
	}

	// Status stays nil: it is what the spinner in the columns stands for, and it
	// arrives from Status(dir) once the git and session reads come back.
	return &tui.Node{
		Kind:   tui.NodeTask,
		Label:  t.Label(),
		Path:   t.Dir,
		Detail: detail,
	}
}

// orphanNode describes a directory that is not a task.
func orphanNode(dir string, loadErr error) *tui.Node {
	note := "no .nm-task.json — nm cannot manage this directory"
	if !os.IsNotExist(loadErr) {
		note = "unreadable .nm-task.json: " + loadErr.Error()
	}

	// Say what is inside, because that is what decides whether it matters: a
	// stranded worktree is work, an empty directory is debris.
	var inside []string
	if entries, err := os.ReadDir(dir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				inside = append(inside, e.Name())
			}
		}
	}
	switch {
	case len(inside) == 0:
		note += " (empty)"
	default:
		note += " (holds " + strings.Join(inside, ", ") + ")"
	}

	return &tui.Node{
		Kind:   tui.NodeOrphan,
		Label:  filepath.Base(dir),
		Path:   dir,
		Detail: &tui.TaskDetail{Dir: dir, Note: note},
	}
}

// Status reads one task's git state: a couple of git invocations per repository,
// which is the slow part of the whole view and the reason it is per task.
func (s taskSource) Status(dir string) (tui.RowStatus, error) {
	t, err := task.Load(dir)
	if err != nil {
		return tui.RowStatus{}, err
	}

	var (
		out   tui.RowStatus
		total gitx.Status
		fail  error
	)
	for _, r := range t.Repos {
		st, statusErr := gitx.GetStatusNoAge(r.Dir)
		if statusErr != nil {
			fail = statusErr
			continue
		}
		total.Staged += st.Staged
		total.Unstaged += st.Unstaged
		total.Untracked += st.Untracked
		total.Unpushed += st.Unpushed
	}

	out.Badges = statusBadges(total)
	if prs := prBadge(t); prs != "" {
		out.PRs = strings.TrimPrefix(prs, "pr ")
	}
	if artifacts, _ := task.CountArtifacts(t.Artifacts(s.cfg)); artifacts > 0 {
		out.Badges = append(out.Badges, tui.Badge{
			Text: "a" + strconv.Itoa(artifacts),
			Kind: tui.BadgeInfo,
		})
	}

	// No session query here. Sessions() answers once per refresh for the whole
	// view, and the dashboard matches them to rows itself; asking per task would
	// spawn `claude agents` once per task — eighteen subprocesses at ~600ms each
	// for one answer that does not vary between them.

	// A repository whose status could not be read is reported, but the rest of the
	// task's numbers are still worth showing.
	if fail != nil && len(t.Repos) == 1 {
		return out, fail
	}
	return out, nil
}

// Sessions lists the claude agents nm knows about.
func (s taskSource) Sessions() ([]tui.SessionInfo, error) {
	client := agent.Client{Bin: s.cfg.ClaudeCommand}
	if !client.Available() {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), agent.ListTimeout)
	defer cancel()

	sessions, err := client.List(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]tui.SessionInfo, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, tui.SessionInfo{
			ID:       session.AttachID(),
			Describe: session.Describe(),
			Class:    int(session.Class()),
			Name:     session.Name,
			Dir:      session.CWD,
			Age:      sessionAge(session),
			Live:     session.Class() != agent.ClassDone,
		})
	}
	return out, nil
}

// Logs returns a session's recent output, already stripped of escape codes.
func (s taskSource) Logs(id string) (string, error) {
	client := agent.Client{Bin: s.cfg.ClaudeCommand}
	if !client.Available() {
		return "", fmt.Errorf("%s is not on PATH", s.cfg.ClaudeCommand)
	}
	ctx, cancel := context.WithTimeout(context.Background(), agent.LogTimeout)
	defer cancel()
	return client.Logs(ctx, id)
}

// Files reads one level of a directory. A task expands to the directories it
// always has; anything deeper is read when it is reached.
func (s taskSource) Files(dir string) ([]*tui.Node, error) {
	if t, err := task.Load(dir); err == nil {
		return s.taskChildren(t), nil
	}
	return readLevel(dir)
}

// taskChildren is a task's fixed directories, plus its worktrees. Only
// directories that exist are listed, so a task made before escalations/ existed
// does not show an empty one.
func (s taskSource) taskChildren(t task.Task) []*tui.Node {
	var out []*tui.Node
	for _, dir := range []string{
		t.Input(s.cfg), t.Artifacts(s.cfg), t.Scratch(s.cfg), t.Escalations(s.cfg),
	} {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		out = append(out, &tui.Node{
			Kind:  tui.NodeDir,
			Label: filepath.Base(dir),
			Path:  dir,
		})
	}
	for _, r := range t.Repos {
		if info, err := os.Stat(r.Dir); err == nil && info.IsDir() {
			out = append(out, &tui.Node{
				Kind:  tui.NodeDir,
				Label: r.Name,
				Path:  r.Dir,
			})
		}
	}
	return out
}

// readLevel lists one directory, directories first and then files, each
// alphabetically — the order someone scanning for a document expects.
func readLevel(dir string) ([]*tui.Node, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var dirs, files []*tui.Node
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue // .git and friends are not what the pane is for
		}
		path := filepath.Join(dir, name)
		if e.IsDir() {
			dirs = append(dirs, &tui.Node{Kind: tui.NodeDir, Label: name, Path: path})
			continue
		}
		node := &tui.Node{Kind: tui.NodeFile, Label: name, Path: path, Loaded: true}
		if info, infoErr := e.Info(); infoErr == nil {
			node.Size = info.Size()
		}
		files = append(files, node)
	}
	return append(dirs, files...), nil
}

// Read returns a file's content for the preview, capped so a very large file
// cannot stall the view, along with the real size so the pane can say there is
// more.
func (s taskSource) Read(path string) ([]byte, int64, time.Time, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, 0, time.Time{}, err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, 0, time.Time{}, err
	}
	defer func() { _ = file.Close() }()

	buf := make([]byte, min64(info.Size(), tui.RenderCap))
	read, err := io.ReadFull(file, buf)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, 0, time.Time{}, err
	}
	return buf[:read], info.Size(), info.ModTime(), nil
}

// sessionAge renders how long an agent has been going.
func sessionAge(session agent.Session) string {
	started := session.Started()
	if started.IsZero() {
		return ""
	}
	return humanAge(time.Since(started))
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
