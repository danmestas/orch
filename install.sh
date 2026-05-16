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

# Per-harness hook scripts (codex/gemini/pi) were retired in orch#94 — the
# Synadia Agent Protocol path via orch-agent-shim is now the only path.

# fleet doctrine — copy (not symlink — agents read it once at spawn so we
# want a stable file, not one that disappears if the repo is moved).
mkdir -p "$HOME/.cache"
cp "$ROOT/fleet-prompt.md" "$HOME/.cache/orch-fleet-prompt.md"
echo "fleet doctrine cached at ~/.cache/orch-fleet-prompt.md"

# orch-agent-shim — Go binary, Synadia Agent Protocol bridge. Optional;
# only built if `go` is on PATH. The shim is invoked from orch-spawn
# under --with-shim, which is opt-in in v1. Builds straight into
# ~/.local/bin so it's discoverable without an extra symlink dance.
if command -v go >/dev/null 2>&1; then
    SHIM_DST="$HOME/.local/bin/orch-agent-shim"
    mkdir -p "$(dirname "$SHIM_DST")"
    ( cd "$ROOT" && go build -o "$SHIM_DST" ./cmd/orch-agent-shim ) \
        && echo "built $SHIM_DST" \
        || echo "warn: orch-agent-shim build failed — --with-shim will be a no-op until fixed (the go.mod 'go' directive is the authoritative floor)"
else
    # The Go floor is whatever go.mod's `go` directive settles on against
    # the dependency graph (currently 1.25, driven by nats-server v2.14).
    # Bumps automatically as `go mod tidy` runs after upstream upgrades —
    # check `head -3 go.mod` for the current authoritative floor.
    echo "skip: go not on PATH — orch-agent-shim not built (see go.mod's 'go' directive for the floor; install Go to enable --with-shim)"
fi

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
     (As of orch#94 the legacy marker + NATS-publish hooks are retired;
     the only entry in the snippet is orch-goal-session-context.sh.)
  2. Install runtime dependencies:
       $DEPS_HINT
     (tmux is the runtime substrate; jq is used by hook scripts and
     registry tooling.)

Verify by spawning a tmux pane like:

  tmux split-window -d -h \\
    'export ORCH_PANE_ID=\$TMUX_PANE; claude --dangerously-skip-permissions; \\
     echo "[exit]"; read; exec \$SHELL -l'

Then from the parent shell:

  orch-tell <new_pane_id> "say hello"
  orch-ask  <new_pane_id> "summarize this dir"
EOF
