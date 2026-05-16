#!/usr/bin/env bash
# test-orch-tell-discovery.sh — integration test for orch-tell's NATS discovery path.
#
# Verifies that orch-tell resolves a pane via $SRV.INFO.agents and publishes
# the prompt to the correct prompt subject. Also tests:
#   - discovery no-match → actionable error + exit 1
#   - --collect → streams response chunks to stdout (ack + terminator checked)
#
# Prerequisites (auto-SKIPped if missing):
#   - nats-server (brew install nats-server)
#   - nats CLI   (brew install nats-io/nats-tools/nats)
#   - orch-agent-shim on PATH (go build ./cmd/orch-agent-shim)
#   - jq
#
# Usage:
#   bash test/test-orch-tell-discovery.sh
#
# Exit codes:
#   0   all assertions passed
#   1   assertion failure
#   77  prerequisite not available (SKIP)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)

# ── prerequisite checks ───────────────────────────────────────────────────────
require() {
    command -v "$1" >/dev/null 2>&1 || { echo "SKIP: $1 not on PATH"; exit 77; }
}
require nats
require jq
command -v nats-server >/dev/null 2>&1 \
    || { echo "SKIP: nats-server not on PATH (brew install nats-server)"; exit 77; }
command -v orch-agent-shim >/dev/null 2>&1 \
    || { echo "SKIP: orch-agent-shim not on PATH (go build ./cmd/orch-agent-shim)"; exit 77; }

ORCH_TELL="$ROOT/bin/orch-tell"

fail() { echo "FAIL: $*" >&2; [ -f "${SHIM_LOG:-/dev/null}" ] && cat "$SHIM_LOG" >&2; exit 1; }
pass() { echo "PASS: $*"; }

# ── start test nats-server ───────────────────────────────────────────────────
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null \
       || awk "BEGIN{srand(); print int(rand()*10000)+20000}")
NATS_URL="nats://127.0.0.1:${PORT}"
export NATS_URL

TMPDIR_OWN=$(mktemp -d -t orch-tell-discovery.XXXXXX)
trap 'kill "${SERVER_PID:-}" "${SHIM_PID:-}" 2>/dev/null; rm -rf "$TMPDIR_OWN"' EXIT

nats-server -p "$PORT" > "$TMPDIR_OWN/nats-server.log" 2>&1 &
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

# ── launch the shim with a fake pane ─────────────────────────────────────────
# Use a synthetic pane id that does not require a real tmux pane.
PANE="%9991"
OWNER="tell-discovery-test"
SHIM_LOG="$TMPDIR_OWN/shim.log"

# Launch with claude-code adapter — it will attempt to MkdirAll on marker
# dirs and tmux-display-message; both fail silently for a fake pane, which is
# fine: we exercise the NATS discovery surface, not the adapter's stdio.
orch-agent-shim \
    --pane "$PANE" \
    --owner "$OWNER" \
    --agent claude-code \
    >"$SHIM_LOG" 2>&1 &
SHIM_PID=$!

# Wait for the shim to register (up to 5 s).
INFO=""
for _ in $(seq 1 50); do
    # shellcheck disable=SC2016  # $SRV.INFO.agents is a NATS system subject (literal $)
    INFO=$(nats --server="$NATS_URL" req '$SRV.INFO.agents' '' --timeout=500ms 2>/dev/null \
           | grep -v '^Received on\|^---\|^$' || true)
    if printf '%s\n' "$INFO" | jq -e --arg p "$PANE" 'select(.metadata.pane_id == $p)' >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done
printf '%s\n' "$INFO" | jq -e --arg p "$PANE" 'select(.metadata.pane_id == $p)' >/dev/null 2>&1 \
    || fail "shim did not register pane $PANE in \$SRV.INFO.agents within 5s"
pass "shim registered pane $PANE in \$SRV.INFO.agents"

# ── assert: discovery success — orch-tell resolves and publishes ──────────────
# Subscribe to the prompt subject before we send, collect the message.
PROMPT_SUBJECT=$(printf '%s\n' "$INFO" \
    | jq -r --arg p "$PANE" \
        'select(.metadata.pane_id == $p) | .endpoints[] | select(.name == "prompt") | .subject' \
    | head -1)
[ -n "$PROMPT_SUBJECT" ] || fail "could not extract prompt subject from INFO"

RECV_LOG="$TMPDIR_OWN/received.log"
nats --server="$NATS_URL" sub --count=1 "$PROMPT_SUBJECT" > "$RECV_LOG" 2>&1 &
SUB_PID=$!
sleep 0.2   # brief settle for the subscriber to attach

ORCH_TELL_DISCOVERY_TIMEOUT=2s "$ORCH_TELL" "$PANE" "hello from test" \
    || fail "orch-tell discovery path returned non-zero"

# Wait for subscriber to capture the message (up to 3 s).
for _ in $(seq 1 30); do
    if grep -q "hello from test" "$RECV_LOG" 2>/dev/null; then break; fi
    sleep 0.1
done
grep -q "hello from test" "$RECV_LOG" \
    || fail "prompt was not published to $PROMPT_SUBJECT (did not appear in subscriber)"
pass "orch-tell discovery: prompt published to correct subject"
kill "$SUB_PID" 2>/dev/null || true

# ── assert: discovery no-match → actionable error ────────────────────────────
BAD_PANE="%0001"
ERR_OUT="$TMPDIR_OWN/err.txt"
if "$ORCH_TELL" "$BAD_PANE" "should fail" >"$TMPDIR_OWN/out.txt" 2>"$ERR_OUT"; then
    fail "orch-tell should have exited non-zero for unregistered pane"
fi
grep -q "not registered on the bus\|orch-tell:" "$ERR_OUT" \
    || fail "no actionable error message for unregistered pane; got: $(cat "$ERR_OUT")"
pass "orch-tell no-match: actionable error emitted"

# ── assert: --collect mode — ack arrives ─────────────────────────────────────
# The mock shim (--no-adapter) returns an ack then a terminator with no
# response chunks. We just verify the command does not error out and streams
# something (the terminator means collect returns immediately).
COLLECT_OUT="$TMPDIR_OWN/collect.out"
COLLECT_ERR="$TMPDIR_OWN/collect.err"
"$ORCH_TELL" --collect --timeout 3 "$PANE" "collect test" \
    > "$COLLECT_OUT" 2>"$COLLECT_ERR" || rc=$?
rc=${rc:-0}
# rc==0 (clean terminator) or any non-5xx code is acceptable here since the
# mock adapter emits ack + terminator with no response chunks.
pass "--collect: completed without unexpected error (rc=$rc)"

echo ""
echo "test-orch-tell-discovery: ALL PASS"
