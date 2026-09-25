// Package skills holds the agent skills that drive the nm workplan commands, and
// installs them into a user's skill directory.
//
// They are embedded in the binary rather than shipped as loose files so they
// version with the CLI they invoke. A skill that names a flag the installed nm
// does not have is worse than no skill: it fails in the middle of unattended work,
// where a missing skill fails immediately.
package skills

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed all:assets
var assets embed.FS

// Root is the directory inside the embedded filesystem holding one directory per
// skill.
const Root = "assets"

// File is the name every skill's body has, in every client that reads them.
const File = "SKILL.md"

// Skill is one embedded skill.
type Skill struct {
	Name string
	Body string
}

// All returns every embedded skill, by name.
func All() ([]Skill, error) {
	entries, err := fs.ReadDir(assets, Root)
	if err != nil {
		return nil, fmt.Errorf("reading the embedded skills: %w", err)
	}

	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		body, err := fs.ReadFile(assets, path(e.Name()))
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", e.Name(), err)
		}
		out = append(out, Skill{Name: e.Name(), Body: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func path(name string) string { return Root + "/" + name + "/" + File }

// Retired names skills nm used to install and no longer does.
//
// Install has to delete these rather than merely stop writing them. A skill left
// behind in ~/.claude/skills stays invocable, and a retired one is retired because
// something else now covers its job — so what lingers is an older, contradictory
// copy of live instructions, which is worse than no skill at all. nm-task-refine
// went when its content became a phase of nm-task-execute (design O7), and an
// agent reading both would find two escalation paths and two answers about when to
// advance ready-to-review.md.
//
// A name stays on this list. Dropping it once "everyone has upgraded" is how the
// one machine that did not keeps the stale file forever.
var Retired = []string{"nm-task-refine"}

// Action is what installing one skill did.
type Action string

// The outcomes of installing one skill.
const (
	Installed Action = "installed" // written fresh
	Unchanged Action = "unchanged" // already identical
	Replaced  Action = "replaced"  // overwritten under --force
	Conflict  Action = "conflict"  // present and different, left alone
	Refused   Action = "refused"   // a symlink, or otherwise not safe to write
	Removed   Action = "removed"   // retired, and deleted from the skills directory
	Kept      Action = "kept"      // retired, but not safe to delete; needs a human
)

// Result is one skill's installation outcome.
type Result struct {
	Name   string
	Path   string
	Action Action
	Reason string
}

// OK reports whether the skill ended up installed and current.
//
// Removed counts: a retired skill that is gone is the state the caller wanted.
func (r Result) OK() bool {
	return r.Action == Installed || r.Action == Unchanged ||
		r.Action == Replaced || r.Action == Removed
}

// InstallOptions controls an install run.
type InstallOptions struct {
	// Dir is the skills directory, normally ~/.claude/skills.
	Dir string
	// Force replaces a file that is present and different.
	Force bool
	// DryRun reports what would happen and writes nothing.
	DryRun bool
}

// Install writes every embedded skill into the skills directory, and deletes any
// skill nm has retired.
//
// The whole run is checked before anything is written, so a conflict in the third
// skill does not leave the first two installed and the set half-updated. A
// symlinked destination is refused even with --force: following one would write
// through to wherever it points, which is not a place the caller named.
func Install(opts InstallOptions) ([]Result, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}

	// live stays index-parallel with all, because the write loop below pairs them.
	// The retired ones are a separate slice for that reason, and every return joins
	// the two so a caller always sees the whole picture.
	live := make([]Result, 0, len(all))
	blocked := false
	for _, s := range all {
		dest := filepath.Join(opts.Dir, s.Name, File)
		result := Result{Name: s.Name, Path: dest}

		switch existing, state := inspect(dest, s.Body); state {
		case Unchanged:
			result.Action = Unchanged
		case Refused:
			result.Action, result.Reason, blocked = Refused, existing, true
		case Conflict:
			if opts.Force {
				result.Action = Replaced
			} else {
				result.Action, result.Reason, blocked = Conflict, existing, true
			}
		default:
			result.Action = Installed
		}
		live = append(live, result)
	}

	retired := inspectRetired(opts.Dir)
	report := func() []Result { return append(live, retired...) }

	if blocked || opts.DryRun {
		return report(), nil
	}

	for i := range live {
		s := all[i]
		if live[i].Action == Unchanged {
			continue
		}
		dir := filepath.Dir(live[i].Path)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return report(), fmt.Errorf("creating %s: %w", dir, err)
		}
		if err := os.WriteFile(live[i].Path, []byte(s.Body), 0o644); err != nil {
			return report(), fmt.Errorf("writing %s: %w", live[i].Path, err)
		}
	}

	for _, r := range retired {
		if r.Action != Removed {
			continue
		}
		if err := os.Remove(r.Path); err != nil && !os.IsNotExist(err) {
			return report(), fmt.Errorf("removing %s: %w", r.Path, err)
		}
		// And the directory it sat in, if nothing else is in it. A non-empty one
		// fails here and is left alone, which is the wanted behaviour: whatever else
		// the user keeps in there is theirs.
		_ = os.Remove(filepath.Dir(r.Path))
	}
	return report(), nil
}

