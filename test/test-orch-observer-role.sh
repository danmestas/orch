#!/usr/bin/env bash
# Regression tests for the A1 observer-role tag end-to-end:
#   - orch-register persists role (preserve-on-update, --role override)
#   - orch-spawn auto-detects role for stasi/wait-watch
#   - orch-listen default-excludes observers
#   - orch-tell refuses worker→observer (unless --force)
#   - orch-peek surfaces role in text + JSON
#
# Uses a sandboxed ORCH_REGISTRY_DIR for the fast portion. Spawn-based
# checks at the end use a real pi worker (free) and one real claude --outfit
# stasi spawn for the auto-detect path.
#
# Run with: bash test/test-orch-observer-role.sh
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

REGBIN=$(command -v orch-register)
SPAWN=$(command -v orch-spawn)
LISTEN=$(command -v orch-listen)
TELL=$(command -v orch-tell)
PEEK=$(command -v orch-peek)
[ -x "$REGBIN" ] && [ -x "$SPAWN" ] && [ -x "$LISTEN" ] && [ -x "$TELL" ] && [ -x "$PEEK" ] || {
    echo "binaries missing on PATH"; exit 2; }

# Sandbox the registry (fast portion).
SANDBOX=$(mktemp -d)
export ORCH_REGISTRY_DIR="$SANDBOX/registry"
mkdir -p "$ORCH_REGISTRY_DIR"
trap 'rm -rf "$SANDBOX"' EXIT

echo "=== orch-register role persistence ==="

# 1) New entry without --role → role=worker
"$REGBIN" %900 pi /tmp >/dev/null
assert "register: default new entry role=worker" "worker" "$(jq -r .role "$ORCH_REGISTRY_DIR/%900.json")"

# 2) New entry with --role observer → role=observer
"$REGBIN" %901 claude /tmp --role observer >/dev/null
assert "register: --role observer persisted" "observer" "$(jq -r .role "$ORCH_REGISTRY_DIR/%901.json")"

# 3) Re-register without --role preserves existing role (hook lazy-register doesn't clobber)
"$REGBIN" %901 claude /tmp >/dev/null
assert "register: re-register without --role preserves observer" "observer" "$(jq -r .role "$ORCH_REGISTRY_DIR/%901.json")"

# 4) --role override wins
"$REGBIN" %901 claude /tmp --role worker >/dev/null
assert "register: --role override wins" "worker" "$(jq -r .role "$ORCH_REGISTRY_DIR/%901.json")"

# 5) Invalid --role rejected
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$REGBIN" %902 pi /tmp --role bogus >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "register: invalid --role rejected" 1 "$rc"
assert_contains "register: invalid --role error names value" "bogus" "$(cat "$TMP_ERR")"
[ ! -f "$ORCH_REGISTRY_DIR/%902.json" ] && exists="absent" || exists="present"
assert "register: invalid --role does not write file" "absent" "$exists"
rm -f "$TMP_OUT" "$TMP_ERR"

# 6) spawn_ts_ns preserved on re-register (existing invariant)
SPAWN_TS_BEFORE=$(jq -r .spawn_ts_ns "$ORCH_REGISTRY_DIR/%900.json")
sleep 0.1
"$REGBIN" %900 pi /tmp >/dev/null
SPAWN_TS_AFTER=$(jq -r .spawn_ts_ns "$ORCH_REGISTRY_DIR/%900.json")
assert "register: spawn_ts_ns preserved on update" "$SPAWN_TS_BEFORE" "$SPAWN_TS_AFTER"

echo
echo "=== orch-listen observer-default-exclusion (stubbed registry) ==="

# Pre-populate fake registry entries: %900 worker, %901 observer.
"$REGBIN" %900 pi /tmp --role worker >/dev/null
"$REGBIN" %901 claude /tmp --role observer >/dev/null

export ORCH_STOP_DIR="$SANDBOX/stop"
mkdir -p "$ORCH_STOP_DIR"

# Marker file content shape mirrors what the real Stop hook writes.
write_marker() {
    local pane=$1 ns
    ns=$(date +%s%N)
    cat > "$ORCH_STOP_DIR/$pane.event" <<EOT
ts_ns=$ns
ts_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)
pane_id=$pane
session_id=test-fake
cwd=/tmp
EOT
}

# Pattern: start listen in background, then write marker so its mtime bumps
# AFTER the SEEN snapshot is taken. fswatch fires, listen processes, exits.
listen_then_fire() {
    # $1=timeout $2=pane_to_fire $3=opts(string) $4=out $5=err
    local timeout=$1 pane=$2 opts=$3 out=$4 err=$5 pid rc
    "$LISTEN" "$timeout" $opts >"$out" 2>"$err" &
    pid=$!
    sleep 0.5  # let SEEN snapshot + fswatch arm
    write_marker "$pane"
    wait $pid; rc=$?
    echo "$rc"
}

