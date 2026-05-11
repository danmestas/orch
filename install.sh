#!/usr/bin/env bash
# install.sh — symlink orch files into the locations Claude Code,
# tmux, and the user's shell expect.
#
# Idempotent. Won't clobber pre-existing real files / directories — those
# are skipped with a message. Re-running just refreshes existing symlinks.
#
# Final manual step (printed at the end): merge settings-snippet.json into
# ~/.claude/settings.json under the "hooks" key.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)

link_one() {
    local src=$1 dst=$2
    if [ -L "$dst" ]; then
        rm "$dst"
    elif [ -e "$dst" ]; then
        echo "SKIP $dst — real file/dir already there. rm it first if you want to symlink."
        return 0
    fi
    mkdir -p "$(dirname "$dst")"
    ln -s "$src" "$dst"
    echo "linked $dst → $src"
}

# bin
for f in "$ROOT"/bin/*; do
    [ -f "$f" ] || continue
    link_one "$f" "$HOME/.local/bin/$(basename "$f")"
    chmod +x "$f"
done

# hooks
for f in "$ROOT"/hooks/*; do
    [ -f "$f" ] || continue
    link_one "$f" "$HOME/.claude/hooks/$(basename "$f")"
    chmod +x "$f"
done

# skills (one symlink per skill directory)
for d in "$ROOT"/skills/*/; do
    [ -d "$d" ] || continue
    name=$(basename "$d")
    link_one "${d%/}" "$HOME/.claude/skills/$name"
done

# pi extension (auto-discovered from ~/.pi/agent/extensions/)
if [ -d "$ROOT/pi-extensions" ]; then
    for f in "$ROOT"/pi-extensions/*; do
        [ -f "$f" ] || continue
        link_one "$f" "$HOME/.pi/agent/extensions/$(basename "$f")"
    done
fi

# fleet doctrine — copy (not symlink — agents read it once at spawn so we
# want a stable file, not one that disappears if the repo is moved).
mkdir -p "$HOME/.cache"
cp "$ROOT/fleet-prompt.md" "$HOME/.cache/orch-fleet-prompt.md"
echo "fleet doctrine cached at ~/.cache/orch-fleet-prompt.md"

# Inject fleet doctrine idempotently into agents that don't have a CLI flag for
# system-prompt-append. Uses a marker block so re-running install.sh refreshes
# the content in place without duplicating or clobbering surrounding user content.
inject_doctrine() {
    local target=$1
    local source=$HOME/.cache/orch-fleet-prompt.md
    local begin="<!-- BEGIN orch-fleet-doctrine -->"
    local end="<!-- END orch-fleet-doctrine -->"

    mkdir -p "$(dirname "$target")"

    if [ ! -f "$target" ]; then
        { echo "$begin"; cat "$source"; echo "$end"; } > "$target"
        echo "fleet-doctrine: created $target"
    elif grep -qF "$begin" "$target"; then
        awk -v begin="$begin" -v end="$end" -v src="$source" '
            $0 == begin { print; while ((getline line < src) > 0) print line; print end; in_block=1; next }
            $0 == end { in_block=0; next }
            !in_block { print }
        ' "$target" > "$target.tmp" && mv "$target.tmp" "$target"
        echo "fleet-doctrine: refreshed block in $target"
    else
        { echo; echo "$begin"; cat "$source"; echo "$end"; } >> "$target"
        echo "fleet-doctrine: appended block to $target"
    fi
}

# codex global instructions (no --append-system-prompt CLI flag)
inject_doctrine "$HOME/.codex/AGENTS.md"
# gemini global instructions (no CLI flag either)
inject_doctrine "$HOME/.gemini/GEMINI.md"

# OS-detect for the runtime-deps install hint at the end.
case "$(uname -s)" in
    Darwin)
        DEPS_HINT='brew install tmux fswatch jq' ;;
    Linux)
        if command -v apt >/dev/null 2>&1; then
            DEPS_HINT='sudo apt install tmux fswatch jq'
        elif command -v dnf >/dev/null 2>&1; then
            DEPS_HINT='sudo dnf install tmux fswatch jq'
        elif command -v pacman >/dev/null 2>&1; then
            DEPS_HINT='sudo pacman -S tmux fswatch jq'
        else
            DEPS_HINT='install tmux fswatch jq via your package manager'
        fi ;;
    *)
        DEPS_HINT='install tmux fswatch jq via your platform package manager' ;;
esac

cat <<EOF

Done.

Manual step remaining:
  1. Merge settings-snippet.json into ~/.claude/settings.json under the
     existing "hooks" object. Preserve any hooks that are already there.
  2. Install runtime dependencies:
       $DEPS_HINT
     (tmux is the runtime substrate; fswatch is used by orch-listen;
     jq by the hook scripts and registry tooling.)

Verify by spawning a tmux pane like:

  tmux split-window -d -h \\
    'export ORCH_PANE_ID=\$TMUX_PANE; claude --dangerously-skip-permissions; \\
     echo "[exit]"; read; exec \$SHELL -l'

Then from the parent shell:

  orch-tell <new_pane_id> "say hello"
  orch-ask  <new_pane_id> "summarize this dir"
EOF
