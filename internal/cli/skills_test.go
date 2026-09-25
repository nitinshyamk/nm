package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/skills"
	"github.com/spf13/cobra"
)

// TestEverySkillCommandExists is what stops a skill from drifting away from the
// CLI it drives.
//
// A skill is read by an agent working unattended. A command that does not exist
// fails in the middle of the work, hours after anyone could have noticed and with
// a half-finished task to clean up — where a skill that fails to load fails
// immediately. The skills are embedded in this binary precisely so this check can
// exist, and it is the reason they are not shipped as loose files.
func TestEverySkillCommandExists(t *testing.T) {
	invoked, err := skills.Commands()
	if err != nil {
		t.Fatalf("reading the skills: %v", err)
	}
	if len(invoked) == 0 {
		t.Fatal("no nm commands were found in the skills, so this test is checking nothing")
	}

	root := newRootCmd()
	for _, invocation := range invoked {
		path := strings.Fields(invocation)[1:] // drop "nm"
		found, rest, err := root.Find(path)
		if err != nil {
			t.Errorf("a skill invokes `%s`, which this binary does not have: %v", invocation, err)
			continue
		}

		// cobra.Find does NOT error on an unknown subcommand. It returns the
		// closest parent it could resolve and hands back the words it could not
		// consume, so `nm workplan wait-for-answer` resolves to `workplan` with
		// rest=["wait-for-answer"] and a nil error. Checking only the error is
		// what made an earlier version of this test pass against a command that
		// did not exist.
		//
		// Anything left in rest is either a real argument or a subcommand that
		// does not exist, and the two are told apart by asking whether the
		// command it resolved to has subcommands at all.
		if len(rest) > 0 && found.HasSubCommands() {
			for _, word := range rest {
				if sub := subCommandNamed(found, word); sub == nil {
					t.Errorf("a skill invokes `%s`, but %q is not a subcommand of `nm %s`",
						invocation, word, strings.Join(path[:len(path)-len(rest)], " "))
				}
			}
		}
	}
}

// subCommandNamed finds a subcommand by name or alias.
func subCommandNamed(parent *cobra.Command, name string) *cobra.Command {
	for _, sub := range parent.Commands() {
		if sub.Name() == name {
			return sub
		}
		for _, alias := range sub.Aliases {
			if alias == name {
				return sub
			}
		}
	}
	return nil
}

// install-skills must succeed while also sweeping a retired skill away.
//
// This covers the path `mise run install` takes on an existing machine: the
// bundled skills land and the withdrawn one goes, in one run that exits 0. The
// `kept` case — a retired skill that cannot be removed, which is the one carrying a
// Reason and so the one the command's blocked rule has to classify — needs a
// symlink and is tested in internal/skills, where it can be skipped per platform.
func TestInstallSkillsSweepsARetiredSkillAndStillSucceeds(t *testing.T) {
	if len(skills.Retired) == 0 {
		t.Skip("nothing is retired, so there is nothing to sweep")
	}
	dir := t.TempDir()
	stale := filepath.Join(dir, skills.Retired[0], skills.File)
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("withdrawn instructions\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	root := newRootCmd()
	var out strings.Builder
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"self", "install-skills", "--dir", dir})
	if err := root.Execute(); err != nil {
		t.Fatalf("install-skills failed on a sweep: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("%s survived: %v", skills.Retired[0], err)
	}
	if !strings.Contains(out.String(), "installed into") {
		t.Errorf("the run did not report success:\n%s", out.String())
	}
	all, err := skills.All()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range all {
		if _, err := os.Stat(filepath.Join(dir, s.Name, skills.File)); err != nil {
			t.Errorf("%s was not installed: %v", s.Name, err)
		}
	}
}

// The flags the skills name have to exist too: a skill telling an agent to pass
// --timeout to a command with no such flag fails the same way.
func TestSkillFlagsExist(t *testing.T) {
	cases := []struct {
		path []string
		flag string
	}{
		{[]string{"workplan", "define"}, "name"},
		{[]string{"workplan", "define"}, "artifacts"},
		{[]string{"workplan", "add"}, "content"},
		{[]string{"workplan", "execute"}, "merge"},
		{[]string{"workplan", "execute"}, "json"},
		{[]string{"workplan", "await-resolution"}, "timeout"},
	}

	root := newRootCmd()
	for _, tc := range cases {
		cmd, _, err := root.Find(tc.path)
		if err != nil {
			t.Errorf("nm %s: %v", strings.Join(tc.path, " "), err)
			continue
		}
		if cmd.Flags().Lookup(tc.flag) == nil {
			t.Errorf("the skills pass --%s to `nm %s`, which has no such flag",
				tc.flag, strings.Join(tc.path, " "))
		}
	}
}
