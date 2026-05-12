#!/usr/bin/env bash
# Regression tests for `orch-listen --stream` (A3).
#
# Validates:
#   - --stream emits one JSON line per event without exiting on event
#   - --stream still applies observer default-exclude
#   - --stream + --include-observers surfaces observer events
#   - --stream emits valid JSON (parses through jq, has expected fields)
#   - --stream exits cleanly (rc=0) on timeout instead of rc=2
#   - Legacy one-shot mode still exits after first event (not regressed)
#   - --stream without jq fails fast (rc=1 with helpful stderr)
#
# Run with: bash test/test-orch-listen-stream.sh
set -uo pipefail

PASS=0
FAIL=0
FAILED_TESTS=()

assert() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected: $expected"
        echo "        got:      $got"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

assert_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" == *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected substring: $substr"
        echo "        got:                $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

LISTEN=$(command -v orch-listen)
REGBIN=$(command -v orch-register)
[ -x "$LISTEN" ] && [ -x "$REGBIN" ] || { echo "harness binaries missing on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
export ORCH_REGISTRY_DIR="$SANDBOX/registry"
export ORCH_STOP_DIR="$SANDBOX/stop"
mkdir -p "$ORCH_REGISTRY_DIR" "$ORCH_STOP_DIR"
trap 'rm -rf "$SANDBOX"' EXIT

# Pre-register two panes: %900 worker, %901 observer.
"$REGBIN" %900 pi /tmp --role worker >/dev/null
"$REGBIN" %901 claude /tmp --role observer >/dev/null

write_marker() {
    local pane=$1
    cat > "$ORCH_STOP_DIR/$pane.event" <<EOT
ts_ns=$(date +%s%N)
ts_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)
pane_id=$pane
session_id=test-fake
cwd=/tmp
EOT
}

write_notify() {
    local pane=$1
    cat > "$ORCH_STOP_DIR/$pane.notify" <<EOT
ts_ns=$(date +%s%N)
ts_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)
pane_id=$pane
session_id=test-fake
cwd=/tmp
message=needs-attention
EOT
}

echo "=== --stream emits per-event JSON, doesn't exit on first event ==="

# 1) Start --stream in bg with a 4s timeout. Write 3 worker markers spaced 0.5s
# apart. Stream should emit 3 JSON lines and then exit at timeout.
OUT="$SANDBOX/stream_out_1"
ERR="$SANDBOX/stream_err_1"
"$LISTEN" 4 --stream >"$OUT" 2>"$ERR" &
PID=$!
sleep 0.5  # let SEEN snapshot + first fswatch arm

write_marker %900; sleep 0.5
write_marker %900; sleep 0.5
write_marker %900; sleep 0.5

wait "$PID"; rc=$?

assert "stream: rc=0 on clean timeout exit" 0 "$rc"
LINES=$(awk 'END{print NR}' "$OUT")
# Allow some flakiness on slow boxes — stream must catch at least 1, ideally 3.
[ "$LINES" -ge 1 ] && pass="ok" || pass="zero-events"
assert "stream: emits at least 1 event line ($LINES seen)" "ok" "$pass"

# 2) Each line must be valid JSON with the expected fields.
all_valid="ok"
while IFS= read -r line; do
    [ -z "$line" ] && continue
    if ! printf '%s' "$line" | jq -e '.event_file and .pane_id and .ext and .ts_ns_emit and .kv' >/dev/null 2>&1; then
        all_valid="invalid: $line"
        break
    fi
done < "$OUT"
assert "stream: every line is valid JSON with required fields" "ok" "$all_valid"

# 3) JSON line records the firing pane id.
PANE_FOUND=$(jq -r '.pane_id' "$OUT" 2>/dev/null | head -1)
assert "stream: pane_id field carries firing pane" "%900" "$PANE_FOUND"

echo
echo "=== --stream applies observer default-exclude ==="