# 7) Default: observer event is silent; listen times out.
rm -f "$ORCH_STOP_DIR"/*.event
rc=$(listen_then_fire 2 %901 "" "$SANDBOX/listen_out" "$SANDBOX/listen_err")
assert "listen default: observer event ignored (timeout)" 2 "$rc"
assert "listen default: stdout empty on observer-only fire" "" "$(cat "$SANDBOX/listen_out")"

# 8) --include-observers: observer event wakes the listener.
rm -f "$ORCH_STOP_DIR"/*.event
rc=$(listen_then_fire 2 %901 "--include-observers" "$SANDBOX/listen_out" "$SANDBOX/listen_err")
assert "listen --include-observers: observer event surfaces" 0 "$rc"
assert_contains "listen --include-observers: pane_id in output" "pane_id=%901" "$(cat "$SANDBOX/listen_out")"

# 9) Worker event ALWAYS surfaces (even without --include-observers).
rm -f "$ORCH_STOP_DIR"/*.event
rc=$(listen_then_fire 2 %900 "" "$SANDBOX/listen_out" "$SANDBOX/listen_err")
assert "listen default: worker event surfaces" 0 "$rc"
assert_contains "listen default: worker pane_id in output" "pane_id=%900" "$(cat "$SANDBOX/listen_out")"

echo
echo "=== orch-tell worker→observer guard (stubbed registry) ==="

# Spawn a real harmless target pane (sleep) so orch-tell's tmux-list and
# capture-pane checks pass. Uses the orchestrator's tmux session.
TARGET_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' 'while true; do sleep 60; done' 2>/dev/null) || {
    echo "  SKIP  no tmux pane to use as tell target"; TARGET_PANE=""; }

if [ -n "$TARGET_PANE" ]; then
    # Mark this pane as observer-role in the sandbox registry.
    "$REGBIN" "$TARGET_PANE" claude /tmp --role observer >/dev/null

    # 10) Worker-source (ORCH_PANE_ID set) refused without --force
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=2 "$TELL" "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell worker→observer: refused" 1 "$rc"
    assert_contains "tell worker→observer: error names refusal" "refusing to tell observer" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 11) --force bypasses the guard
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    ORCH_PANE_ID=%999 ORCH_TELL_MAX_WAIT=5 "$TELL" --force "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell --force worker→observer: allowed" 0 "$rc"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 12) Operator-source (no ORCH_PANE_ID) unrestricted
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    unset ORCH_PANE_ID
    ORCH_TELL_MAX_WAIT=5 "$TELL" "$TARGET_PANE" "hello" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "tell operator→observer: allowed (no ORCH_PANE_ID)" 0 "$rc"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # cleanup target pane
    tmux kill-pane -t "$TARGET_PANE" 2>/dev/null || true
fi

echo
echo "=== orch-peek role surface (stubbed registry) ==="

# A peek run pulls from the sandbox registry. The tell-target pane was killed,
# so its registry entry is now dead — peek's --all should still surface it
# with the role tag.
PEEK_JSON=$("$PEEK" --all --json 2>/dev/null)
OBSERVER_ROW_COUNT=$(printf '%s' "$PEEK_JSON" | jq '[.[] | select(.role=="observer")] | length' 2>/dev/null || echo 0)
WORKER_ROW_COUNT=$(printf '%s' "$PEEK_JSON" | jq '[.[] | select(.role=="worker")] | length' 2>/dev/null || echo 0)
echo "  peek --all --json yielded observer=$OBSERVER_ROW_COUNT worker=$WORKER_ROW_COUNT rows"
assert "peek --json: at least one observer row" 1 "$( [ "$OBSERVER_ROW_COUNT" -ge 1 ] && echo 1 || echo 0 )"
assert "peek --json: at least one worker row" 1 "$( [ "$WORKER_ROW_COUNT" -ge 1 ] && echo 1 || echo 0 )"

echo
echo "=== orch-spawn role auto-detect (real pi spawn — free, headless) ==="

# Use the real registry (~/.cache/orch-registry/) for spawn-based checks.
unset ORCH_REGISTRY_DIR ORCH_STOP_DIR

# 13) pi default → role=worker
P1=$("$SPAWN" pi --headless --no-fleet --cwd /tmp 2>/dev/null)
sleep 1
ROLE_P1=$(jq -r '.role // empty' "$HOME/.cache/orch-registry/$P1.json" 2>/dev/null || echo "")
assert "spawn pi default: role=worker" "worker" "$ROLE_P1"
tmux kill-pane -t "$P1" 2>/dev/null; sleep 1
rm -f "$HOME/.cache/orch-registry/$P1.json"

# 14) pi --role observer (explicit override) → role=observer
P2=$("$SPAWN" pi --headless --no-fleet --cwd /tmp --role observer 2>/dev/null)
sleep 1
ROLE_P2=$(jq -r '.role // empty' "$HOME/.cache/orch-registry/$P2.json" 2>/dev/null || echo "")
assert "spawn pi --role observer: tag persisted" "observer" "$ROLE_P2"
tmux kill-pane -t "$P2" 2>/dev/null; sleep 1
rm -f "$HOME/.cache/orch-registry/$P2.json"

echo
echo "=== orch-spawn auto-detect on real claude --outfit stasi (1 real spawn) ==="

# Allow opt-out for cost-conscious runs.
if [ "${SKIP_REAL_OUTFIT:-0}" = "1" ]; then
    echo "  SKIP  SKIP_REAL_OUTFIT=1 — outfit auto-detect untested in this run"
else
    P3=$("$SPAWN" claude --headless --no-fleet --cwd /tmp --outfit stasi --cut wait-watch 2>/dev/null) || P3=""
    if [ -n "$P3" ] && [[ "$P3" =~ ^%[0-9]+$ ]]; then
        sleep 1
        ROLE_P3=$(jq -r '.role // empty' "$HOME/.cache/orch-registry/$P3.json" 2>/dev/null || echo "")
        assert "spawn claude --outfit stasi --cut wait-watch: auto role=observer" "observer" "$ROLE_P3"
        tmux kill-pane -t "$P3" 2>/dev/null; sleep 2
        rm -f "$HOME/.cache/orch-registry/$P3.json"
    else
        echo "  SKIP  could not spawn (suit unavailable or stasi outfit missing)"
    fi
fi

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
