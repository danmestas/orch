#!/usr/bin/env bash
# test-cc-subagent-bridge.sh — end-to-end smoke for the
# claudecode-subagent-panel extension.
#
# Spins up a real NATS server (via the nats-server binary if available,
# else skipped), fakes a CC projects/<cwd-enc>/<uuid>/ directory under a
# tempdir, runs the bridge for ~3 seconds, publishes a synthetic
# $SRV.INFO.agents reply when the bridge requests it, and verifies that a
# matching agent-pct*.jsonl materialises in the fake subagents dir.
#
# Skips gracefully if:
#   - nats-server or nats CLI is not on PATH
#   - Go is not on PATH (we build the bridge inline)
#
# Run from the orch repo root or via:  bash test/test-cc-subagent-bridge.sh

set -uo pipefail

# ---------------------------------------------------------------------
# Prereqs
# ---------------------------------------------------------------------
have() { command -v "$1" >/dev/null 2>&1; }

if ! have nats-server; then
    echo "test-cc-subagent-bridge: nats-server not on PATH — skipping" >&2
    exit 0
fi
if ! have nats; then
    echo "test-cc-subagent-bridge: nats CLI not on PATH — skipping" >&2
    exit 0
fi
if ! have go; then
    echo "test-cc-subagent-bridge: go not on PATH — skipping" >&2
    exit 0
fi

# Find repo root from this script's location so the test runs the same
# whether invoked via the CI matrix or stand-alone.
HERE=$(cd "$(dirname "$0")" && pwd)
REPO=$(cd "$HERE/.." && pwd)

# ---------------------------------------------------------------------
# Set up sandbox
# ---------------------------------------------------------------------
TMP=$(mktemp -d -t cc-bridge.XXXXXX)
cleanup() {
    [ -n "${NATS_PID:-}" ] && kill "$NATS_PID" 2>/dev/null || true
    [ -n "${BRIDGE_PID:-}" ] && kill "$BRIDGE_PID" 2>/dev/null || true
    [ -n "${RESPONDER_PID:-}" ] && kill "$RESPONDER_PID" 2>/dev/null || true
    rm -rf "$TMP"
}
trap cleanup EXIT

# Pick a free port for NATS so parallel CI jobs don't collide.
PORT=$((30000 + RANDOM % 5000))
NATS_URL="nats://127.0.0.1:${PORT}"

# Fake CC projects layout.
PROJECTS="$TMP/projects"
SESSION_UUID="abcdef12-3456-4abc-8def-0123456789ab"
CWD_ENC="-private-tmp-fake-workspace"
SESS_DIR="$PROJECTS/$CWD_ENC/$SESSION_UUID"
mkdir -p "$SESS_DIR"
# Plant a transcript jsonl so ccsession detection picks this dir.
echo '{}' > "$SESS_DIR/transcript.jsonl"

# Build the bridge binary.
BRIDGE="$TMP/orch-cc-subagent-bridge"
(cd "$REPO" && go build -o "$BRIDGE" ./extensions/claudecode-subagent-panel/cmd/orch-cc-subagent-bridge) \
    || { echo "go build failed"; exit 1; }

# ---------------------------------------------------------------------
# Start NATS
# ---------------------------------------------------------------------
nats-server --port "$PORT" >"$TMP/nats.log" 2>&1 &
NATS_PID=$!
# Poll until it's accepting (max 5s).
for _ in 1 2 3 4 5 6 7 8 9 10; do
    if nats --server="$NATS_URL" server check connection >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done
if ! nats --server="$NATS_URL" server check connection >/dev/null 2>&1; then
    echo "NATS did not start; see $TMP/nats.log"
    exit 1
fi

# ---------------------------------------------------------------------
# Stand up a tiny "responder" that replies to $SRV.INFO.agents with a
# fake service-info envelope. We use a background `nats reply` to do
# this — it answers every request on $SRV.INFO.agents with the JSON we
# stage in the file. The bridge's discovery loop will pick it up.
# ---------------------------------------------------------------------
FAKE_INFO=$(cat <<JSON
{"id":"abc","name":"agents","version":"0.3.0","metadata":{"agent":"echo","harness":"echo","owner":"testop","pane_id":"%42","cwd":"/tmp"}}
JSON
)
# Use nats reply (subscribes + answers each request). Background it.
nats --server="$NATS_URL" reply '$SRV.INFO.agents' "$FAKE_INFO" >"$TMP/responder.log" 2>&1 &
RESPONDER_PID=$!
sleep 0.5

# ---------------------------------------------------------------------
# Launch the bridge against the sandbox.
# ---------------------------------------------------------------------
NATS_URL="$NATS_URL" \
ORCH_BRIDGE_CC_PROJECTS_DIR="$PROJECTS" \
ORCH_BRIDGE_KEEP_FILES=1 \
"$BRIDGE" >"$TMP/bridge.log" 2>&1 &
BRIDGE_PID=$!

# Give the bridge up to 5s to discover the fake agent + seed the JSONL.
SEED_FILE="$SESS_DIR/subagents/agent-pct42.jsonl"
for _ in 1 2 3 4 5 6 7 8 9 10; do
    [ -f "$SEED_FILE" ] && break
    sleep 0.5
done

# ---------------------------------------------------------------------
# Verify
# ---------------------------------------------------------------------
fail=0
if [ ! -f "$SEED_FILE" ]; then
    echo "FAIL: seed file not created at $SEED_FILE"
    echo "--- bridge log ---"
    cat "$TMP/bridge.log"
    fail=1
else
    # Validate the seed line shape.
    line=$(head -1 "$SEED_FILE")
    for field in '"isSidechain":true' '"type":"user"' '"agentId":"pct42"' '"sessionId":"abcdef12-3456-4abc-8def-0123456789ab"'; do
        if ! echo "$line" | grep -q "$field"; then
            echo "FAIL: seed line missing $field; got: $line"
            fail=1
        fi
    done
fi

# Publish a synthetic response chunk and check a second line appears.
nats --server="$NATS_URL" pub 'agents.event.echo.testop.pct42' '{"type":"response","data":"hello bridge"}' >/dev/null 2>&1
sleep 1.0
lines=$(wc -l < "$SEED_FILE" 2>/dev/null || echo 0)
if [ "$lines" -lt 2 ]; then
    echo "FAIL: expected 2+ lines after chunk publish, got $lines"
    echo "--- bridge log ---"
    cat "$TMP/bridge.log"
    fail=1
else
    if ! grep -q '"hello bridge"' "$SEED_FILE"; then
        echo "FAIL: chunk text not surfaced in JSONL"
        fail=1
    fi
fi

if [ $fail -eq 0 ]; then
    echo "test-cc-subagent-bridge: PASS"
    exit 0
else
    echo "test-cc-subagent-bridge: FAIL"
    exit 1
fi
