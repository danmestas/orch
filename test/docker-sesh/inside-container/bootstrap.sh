#!/usr/bin/env bash
# bootstrap.sh — ENTRYPOINT for the sesh test bench.
#
# Per-test isolation: each test case runs `sesh up` / `sesh down` itself
# in its own cwd, so we don't need a session-running at bootstrap. We
# just set up paths, clone the wardrobe so `suit` tests pass, and hand
# off to tests.sh inside a tmux session (sesh + tmux interactions are
# tested too).
set -uo pipefail

log() { printf '[bootstrap %s] %s\n' "$(date -u +%T)" "$*"; }

ORCH_PKG_DIR=/usr/lib/node_modules/@agent-ops/orch

log "sesh version: $(sesh --help 2>&1 | head -1 || true)"

# Wire claude-side hooks so the orch bridge tests work (mirrors test/docker).
mkdir -p "$HOME/.claude/hooks" "$HOME/.cache"
for f in "$ORCH_PKG_DIR"/hooks/*; do
    [ -f "$f" ] || continue
    ln -sf "$f" "$HOME/.claude/hooks/$(basename "$f")"
done

sed "s|\$HOME|$HOME|g" "$ORCH_PKG_DIR/settings-snippet.json" \
    | jq 'del(._INSTRUCTIONS)' \
    > "$HOME/.claude/settings.json"

# Clone the public wardrobe so suit-touching patterns can be tested.
SUIT_CONTENT="$HOME/.local/share/suit/content"
mkdir -p "$(dirname "$SUIT_CONTENT")"
rm -rf "$SUIT_CONTENT"
git clone --depth 1 https://github.com/danmestas/wardrobe.git "$SUIT_CONTENT" \
    >/tmp/wardrobe-clone.log 2>&1 || log "wardrobe clone failed (suit tests will reflect)"

# Tests run inside tmux because some of them spawn orch workers, which
# require a tmux server.
rm -f /tmp/test-rc /tmp/test-out.log
tmux new-session -d -s sesh-tests \
    'bash /usr/local/bin/tests.sh > /tmp/test-out.log 2>&1; echo $? > /tmp/test-rc'

deadline=$(( $(date +%s) + 300 ))
while [ ! -f /tmp/test-rc ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { log "TIMEOUT at 300s"; break; }
    sleep 1
done

log "tests done — output follows"
echo "----------------------------------------------------------------"
cat /tmp/test-out.log
echo "----------------------------------------------------------------"

RC=$(cat /tmp/test-rc 2>/dev/null || echo 99)
log "exit code = $RC"
exit "$RC"