// inspectRetired reports what should happen to each retired skill still on disk.
//
// A retired skill that is absent is reported as nothing at all: the normal state
// on a fresh machine is that it was never installed, and listing it every run
// would make `install-skills` noisier forever for no reason.
//
// A retired skill is deleted whether or not the user edited it, and `--force` does
// not gate that. The conflict rule exists to protect an edit to a skill that is
// still live — an edit to a retired one is an edit to instructions nothing should
// follow any more, and keeping it is the outcome being prevented.
//
// A symlink is the exception and is reported as Kept: deleting the link itself
// would be safe, but the user pointed it somewhere deliberately, so this says so
// and leaves it. Kept rather than Refused because it must not block the install —
// the live skills are what an unattended agent needs, and holding all of them back
// over one stale symlink would trade a small problem for a larger one.
func inspectRetired(dir string) []Result {
	var out []Result
	for _, name := range Retired {
		dest := filepath.Join(dir, name, File)
		info, err := os.Lstat(dest)
		switch {
		case os.IsNotExist(err):
			continue
		case err != nil:
			out = append(out, Result{Name: name, Path: dest, Action: Kept, Reason: err.Error()})
		case info.Mode()&os.ModeSymlink != 0:
			out = append(out, Result{
				Name: name, Path: dest, Action: Kept,
				Reason: "it is a symlink to a retired skill; delete it by hand",
			})
		default:
			out = append(out, Result{Name: name, Path: dest, Action: Removed})
		}
	}
	return out
}

// inspect reports what is already at a destination.
func inspect(dest, body string) (reason string, state Action) {
	info, err := os.Lstat(dest)
	if os.IsNotExist(err) {
		return "", Installed
	}
	if err != nil {
		return err.Error(), Refused
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "it is a symlink; resolve it before installing", Refused
	}
	if !info.Mode().IsRegular() {
		return "it is not a regular file", Refused
	}
	current, err := os.ReadFile(dest)
	if err != nil {
		return err.Error(), Refused
	}
	if string(current) == body {
		return "", Unchanged
	}
	return "it differs from the bundled version; --force replaces it", Conflict
}

// DefaultDir is where Claude Code reads user skills from.
func DefaultDir(home string) string { return filepath.Join(home, ".claude", "skills") }

// Commands extracts every `nm ...` invocation the skills name, so a test can check
// them against the real command tree.
//
// This is the check that stops a skill from drifting away from the CLI it drives.
// A skill is read by an agent working unattended: a command that does not exist
// fails in the middle of the work, long after anyone could have noticed.
func Commands() ([]string, error) {
	all, err := All()
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var out []string
	for _, s := range all {
		for _, cmd := range findCommands(s.Body) {
			if _, ok := seen[cmd]; ok {
				continue
			}
			seen[cmd] = struct{}{}
			out = append(out, cmd)
		}
	}
	sort.Strings(out)
	return out, nil
}

// findCommands pulls `nm <words>` out of the fenced code blocks and inline code
// spans of one skill.
//
// Only the subcommand path is returned — flags and arguments are dropped, because
// what matters is that `nm workplan await-resolution` is a real command, and the
// arguments are the skill's business.
func findCommands(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "$ "))
		// Inline code spans: `nm workplan resolve <name>`.
		for _, span := range codeSpans(line) {
			if cmd := commandPath(span); cmd != "" {
				out = append(out, cmd)
			}
		}
		if cmd := commandPath(line); cmd != "" {
			out = append(out, cmd)
		}
	}
	return out
}

func codeSpans(line string) []string {
	var spans []string
	parts := strings.Split(line, "`")
	// Odd indices are inside a span, given an even number of delimiters.
	for i := 1; i < len(parts); i += 2 {
		spans = append(spans, parts[i])
	}
	return spans
}

// commandPath returns the subcommand path of an `nm` invocation, or "".
func commandPath(s string) string {
	fields := strings.Fields(s)
	if len(fields) < 2 || fields[0] != "nm" {
		return ""
	}
	path := []string{"nm"}
	for _, f := range fields[1:] {
		// A subcommand is a bare word. Anything else — a flag, a placeholder, a
		// path — ends the path.
		if f == "" || strings.HasPrefix(f, "-") || strings.HasPrefix(f, "<") ||
			strings.HasPrefix(f, "@") || strings.ContainsAny(f, "/\\.$\"'") {
			break
		}
		path = append(path, f)
	}
	if len(path) < 2 {
		return ""
	}
	return strings.Join(path, " ")
}
