#!/usr/bin/env bash
# Regression tests for orch-subscribe.
# Run with: bash test/test-orch-subscribe.sh
# Requires tmux session (it spawns + cleans up its own panes).
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

# Each test uses a unique fake peer id, so leftover daemons from prior runs
# can't pollute the assertions. We don't preemptively kill stragglers because
# matching by cmdline can hit the test runner's own ancestors.
TS=$(date +%s)
FAKE_BASE="%9${TS:5}"  # last digits of epoch seconds → unique-ish per run

[ -n "${TMUX:-}" ] || { echo "must run inside tmux"; exit 1; }
command -v orch-subscribe >/dev/null || { echo "orch-subscribe not on PATH"; exit 1; }

#---- CLI surface tests (no daemon spawning) ----
echo "## CLI surface"

# 1. No args → usage error.
out=$(ORCH_PANE_ID=%999 orch-subscribe 2>&1 || true)
assert_contains "no args prints usage" "usage:" "$out"

# 2. Missing ORCH_PANE_ID → error.
out=$(unset ORCH_PANE_ID; orch-subscribe %99100 2>&1 || true)
assert_contains "missing ORCH_PANE_ID errors out" "ORCH_PANE_ID not set" "$out"

# 3. Invalid pane id rejected.
out=$(ORCH_PANE_ID=%999 orch-subscribe foo 2>&1 || true)
assert_contains "invalid pane id rejected" "invalid pane id" "$out"

# 4. Self-subscription refused.
out=$(ORCH_PANE_ID=%999 orch-subscribe %999 2>&1 || true)
assert_contains "self-sub refused" "refusing to subscribe pane to itself" "$out"

# 5. --list when nothing subscribed.
out=$(ORCH_PANE_ID=%9999 orch-subscribe --list 2>&1)
assert_contains "--list empty case" "no active subscriptions" "$out"

# 6. --unsub of non-existent peer.
out=$(ORCH_PANE_ID=%9999 orch-subscribe --unsub %99001 2>&1)
assert_contains "--unsub of missing peer is graceful" "not subscribed" "$out"

#---- Daemon lifecycle ----
echo "## Daemon lifecycle"

# Use `cat` receiver: stable screen content (passes orch-tell's idle-wait)
# AND doesn't interpret injected text as shell commands.
TEST=$(tmux new-window -d -n hs-regress -P -F '#{pane_id}' 'clear; echo READY; exec cat')
sleep 0.4

FAKE="${FAKE_BASE}1"
rm -f ~/.cache/orch-stop/${FAKE}.event ~/.cache/orch-subs/${TEST}.${FAKE}.pid

# 7. Subscribe creates a pidfile with a real PID.
ORCH_PANE_ID=$TEST orch-subscribe $FAKE >/dev/null
sleep 0.2
PID_CONTENT=$(cat ~/.cache/orch-subs/${TEST}.${FAKE}.pid 2>/dev/null || echo "")
[ -n "$PID_CONTENT" ] && has_pid=yes || has_pid=no
assert "subscribe creates pidfile with content" "yes" "$has_pid"
if [ -n "$PID_CONTENT" ]; then kill -0 "$PID_CONTENT" 2>/dev/null && alive=yes || alive=no
else alive=no; fi
assert "daemon process is alive" "yes" "$alive"

# 8. Re-subscribing same peer is a no-op.
out=$(ORCH_PANE_ID=$TEST orch-subscribe $FAKE 2>&1)
assert_contains "duplicate subscribe is no-op" "already subscribed" "$out"

# 9. --list shows the active sub.
out=$(ORCH_PANE_ID=$TEST orch-subscribe --list 2>&1)
assert_contains "--list shows active" "$FAKE" "$out"

# 10. --unsub kills the daemon and removes the pidfile.
ORCH_PANE_ID=$TEST orch-subscribe --unsub $FAKE >/dev/null
sleep 0.4
[ -f ~/.cache/orch-subs/${TEST}.${FAKE}.pid ] && pidfile_exists=yes || pidfile_exists=no
assert "--unsub removes pidfile" "no" "$pidfile_exists"

#---- E2E: marker write delivers exactly one fire ----
echo "## E2E delivery"

