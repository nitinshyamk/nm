package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/nitinshyamk/nm/internal/config"
	"github.com/nitinshyamk/nm/internal/shellint"
)

func TestWriteManagedBlockIsIdempotent(t *testing.T) {
	rc := filepath.Join(t.TempDir(), ".bashrc")
	original := "export PATH=\"$HOME/bin:$PATH\"\nalias ll='ls -la'\n"
	if err := os.WriteFile(rc, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	replaced, err := writeManagedBlock(rc, "bash")
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	if replaced {
		t.Error("the first write reported replacing a block that did not exist")
	}

	after := readFile(t, rc)
	if !strings.HasPrefix(after, original) {
		t.Error("writing the block disturbed the existing rc content")
	}

	for i := range 3 {
		replaced, err = writeManagedBlock(rc, "bash")
		if err != nil {
			t.Fatalf("rewrite %d: %v", i, err)
		}
		if !replaced {
			t.Errorf("rewrite %d did not report replacing the existing block", i)
		}
	}

	final := readFile(t, rc)
	if n := strings.Count(final, beginMarker); n != 1 {
		t.Errorf("rc file holds %d nm blocks after repeated setup, want 1:\n%s", n, final)
	}
	if !strings.HasPrefix(final, original) {
		t.Errorf("repeated setup disturbed the user's own lines:\n%s", final)
	}
	if strings.Count(final, "\n\n\n") > 0 {
		t.Errorf("repeated setup accumulated blank lines:\n%q", final)
	}
}

func TestWriteManagedBlockCreatesMissingFile(t *testing.T) {
	rc := filepath.Join(t.TempDir(), "nested", ".zshrc")
	if _, err := writeManagedBlock(rc, "zsh"); err != nil {
		t.Fatalf("writeManagedBlock: %v", err)
	}
	if got := readFile(t, rc); !strings.Contains(got, "nm shell init zsh") {
		t.Errorf("rc file does not load the zsh integration:\n%s", got)
	}
}

func TestStripManagedBlockLeavesForeignContent(t *testing.T) {
	content := "before\n" + beginMarker + "\nold line\n" + endMarker + "\nafter\n"
	got, found := stripManagedBlock(content)
	if !found {
		t.Fatal("stripManagedBlock did not find the block")
	}
	if strings.Contains(got, "old line") {
		t.Error("the old block survived")
	}
	if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
		t.Errorf("surrounding content was lost: %q", got)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSettingValueExpandsPaths(t *testing.T) {
	cfg := config.Defaults()
	cfg.InstallDir = "/opt/bin"
	cfg.ProjectsRoot = "/src"

	cases := map[string]string{
		"install_dir":    "/opt/bin",
		"projects_root":  "/src",
		"hash_length":    "6",
		"editor_command": "code",
	}
	for key, want := range cases {
		got, err := settingValue(cfg, key)
		if err != nil {
			t.Errorf("settingValue(%q): %v", key, err)
			continue
		}
		if got != want {
			t.Errorf("settingValue(%q) = %q, want %q", key, got, want)
		}
	}

	if _, err := settingValue(cfg, "not_a_setting"); err == nil {
		t.Error("settingValue accepted an unknown key")
	} else if !strings.Contains(err.Error(), "install_dir") {
		t.Errorf("the error should list the known settings, got: %v", err)
	}
}

func TestSettingValueUsesHomeExpansion(t *testing.T) {
	cfg := config.Defaults()
	got, err := settingValue(cfg, "worktrees_root")
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(got, "~") {
		t.Errorf("settingValue returned an unexpanded path: %q", got)
	}
}

// nushell does not follow XDG on Windows: it reads %APPDATA%\nushell, and nm
// used to write ~/.config/nushell there, producing a file nushell never loads.
// That made `nm shell setup` report success while doing nothing at all.
func TestNuConfigPathFollowsNushellNotXDG(t *testing.T) {
	home := filepath.Join("C:", "Users", "someone")
	if runtime.GOOS != "windows" {
		home = "/home/someone"
	}

	got, err := nuConfigPath(home)
	if err != nil {
		t.Fatalf("nuConfigPath: %v", err)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(got, filepath.Join("AppData", "Roaming", "nushell")) {
			t.Errorf("nuConfigPath = %q, want it under %%APPDATA%%\nushell", got)
		}
		if strings.Contains(got, ".config") {
			t.Errorf("nuConfigPath = %q, which is the XDG path nushell ignores on Windows", got)
		}
		return
	}
	if !strings.Contains(got, filepath.Join(".config", "nushell")) {
		t.Errorf("nuConfigPath = %q, want it under ~/.config/nushell", got)
	}
}

// XDG_CONFIG_HOME is honored away from Windows, where nushell does follow it.
func TestNuConfigPathHonorsXDGAwayFromWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("nushell ignores XDG_CONFIG_HOME on Windows")
	}
	t.Setenv("XDG_CONFIG_HOME", "/custom/cfg")

	got, err := nuConfigPath("/home/someone")
	if err != nil {
		t.Fatalf("nuConfigPath: %v", err)
	}
	if got != filepath.Join("/custom/cfg", "nushell", "config.nu") {
		t.Errorf("nuConfigPath = %q, want it under XDG_CONFIG_HOME", got)
	}
}

