#!/usr/bin/env bash
# Append the nm shell integration to the current shell's rc file.
# Idempotent: re-running updates the managed block instead of duplicating it.
set -euo pipefail

BEGIN_MARKER='# >>> nm shell integration >>>'
END_MARKER='# <<< nm shell integration <<<'

shell_name="${1:-}"
if [ -z "$shell_name" ]; then
  shell_name="$(basename "${SHELL:-bash}")"
fi

case "$shell_name" in
  bash) rc="$HOME/.bashrc" ;;
  zsh)  rc="${ZDOTDIR:-$HOME}/.zshrc" ;;
  nu)   rc="${XDG_CONFIG_HOME:-$HOME/.config}/nushell/config.nu" ;;
  *)
    echo "setup-shell: unsupported shell '$shell_name' (want bash, zsh, or nu)" >&2
    exit 1
    ;;
esac

line="eval \"\$(nm shell init $shell_name)\""
if [ "$shell_name" = "nu" ]; then
  line="nm shell init nu | save --force \${\$nu.default-config-dir}/nm.nu
source \${\$nu.default-config-dir}/nm.nu"
fi

mkdir -p "$(dirname "$rc")"
touch "$rc"

if grep -qF "$BEGIN_MARKER" "$rc"; then
  tmp="$(mktemp)"
  awk -v b="$BEGIN_MARKER" -v e="$END_MARKER" '
    $0 == b { skipping = 1 }
    !skipping { print }
    $0 == e { skipping = 0 }
  ' "$rc" >"$tmp"
  mv "$tmp" "$rc"
  echo "setup-shell: refreshed the existing nm block in $rc"
else
  echo "setup-shell: added the nm block to $rc"
fi

{
  echo ""
  echo "$BEGIN_MARKER"
  echo "$line"
  echo "$END_MARKER"
} >>"$rc"

echo "setup-shell: open a new shell, or run: source $rc"