# 11. Subscribe + write marker → exactly one send-log entry for this peer.
FAKE2="${FAKE_BASE}2"
rm -f ~/.cache/orch-stop/${FAKE2}.event
SEND_LOG=~/.cache/orch-send.log
count_fires() { grep "\\[peer event\\] $1" "$SEND_LOG" 2>/dev/null | wc -l | tr -d ' '; }
BEFORE=$(count_fires "$FAKE2")

ORCH_PANE_ID=$TEST orch-subscribe $FAKE2 >/dev/null
sleep 0.5
{
    echo "ts_ns=$(date +%s%N)"
    echo "ts_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "pane_id=$FAKE2"
    echo "cwd=/tmp/regress"
} > ~/.cache/orch-stop/${FAKE2}.event
sleep 1.8

AFTER=$(count_fires "$FAKE2")
DELTA=$((AFTER - BEFORE))
assert "single marker write fires orch-tell exactly once" "1" "$DELTA"

# 12. Second marker write (different ts_ns) fires again.
sleep 0.3
{
    echo "ts_ns=$(date +%s%N)"
    echo "ts_iso=$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "pane_id=$FAKE2"
    echo "cwd=/tmp/regress2"
} > ~/.cache/orch-stop/${FAKE2}.event
sleep 1.5
AFTER2=$(count_fires "$FAKE2")
DELTA2=$((AFTER2 - AFTER))
assert "second event with new ts_ns fires again" "1" "$DELTA2"

# 13. --cancel cleans up everything for this pane.
ORCH_PANE_ID=$TEST orch-subscribe "${FAKE_BASE}3" "${FAKE_BASE}4" >/dev/null
sleep 0.3
ORCH_PANE_ID=$TEST orch-subscribe --cancel >/dev/null
sleep 0.4
LEFTOVERS=$(ls ~/.cache/orch-subs/${TEST}.*.pid 2>/dev/null | wc -l | tr -d ' ')
assert "--cancel removes all pidfiles for self" "0" "$LEFTOVERS"

# 14. Mutual subscription is refused (would cause cascade loop).
PEER_PANE=$(tmux new-window -d -n hs-mut -P -F '#{pane_id}' 'cat')
sleep 0.3
# First, PEER subscribes to TEST.
ORCH_PANE_ID=$PEER_PANE orch-subscribe "$TEST" >/dev/null 2>&1
sleep 0.3
# Now TEST tries to sub back — should refuse with exit 2.
out=$(ORCH_PANE_ID=$TEST orch-subscribe "$PEER_PANE" 2>&1)
ec=$?
assert "mutual sub returns exit code 2" "2" "$ec"
[[ "$out" == *"mutual"* ]] && msg=yes || msg=no
assert "mutual sub error mentions 'mutual'" "yes" "$msg"
[ -f ~/.cache/orch-subs/${TEST}.${PEER_PANE}.pid ] && created=yes || created=no
assert "mutual sub does NOT create reverse daemon" "no" "$created"
ORCH_PANE_ID=$PEER_PANE orch-subscribe --cancel >/dev/null 2>&1
tmux kill-window -t hs-mut 2>/dev/null

# 15. Daemon exits when self-pane is killed (delivered via next fswatch wake).
ORCH_PANE_ID=$TEST orch-subscribe "${FAKE_BASE}5" >/dev/null
sleep 0.3
DPID=$(cat ~/.cache/orch-subs/${TEST}.${FAKE_BASE}5.pid 2>/dev/null)
tmux kill-window -t hs-regress 2>/dev/null
sleep 0.5  # let tmux drop the pane from list-panes
# Daemon's self-pane check happens at the top of each loop iteration AFTER
# fswatch wakes. Trigger fswatch by writing a marker for any peer (doesn't
# need to be one we subscribed to — fswatch wakes on any DIR change).
touch ~/.cache/orch-stop/regress-trigger.event
sleep 1.5
rm -f ~/.cache/orch-stop/regress-trigger.event
if [ -n "$DPID" ] && kill -0 "$DPID" 2>/dev/null; then alive=yes; else alive=no; fi
assert "daemon exits when self-pane disappears" "no" "$alive"

# Cleanup.
rm -f ~/.cache/orch-stop/${FAKE2}.event
rm -f ~/.cache/orch-subs/*.pid 2>/dev/null

echo
echo "================================"
echo "PASS: $PASS / $((PASS + FAIL))"
if [ $FAIL -gt 0 ]; then
    echo "FAIL: $FAIL"
    printf '       %s\n' "${FAILED_TESTS[@]}"
    exit 1
fi
echo "all green"