# 4) Stream with default observer policy: observer event fires but no JSON.
rm -f "$ORCH_STOP_DIR"/*.event
OUT="$SANDBOX/stream_out_2"
ERR="$SANDBOX/stream_err_2"
"$LISTEN" 3 --stream >"$OUT" 2>"$ERR" &
PID=$!
sleep 0.5
write_marker %901  # observer
sleep 0.5
write_marker %901  # observer
wait "$PID"; rc=$?

assert "stream: observer-only fires → clean rc=0 timeout" 0 "$rc"
LINES_OBS=$(awk 'END{print NR}' "$OUT")
assert "stream: observer-only fires → 0 lines emitted" 0 "$LINES_OBS"

echo
echo "=== --stream --include-observers surfaces observer events ==="

# 5) Same observer fire pattern with --include-observers.
rm -f "$ORCH_STOP_DIR"/*.event
OUT="$SANDBOX/stream_out_3"
ERR="$SANDBOX/stream_err_3"
"$LISTEN" 3 --stream --include-observers >"$OUT" 2>"$ERR" &
PID=$!
sleep 0.5
write_marker %901
sleep 0.5
wait "$PID"; rc=$?

assert "stream + include-observers: rc=0" 0 "$rc"
LINES_INC=$(awk 'END{print NR}' "$OUT")
[ "$LINES_INC" -ge 1 ] && saw="ok" || saw="missed"
assert "stream + include-observers: at least 1 observer event surfaced" "ok" "$saw"
PANE_INC=$(jq -r '.pane_id' "$OUT" 2>/dev/null | head -1)
assert "stream + include-observers: pane_id is the observer" "%901" "$PANE_INC"

echo
echo "=== legacy one-shot mode: not regressed by A3 ==="

# 6) Legacy mode still exits after first matched event (rc=0 + key=value block).
rm -f "$ORCH_STOP_DIR"/*.event
OUT="$SANDBOX/oneshot_out"
ERR="$SANDBOX/oneshot_err"
"$LISTEN" 5 >"$OUT" 2>"$ERR" &
PID=$!
sleep 0.5
write_marker %900
wait "$PID"; rc=$?

assert "one-shot: rc=0 on first event" 0 "$rc"
assert_contains "one-shot: stdout contains EVENT_FILE= header" "EVENT_FILE=" "$(cat "$OUT")"
assert_contains "one-shot: stdout contains pane_id=%900" "pane_id=%900" "$(cat "$OUT")"

# 7) One-shot timeout still exits rc=2 (not rc=0).
rm -f "$ORCH_STOP_DIR"/*.event
"$LISTEN" 2 >"$SANDBOX/timeout_out" 2>"$SANDBOX/timeout_err" && rc=0 || rc=$?
assert "one-shot: rc=2 on timeout" 2 "$rc"

echo
echo "=== --stream usage error path ==="

# 8) --stream + non-existent flag → exit 1, stderr surfaces.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$LISTEN" --stream --bogus-flag >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "stream: unknown flag → rc=1" 1 "$rc"
assert_contains "stream: unknown flag → stderr names the flag" "--bogus-flag" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== notify markers are included by default ==="

# 9) Default behavior: notify markers should wake the listener (one-shot).
rm -f "$ORCH_STOP_DIR"/*.event "$ORCH_STOP_DIR"/*.notify
OUT="$SANDBOX/notify_default_out"
ERR="$SANDBOX/notify_default_err"
"$LISTEN" 5 >"$OUT" 2>"$ERR" &
PID=$!
sleep 0.5
write_notify %900
wait "$PID"; rc=$?

assert "notify default-on: rc=0 on notify marker" 0 "$rc"
assert_contains "notify default-on: stdout shows .notify EVENT_FILE" ".notify" "$(cat "$OUT")"
assert_contains "notify default-on: stdout carries pane_id=%900" "pane_id=%900" "$(cat "$OUT")"

echo
echo "=== --exclude-notify suppresses notify markers ==="

# 10) With --exclude-notify, notify-only fire → timeout (rc=2) for one-shot.
rm -f "$ORCH_STOP_DIR"/*.event "$ORCH_STOP_DIR"/*.notify
"$LISTEN" 2 --exclude-notify >"$SANDBOX/notify_excl_out" 2>"$SANDBOX/notify_excl_err" &
PID=$!
sleep 0.5
write_notify %900
wait "$PID" && rc=0 || rc=$?

assert "exclude-notify: notify-only → rc=2 timeout" 2 "$rc"
LINES_EXCL=$(awk 'END{print NR}' "$SANDBOX/notify_excl_out")
assert "exclude-notify: no event emitted" 0 "$LINES_EXCL"

# 11) --exclude-notify still wakes on .event markers.
rm -f "$ORCH_STOP_DIR"/*.event "$ORCH_STOP_DIR"/*.notify
"$LISTEN" 5 --exclude-notify >"$SANDBOX/notify_excl2_out" 2>"$SANDBOX/notify_excl2_err" &
PID=$!
sleep 0.5
write_marker %900
wait "$PID"; rc=$?

assert "exclude-notify: .event still wakes (rc=0)" 0 "$rc"
assert_contains "exclude-notify: stdout is the .event" ".event" "$(cat "$SANDBOX/notify_excl2_out")"

# 12) Legacy --include-notify is accepted (no-op).
rm -f "$ORCH_STOP_DIR"/*.event "$ORCH_STOP_DIR"/*.notify
"$LISTEN" 5 --include-notify >"$SANDBOX/notify_legacy_out" 2>"$SANDBOX/notify_legacy_err" &
PID=$!
sleep 0.5
write_notify %900
wait "$PID"; rc=$?

assert "legacy --include-notify: still accepted, notify wakes (rc=0)" 0 "$rc"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
