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
	switch shell {
	case "nu":
		return "nm shell init nu | save --force ($nu.default-config-dir | path join nm.nu)\n  source ($nu.default-config-dir | path join nm.nu)"
	case "powershell":
		return "nm shell init powershell | Out-String | Invoke-Expression"
	}
	return fmt.Sprintf("eval \"$(nm shell init %s)\"", shell)
}

var scripts = map[string]string{
	"bash":       bashZshScript,
	"zsh":        bashZshScript,
	"nu":         nuScript,
	"powershell": psScript,
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

// bashZshScript works in bash and zsh, and in neither case is it POSIX: pushd,
// popd, and local are all extensions. dash has no pushd at all, so the name
// says bash and zsh rather than posix to stop the script being offered to a
// shell that cannot run it.
//
// It moves with pushd rather than cd, so every jump nm makes is pushed onto
// the directory stack and "popd" takes you back where you came from. A jump
// to the directory you are already in is skipped, so the stack does not fill
// up with the same entry.
const bashZshScript = `# nm shell integration.
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
// pushd builtin, and that module is not in scope until it is used. The script
// used to test "which dirs" and fall back to a plain cd, which on a default
// nushell meant always falling back: the jump worked but nothing was pushed, so
// "dirs drop" -- nushell's popd -- could not take you back.
//
// cobra generates no nushell completion, so the script parses the same
// __complete protocol cobra serves to bash and zsh.
const nuScript = `# nm shell integration.

# std/dirs is nushell's directory stack. Without this use it is not in scope and
# every jump would be an unrecorded cd, leaving "dirs drop" nothing to undo.
use std/dirs

# nm answers a __complete request the way cobra does for every other shell:
# "value<TAB>description" lines, then a ":<directive>" line that is not a
# candidate. nushell has no generator for this, so the parsing lives here.
def "nu-complete nm" [context: string] {
    let parts = ($context | split row ' ' | skip 1)
    let answer = (do --ignore-errors { ^nm __complete ...$parts } | complete)
    if $answer.exit_code != 0 {
        return []
    }
    $answer.stdout
        | lines
        | where {|line| ($line | str trim) != "" and not ($line | str starts-with ':') }
        | each {|line|
            let cut = ($line | split row "\t")
            {value: ($cut | first), description: ($cut | skip 1 | str join ' ')}
        }
}

# def --env lets the directory change escape the function and affect the caller.
#
# --wrapped is not optional: without it nushell validates what you typed against
# this signature, and since the signature declares no flags, "nm task new repo -n
# name -p" fails to parse with "The nm command doesn't have flag -n" before the
# body ever runs. It is a parse error, so it cannot even be caught. --wrapped
# tells nushell to stop interpreting flags and collect them into the rest
# parameter, which also lets --help and -h reach nm instead of printing this
# def's own generated help.
def --env --wrapped nm [...args: string@"nu-complete nm"] {
    # Only task and worktree can ask nm to move the shell. Everything else --
    # config, shell, self, completion, help, and the __complete requests a tab
    # press makes -- only writes to stdout, and has to reach the pipeline
    # untouched.
    #
    # A nushell def's output is its last expression, so anything placed after the
    # call swallows it: with the cd bookkeeping below in the way,
    # "nm shell init nu | save --force ..." piped nothing and failed with "can't
    # convert nothing to string". bash and PowerShell stream stdout through a
    # function inherently and need no such split; this is nushell-specific.
    #
    # There is deliberately no "return" anywhere in here. An explicit bare return
    # makes one path yield nothing, which is the same error by another name.
    #
    # The trade-off: piping a task or worktree command is still not transparent,
    # because the cd bookkeeping has to run after nm exits and nothing can come
    # after it. Capturing nm's output instead would fix the pipe and break the
    # TUI those commands open, which is the worse bargain.
    if ($args | length) == 0 or ($args | first) not-in ["task" "worktree"] {
        ^nm ...$args
    } else {
        let nm_cd_file = (mktemp --tmpdir nm-cd.XXXXXX)
        try {
            with-env { NM_CD_FILE: $nm_cd_file } { ^nm ...$args }
        }
        let nm_target = (try { open --raw $nm_cd_file | str trim } catch { "" })
        rm --force $nm_cd_file
        # path expand on both sides, for the reason the bash script canonicalizes:
        # nm writes an OS path, which is not always how the shell spells the same
        # directory, and a jump to where you already are is not worth a stack
        # entry.
        if ($nm_target | is-not-empty) and ($nm_target | path exists) and (($nm_target | path expand) != ($env.PWD | path expand)) {
            dirs add $nm_target
        }
    }
}
`

// psScript is the Windows PowerShell wrapper. Unlike bash and nushell it needs
// no completion of its own: cobra generates a PowerShell completer, and it fires
// even though nm is a function here rather than an external command.
//
// Push-Location is PowerShell's pushd, so the popd contract holds as it does in
// bash: Pop-Location takes you back where the jump started.
const psScript = `# nm shell integration.
# nm cannot change this shell's directory itself, so it writes the directory it
# selected to $env:NM_CD_FILE and this function performs the move.
function nm {
    # -CommandType Application is what keeps this from recursing into itself,
    # the way "command nm" does in bash: it matches the executable on PATH and
    # never this function. The bare name rather than nm.exe so a PATHEXT
    # alternative such as nm.cmd is found too.
    $nmExe = Get-Command nm -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
    if (-not $nmExe) {
        Write-Error 'nm is not on PATH'
        return
    }

    # Tab-completion evaluates "nm __complete ..." in this very shell. Those
    # calls must never take the cd path, or pressing tab could move you.
    if ($args.Count -gt 0 -and @('__complete','__completeNoDesc','completion') -contains $args[0]) {
        & $nmExe.Source @args
        return
    }

    $nmCdFile = [System.IO.Path]::GetTempFileName()
    $nmStatus = 0
    try {
        $env:NM_CD_FILE = $nmCdFile
        & $nmExe.Source @args
        $nmStatus = $LASTEXITCODE
    } finally {
        # The variable must not outlive the call, or a later nm would inherit a
        # stale cd file.
        Remove-Item Env:NM_CD_FILE -ErrorAction SilentlyContinue
    }

    $nmTarget = ''
    if (Test-Path -LiteralPath $nmCdFile) {
        $nmTarget = Get-Content -Raw -LiteralPath $nmCdFile
        if ($null -ne $nmTarget) { $nmTarget = $nmTarget.Trim() }
        Remove-Item -LiteralPath $nmCdFile -Force -ErrorAction SilentlyContinue
    }
    if ($nmTarget -and (Test-Path -LiteralPath $nmTarget -PathType Container)) {
        # Get-Item, not Resolve-Path or Convert-Path: those two preserve an 8.3
        # short name (C:\Users\NITINS~1\...) while Get-Location reports the long
        # one, so comparing them as strings never matched and every jump pushed a
        # duplicate. Already being there is not worth a stack entry.
        $nmResolved = (Get-Item -LiteralPath $nmTarget).FullName
        $nmHere = (Get-Item -LiteralPath (Get-Location).Path).FullName
        if ($nmResolved -ne $nmHere) {
            Push-Location -LiteralPath $nmResolved
        }
    }
    # Hand nm's exit status back, which the cd bookkeeping above would otherwise
    # have overwritten.
    $global:LASTEXITCODE = $nmStatus
}
`
