#!/usr/bin/env bash
# test-orch-agent-shim.sh — bash smoke test for orch-agent-shim.
#
# Spins up an in-process nats-server-equivalent (we use `nats-server`
# if installed; otherwise the test SKIPs), launches the shim with a
# mock pane id, and exercises §SRV.INFO / status / heartbeat / prompt
# via `nats req` / `nats sub`.
#
# Independent of the Go test suite — those run against an embedded
# server inside the Go binary. This script is the "from outside,
# with real CLI tools" check, plus the entry point for the
# wire-compat TS runner.
#
# Usage:
#   bash test/test-orch-agent-shim.sh           # basic protocol checks
#   bash test/test-orch-agent-shim.sh --wire    # also run TS wire-compat
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WIRE=0
[ "${1:-}" = "--wire" ] && WIRE=1

require() {
    command -v "$1" >/dev/null 2>&1 || { echo "SKIP: $1 not on PATH"; exit 77; }
}
require nats
command -v nats-server >/dev/null 2>&1 || { echo "SKIP: nats-server not on PATH (brew install nats-server)"; exit 77; }

# Pick a random port to avoid colliding with a live hub.
PORT=$(python3 -c 'import socket; s=socket.socket(); s.bind(("",0)); print(s.getsockname()[1]); s.close()' 2>/dev/null || echo 14222)
NATS_URL="nats://127.0.0.1:${PORT}"
export NATS_URL

cleanup() {
    [ -n "${SHIM_PID:-}" ] && kill "$SHIM_PID" 2>/dev/null || true
    [ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null || true
    [ -n "${TMPDIR_OWN:-}" ] && rm -rf "$TMPDIR_OWN" || true
}
trap cleanup EXIT

TMPDIR_OWN=$(mktemp -d)
nats-server -p "$PORT" -DV >"$TMPDIR_OWN/server.log" 2>&1 &
SERVER_PID=$!
# Wait for the server to become reachable.
for _ in $(seq 1 50); do
    if nats --server="$NATS_URL" server ping --count=1 --timeout=200ms >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done

# Build the shim binary.
cd "$ROOT"
go build -o "$TMPDIR_OWN/orch-agent-shim" ./cmd/orch-agent-shim

# Launch the shim against a fake pane. The claude-code adapter will
# attempt to MkdirAll on the marker dirs and tmux-display-message; the
# latter will quietly fail (no tmux pane), which is fine for protocol
# checks — we exercise the Synadia surface, not the adapter's stdio.
PANE='%9999'
OWNER='wire-tester'
"$TMPDIR_OWN/orch-agent-shim" \
    --agent claude-code \
    --pane "$PANE" \
    --owner "$OWNER" \
    --nats "$NATS_URL" \
    --interval 2s \
    >"$TMPDIR_OWN/shim.log" 2>&1 &
SHIM_PID=$!

# Wait for service registration.
for _ in $(seq 1 50); do
    if nats --server="$NATS_URL" req '$SRV.PING.agents' '' --timeout=200ms >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done

# --- Assertions ---------------------------------------------------------------

fail() { echo "FAIL: $*"; cat "$TMPDIR_OWN/shim.log" >&2; exit 1; }
pass() { echo "PASS: $*"; }

# §SRV.INFO.agents must return a record naming our pane.
INFO=$(nats --server="$NATS_URL" req '$SRV.INFO.agents' '' --timeout=2s 2>/dev/null | tail -n +2 || true)
echo "$INFO" | grep -q '"agent":"claude-code"' || fail "INFO missing agent=claude-code"
echo "$INFO" | grep -q "\"pane_id\":\"$PANE\"" || fail "INFO missing pane_id=$PANE"
echo "$INFO" | grep -q '"protocol_version":"0.3"' || fail "INFO missing protocol_version=0.3"
echo "$INFO" | grep -q "agents.prompt.cc.$OWNER.pct9999" || fail "INFO missing prompt subject"
pass "§SRV.INFO.agents shape"

# Status endpoint replies with heartbeat shape.
STATUS=$(nats --server="$NATS_URL" req "agents.status.cc.$OWNER.pct9999" '' --timeout=2s 2>/dev/null | tail -n +2 || true)
echo "$STATUS" | grep -q '"agent":"claude-code"' || fail "status reply missing agent"
echo "$STATUS" | grep -q '"interval_s"' || fail "status reply missing interval_s"
pass "§8.7 status endpoint reply"

# Heartbeat is published.
HB_FILE="$TMPDIR_OWN/hb.json"
( nats --server="$NATS_URL" sub "agents.hb.cc.$OWNER.pct9999" --count=1 --raw >"$HB_FILE" 2>&1 || true ) &
HB_WAITER=$!
wait "$HB_WAITER" 2>/dev/null || true
test -s "$HB_FILE" || fail "no heartbeat observed within $(test -e /dev/null && echo a few)s"
grep -q '"interval_s"' "$HB_FILE" || fail "heartbeat missing interval_s"
pass "§8.1/§8.3 heartbeat"

# Prompt: ack must be first, terminator must be last. We use a short
# timeout because the mock pane never produces real responses.
PROMPT_OUT="$TMPDIR_OWN/prompt.out"
nats --server="$NATS_URL" req "agents.prompt.cc.$OWNER.pct9999" "compat ping" --timeout=1s >"$PROMPT_OUT" 2>&1 || true
# `nats req` prints the first message it gets; the protocol's ack
# should land before the request times out waiting for the rest.
grep -q '"type":"status"' "$PROMPT_OUT" || fail "prompt did not yield first ack chunk"
grep -q '"data":"ack"' "$PROMPT_OUT" || fail "first chunk was not status:ack"
pass "§6.4 first-chunk ack"

# Optional: wire-compat against the upstream TS SDK.
if [ "$WIRE" -eq 1 ]; then
    command -v bun >/dev/null 2>&1 || { echo "SKIP: bun not on PATH for --wire"; exit 0; }
    pushd "$ROOT/test/wire-compat" >/dev/null
    bun install --silent
    OWNER="$OWNER" PANE="$PANE" NATS_URL="$NATS_URL" bun run wire-compat.ts
    popd >/dev/null
    pass "wire-compat (TS SDK)"
fi

echo "All shim smoke checks passed."