// TestNushellWrapperClassifiesEveryTopLevelCommand keeps the nushell wrapper's
// command list honest.
//
// nushell is the one shell whose wrapper has to know which commands can move the
// shell: a def's output is its last expression, so putting the cd bookkeeping
// after every call swallowed nm's stdout and broke `nm shell init nu | save ...`.
// The wrapper therefore routes only the cd-capable commands through that path.
//
// The risk that buys is silent: add a command that calls enterDir, forget the
// list, and its jumps quietly stop working. This fails instead, so a new command
// forces the decision.
func TestNushellWrapperClassifiesEveryTopLevelCommand(t *testing.T) {
	// Commands that reach enterDir, and so must take the wrapper's cd path.
	canCD := map[string]bool{"task": true, "worktree": true}
	// Commands that only write to stdout and must stay pipeline-transparent.
	//
	// workplan is here rather than in canCD deliberately: a workplan directory
	// is not somewhere you work — the work happens in the task directories it
	// starts — so nothing in it calls enterDir. Staying output-only is also what
	// lets `nm workplan execute --json` be read by a poller.
	outputOnly := map[string]bool{
		"completion": true, "config": true, "help": true, "self": true, "shell": true,
		"workplan": true,
	}

	routed := nuCDCommands(t)

	for _, cmd := range newRootCmd().Commands() {
		name := cmd.Name()
		switch {
		case canCD[name]:
			if !routed[name] {
				t.Errorf("`nm %s` can move the shell but the nushell wrapper does not route it "+
					"through the cd path, so its jumps silently do nothing", name)
			}
		case outputOnly[name]:
			if routed[name] {
				t.Errorf("`nm %s` only writes to stdout, but the nushell wrapper routes it through "+
					"the cd path, which swallows its output in a pipeline", name)
			}
		default:
			t.Errorf("`nm %s` is new and unclassified: decide whether it can ask the shell to move "+
				"(grep for enterDir), then add it to canCD or outputOnly here and to the "+
				"command list in nuScript", name)
		}
	}

	// And nothing unknown crept into the script's list.
	for name := range routed {
		if !canCD[name] {
			t.Errorf("the nushell wrapper routes %q through the cd path, but it is not a "+
				"cd-capable command", name)
		}
	}
}

// nuCDCommands reads the cd-capable command list out of the generated nushell
// script, so the guard above compares against what the wrapper actually does
// rather than a copy of it that could drift.
func nuCDCommands(t *testing.T) map[string]bool {
	t.Helper()
	script, err := shellint.InitScript("nu")
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`not-in \[([^\]]*)\]`).FindStringSubmatch(script)
	if match == nil {
		t.Fatal("the nushell wrapper no longer has a `not-in [...]` command list; update this guard")
	}
	out := map[string]bool{}
	for _, quoted := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(match[1], -1) {
		out[quoted[1]] = true
	}
	if len(out) == 0 {
		t.Fatal("the nushell wrapper's cd command list is empty, so no jump would ever happen")
	}
	return out
}
