// Package shellint implements the contract between the nm binary and the
// shell function that wraps it.
//
// A process cannot change its parent shell's working directory, so nm writes
// the directory it wants to move to into the file named by NM_CD_FILE and the
// wrapper function performs the cd after nm exits.
package shellint

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// EnvCDFile names the file nm writes its chosen directory to.
const EnvCDFile = "NM_CD_FILE"

// Enabled reports whether the shell wrapper is installed in this shell.
func Enabled() bool { return os.Getenv(EnvCDFile) != "" }

// RequestCD asks the wrapper to move the shell to dir. It reports whether the
// request could be delivered, which is false when the wrapper is not loaded.
func RequestCD(dir string) (bool, error) {
	target := os.Getenv(EnvCDFile)
	if target == "" {
		return false, nil
	}
	if err := os.WriteFile(target, []byte(dir+"\n"), 0o600); err != nil {
		return false, fmt.Errorf("writing the shell cd request: %w", err)
	}
	return true, nil
}

// Hint is printed when nm has a directory for the user but no wrapper to
// deliver it to.
func Hint(shell string) string {
	if shell == "" {
		shell = "bash"
	}
	return fmt.Sprintf("shell integration is not loaded, so your shell stays put.\n"+
		"Add this to your rc file (or run: nm shell setup):\n\n  %s", evalLine(shell))
}

func evalLine(shell string) string {
	if shell == "nu" {
		return "nm shell init nu | save --force ($nu.default-config-dir | path join nm.nu)\n  source ($nu.default-config-dir | path join nm.nu)"
	}
	return fmt.Sprintf("eval \"$(nm shell init %s)\"", shell)
}

var scripts = map[string]string{
	"bash": posixScript,
	"zsh":  posixScript,
	"nu":   nuScript,
}

// Shells lists the shells nm can generate an integration script for.
func Shells() []string {
	out := make([]string, 0, len(scripts))
	for name := range scripts {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// InitScript returns the wrapper function for the named shell.
func InitScript(shell string) (string, error) {
	script, ok := scripts[strings.ToLower(strings.TrimSpace(shell))]
	if !ok {
		return "", fmt.Errorf("unsupported shell %q (supported: %s)", shell, strings.Join(Shells(), ", "))
	}
	return script, nil
}

// posixScript works in both bash and zsh.
//
// It moves with pushd rather than cd, so every jump nm makes is pushed onto
// the directory stack and "popd" takes you back where you came from. A jump
// to the directory you are already in is skipped, so the stack does not fill
// up with the same entry.
const posixScript = `# nm shell integration.
# nm cannot change this shell's directory itself, so it writes the directory it
# selected to $NM_CD_FILE and this function performs the move.
nm() {
  # Tab-completion evaluates "nm __complete ..." in this very shell. Those
  # calls must never take the cd path, or pressing tab could move you.
  case "$1" in
    __complete|__completeNoDesc|completion)
      command nm "$@"
      return $?
      ;;
  esac

  local nm_cd_file
  nm_cd_file="$(mktemp "${TMPDIR:-/tmp}/nm-cd.XXXXXX")" || {
    command nm "$@"
    return $?
  }
  NM_CD_FILE="$nm_cd_file" command nm "$@"
  local nm_status=$?
  if [ -s "$nm_cd_file" ]; then
    local nm_target
    nm_target="$(cat "$nm_cd_file")"
    # Ask this shell what it calls the target before comparing it to $PWD. nm
    # writes an OS path, which is not always how the shell spells the same
    # directory: under Git Bash nm writes C:\Users\... while $PWD holds
    # /c/Users/..., and a plain string compare would never match, so every jump
    # would push a duplicate. The same mismatch happens on Linux whenever the
    # shell reached a directory through a symlink.
    local nm_resolved
    nm_resolved="$(cd "$nm_target" 2>/dev/null && pwd)" || nm_resolved=""
    # pushd, not cd: the directory you were in stays on the stack, so popd
    # brings you back. builtin, so a pushd alias or function cannot redirect
    # it. Already being there is not worth a stack entry.
    if [ -n "$nm_resolved" ] && [ "$nm_resolved" != "$PWD" ]; then
      builtin pushd "$nm_resolved" >/dev/null || builtin cd "$nm_resolved" || true
    fi
  fi
  rm -f "$nm_cd_file"
  return $nm_status
}
`

// nuScript needs def --env: without it the move would be scoped to the
// function and vanish when it returns.
//
// nushell keeps its directory stack in the std/dirs module rather than in a
// pushd builtin, so the script uses "dirs add" when that module is loaded and
// falls back to cd when it is not.
const nuScript = `# nm shell integration.
# def --env lets the directory change escape the function and affect the caller.
def --env nm [...args] {
    # Completion requests must not take the cd path (see the posix script).
    if ($args | length) > 0 and ($args | first) in ["__complete" "__completeNoDesc" "completion"] {
        ^nm ...$args
        return
    }
    let nm_cd_file = (mktemp --tmpdir nm-cd.XXXXXX)
    try {
        with-env { NM_CD_FILE: $nm_cd_file } { ^nm ...$args }
    }
    let nm_target = (try { open --raw $nm_cd_file | str trim } catch { "" })
    rm --force $nm_cd_file
    if ($nm_target | is-not-empty) and ($nm_target | path exists) and ($nm_target != $env.PWD) {
        # "dirs add" is nushell's pushd; it only exists once std/dirs is in
        # scope, so fall back to cd rather than failing the jump.
        if (which dirs | is-not-empty) {
            dirs add $nm_target
        } else {
            cd $nm_target
        }
    }
}
`
