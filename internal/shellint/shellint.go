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
const posixScript = `# nm shell integration.
# nm cannot change this shell's directory itself, so it writes the directory it
# selected to $NM_CD_FILE and this function performs the cd.
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
    [ -d "$nm_target" ] && cd "$nm_target" || true
  fi
  rm -f "$nm_cd_file"
  return $nm_status
}
`

// nuScript needs def --env: without it the cd would be scoped to the function
// and vanish when it returns.
const nuScript = `# nm shell integration.
# def --env lets the cd escape the function and affect the caller.
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
    if ($nm_target | is-not-empty) and ($nm_target | path exists) {
        cd $nm_target
    }
}
`
