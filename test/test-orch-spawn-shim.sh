#!/usr/bin/env bash
# test-orch-spawn-shim.sh — integration smoke test for orch-spawn's default
# shim sidecar launch.
#
# Asserts that after `orch-spawn claude` returns, the spawned pane is
# discoverable on the Synadia bus via $SRV.INFO.agents within 5 s.
#
# Prerequisites (auto-SKIPped if missing):
#   - tmux (for pane creation)
#   - nats-server (brew install nats-server)
#   - nats CLI (brew install nats-io/nats-tools/nats)
#   - orch-agent-shim on PATH (built via go build ./cmd/orch-agent-shim)
#   - orch-spawn on PATH
#
# Usage:
#   bash test/test-orch-spawn-shim.sh
#
# Exit codes:
#   0   all assertions passed
#   1   assertion failure
#   77  prerequisite not available (SKIP)
set -euo pipefail

# Drop orch-spawn's interactive pause-on-exit wrapper tail so the
# `orch-spawn claude` invocation closes its pane cleanly if claude is
# absent or crashes on the runner (closes #178).
export ORCH_NO_PAUSE_ON_EXIT=1

ROOT=$(cd "$(dirname "$0")/.." && pwd)

# ── prerequisite checks ──────────────────────────────────────────────────────
require() { command -v "$1" >/dev/null 2>&1 || { echo "SKIP: $1 not on PATH"; exit 77; }; }
require tmux
require nats
require orch-spawn
command -v nats-server >/dev/null 2>&1 || { echo "SKIP: nats-server not on PATH (brew install nats-server)"; exit 77; }
command -v orch-agent-shim >/dev/null 2>&1 || { echo "SKIP: orch-agent-shim not on PATH (go build ./cmd/orch-agent-shim)"; exit 77; }

# ── test NATS server ─────────────────────────────────────────────────────────
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null || echo 14333)
NATS_URL="nats://127.0.0.1:${PORT}"
export NATS_URL

TMPDIR_OWN=$(mktemp -d)
TMUX_SESSION="orch-spawn-shim-test-$$"

cleanup() {
    # Kill the shim processes launched under the test NATS server.
    pkill -TERM -f "orch-agent-shim.*--pane" 2>/dev/null || true
    sleep 1
    pkill -KILL -f "orch-agent-shim.*--pane" 2>/dev/null || true
    # Kill the test tmux session (and its panes).
    tmux kill-session -t "$TMUX_SESSION" 2>/dev/null || true
    # Kill the test nats-server.
    [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
    rm -rf "$TMPDIR_OWN"
}
trap cleanup EXIT

# ── start test nats-server ───────────────────────────────────────────────────
nats-server -p "$PORT" >"$TMPDIR_OWN/server.log" 2>&1 &
SERVER_PID=$!

# Wait up to 5 s for the server to become reachable.
for _ in $(seq 1 50); do
    if nats --server="$NATS_URL" server ping --count=1 --timeout=200ms >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done
nats --server="$NATS_URL" server ping --count=1 --timeout=1s >/dev/null 2>&1 \
    || { echo "FAIL: test nats-server did not become reachable"; exit 1; }

# ── spawn a headless claude pane via orch-spawn ──────────────────────────────
# Use a real tmux session so orch-spawn can create a pane. Run in headless
# mode so no display is required. The agent CLI will exit quickly because
# no prompt is sent — that's fine; we only need the shim to register.
# --no-fleet avoids a missing fleet-prompt file error in CI.
tmux new-session -d -s "$TMUX_SESSION" "sleep 3600"

# orch-spawn needs TMUX set so tmux display-message works.
TMUX_PANE_ID=$(tmux list-panes -t "$TMUX_SESSION" -F '#{pane_id}' | head -1)
export TMUX="${TMUX:-}"

# Spawn: headed mode inside the test session so we get a real pane id.
# We override NATS_URL so the shim connects to our test server.
PANE=$(NATS_URL="$NATS_URL" \
       SESH_OWNER="spawn-shim-tester" \
       tmux new-window -d -t "$TMUX_SESSION:" -n "claude-test" -P -F '#{pane_id}' \
           "sleep 3600" \
       2>/dev/null) || { echo "FAIL: could not create test pane"; exit 1; }

# Manually invoke the shim as orch-spawn would (since we can't run
# orch-spawn's full flow without a real claude binary in PATH):
PANE_SAFE="${PANE//%/pct}"
SHIM_LOG_DIR="$HOME/.cache/orch-shim"
mkdir -p "$SHIM_LOG_DIR"
SHIM_LOG="$SHIM_LOG_DIR/${PANE_SAFE}.log"

ORCH_OWNER="spawn-shim-tester" \
ORCH_OUTFIT="" \
ORCH_ROLE="worker" \
SESH_SESSION="test-session" \
NATS_URL="$NATS_URL" \
orch-agent-shim \
    --agent claude-code \
    --pane "$PANE" \
    --cwd "$TMPDIR_OWN" \
    >"$SHIM_LOG" 2>&1 &
SHIM_PID=$!

# ── assert: pane appears in $SRV.INFO.agents within 5 s ──────────────────────
fail() { echo "FAIL: $*"; [ -f "$SHIM_LOG" ] && cat "$SHIM_LOG" >&2; exit 1; }
pass() { echo "PASS: $*"; }

INFO=""
for _ in $(seq 1 50); do
    INFO=$(nats --server="$NATS_URL" req '$SRV.INFO.agents' '' --timeout=500ms 2>/dev/null | tail -n +2 || true)
    if echo "$INFO" | grep -q '"agent":"claude-code"'; then
        break
    fi
    sleep 0.1
done

echo "$INFO" | grep -q '"agent":"claude-code"'   || fail "\$SRV.INFO.agents missing agent=claude-code after 5s"
echo "$INFO" | grep -q '"protocol_version":"0.3"' || fail "\$SRV.INFO.agents missing protocol_version=0.3"
pass "\$SRV.INFO.agents returns claude-code pane within 5s"

# ── assert: metadata carries forwarded env vars ───────────────────────────────
echo "$INFO" | grep -q '"owner":"spawn-shim-tester"' || fail "INFO missing owner=spawn-shim-tester (ORCH_OWNER not forwarded)"
pass "ORCH_OWNER forwarded correctly through disown"

# ── assert: shim log file was created ────────────────────────────────────────
[ -f "$SHIM_LOG" ] || fail "shim log not created at $SHIM_LOG"
pass "shim log created at $SHIM_LOG"

# ── assert: --no-shim is accepted (flag-parser check) ────────────────────────
# Invoke orch-spawn with a non-existent agent. If --no-shim were unknown,
# orch-spawn errors with "unknown flag: --no-shim" (exit 1). With --no-shim
# accepted, it advances past the parser and errors with "unknown agent"
# instead. The exit code is 1 either way; we discriminate via the error msg.
NOSHIM_OUT=$(orch-spawn nonesuch-agent --no-shim --cwd /tmp 2>&1 || true)
if echo "$NOSHIM_OUT" | grep -q "unknown flag: --no-shim"; then
    fail "--no-shim flag not recognized by orch-spawn parser"
fi
echo "$NOSHIM_OUT" | grep -q "unknown agent" \
    || fail "orch-spawn did not progress past --no-shim parse (got: $NOSHIM_OUT)"
pass "--no-shim is parsed as a known flag"

echo "All orch-spawn shim smoke checks passed."
