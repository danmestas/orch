#!/usr/bin/env bash
# E2E test for orch-subscribe against REAL claude harnesses.
#
# Spawns two headless claude workers, subscribes one to the other,
# triggers a real Stop event from the publisher, and verifies the
# subscriber pane received the [peer event] prompt.
#
# Slow (claude boot + response time): budget ~60s.
set -uo pipefail

PASS=0
FAIL=0
FAILED=()
A=""
B=""

cleanup() {
    [ -n "$A" ] && tmux kill-pane -t "$A" 2>/dev/null
    [ -n "$B" ] && tmux kill-pane -t "$B" 2>/dev/null
    [ -n "${A:-}" ] && rm -f ~/.cache/orch-subs/${A}.*.pid 2>/dev/null
}
trap cleanup EXIT

assert() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc (expected=$expected got=$got)"
        FAIL=$((FAIL + 1))
        FAILED+=("$desc")
    fi
}

[ -n "${TMUX:-}" ] || { echo "must run inside tmux"; exit 1; }
command -v orch-spawn   >/dev/null || { echo "orch-spawn missing"; exit 1; }
command -v orch-subscribe >/dev/null || { echo "orch-subscribe missing"; exit 1; }
command -v orch-tell    >/dev/null || { echo "orch-tell missing"; exit 1; }

echo "## spawning real claude harnesses (headless)"
A=$(orch-spawn claude --cwd /tmp --headless --no-fleet 2>/dev/null)
B=$(orch-spawn claude --cwd /tmp --headless --no-fleet 2>/dev/null)
echo "  subscriber A=$A    publisher B=$B"

echo
echo "## waiting for both claudes to settle (boot)"
# Idle-wait for both: claude shows a settled input box once ready.
wait_idle() {
    local pane=$1 deadline=$(($(date +%s) + 30)) prev="" cur=""
    while [ "$(date +%s)" -lt "$deadline" ]; do
        cur=$(tmux capture-pane -t "$pane" -p 2>/dev/null)
        if [ -n "$prev" ] && [ "$prev" = "$cur" ]; then return 0; fi
        prev=$cur
        sleep 1
    done
    return 1
}
wait_idle "$A" && echo "  $A settled" || { echo "  $A never settled"; exit 1; }
wait_idle "$B" && echo "  $B settled" || { echo "  $B never settled"; exit 1; }

echo
echo "## subscribing $A to $B's Stop events"
ORCH_PANE_ID=$A orch-subscribe "$B"
sleep 0.4

echo
echo "## verifying daemon is registered"
[ -f ~/.cache/orch-subs/${A}.${B}.pid ] && reg=yes || reg=no
assert "subscription pidfile created" "yes" "$reg"

echo
echo "## triggering real Stop on $B (send a prompt, await response)"
SEND_LOG=~/.cache/orch-send.log
BEFORE=$(grep "\\[peer event\\] $B" "$SEND_LOG" 2>/dev/null | wc -l | tr -d ' ')

orch-tell "$B" "Reply with exactly one word: pong"
echo "  prompt sent — waiting for B's Stop hook to fire (up to 60s)..."

# Poll the marker dir for B's Stop event.
deadline=$(($(date +%s) + 60))
saw_stop=no
while [ "$(date +%s)" -lt "$deadline" ]; do
    if [ -e ~/.cache/orch-stop/${B}.event ] && \
       [ "$(stat -f %m ~/.cache/orch-stop/${B}.event 2>/dev/null || stat -c %Y ~/.cache/orch-stop/${B}.event 2>/dev/null)" -gt "$(($(date +%s) - 60))" ]; then
        saw_stop=yes
        break
    fi
    sleep 1
done
assert "$B fired Stop hook within 60s" "yes" "$saw_stop"

echo
echo "## verifying daemon delivered [peer event] to $A"
# Wait briefly for the daemon's orch-tell to land (it has its own idle-wait).
sleep 5
AFTER=$(grep "\\[peer event\\] $B" "$SEND_LOG" 2>/dev/null | wc -l | tr -d ' ')
DELTA=$((AFTER - BEFORE))
assert "send-log contains exactly one [peer event] for $B" "1" "$DELTA"

echo
echo "## verifying $A's pane shows the peer event in its input or transcript"
A_CONTENT=$(tmux capture-pane -t "$A" -pS -100 2>/dev/null)
echo "$A_CONTENT" | grep -q "\\[peer event\\] $B" && saw_in_pane=yes || saw_in_pane=no
assert "[peer event] visible in $A pane content" "yes" "$saw_in_pane"

echo
echo "## triggering a SECOND Stop on $B and confirming dedup didn't suppress it"
sleep 1
orch-tell "$B" "Reply with one word: again"
deadline=$(($(date +%s) + 60))
PRE_NS=$(grep '^ts_ns=' ~/.cache/orch-stop/${B}.event 2>/dev/null | cut -d= -f2-)
saw_new_stop=no
while [ "$(date +%s)" -lt "$deadline" ]; do
    NS=$(grep '^ts_ns=' ~/.cache/orch-stop/${B}.event 2>/dev/null | cut -d= -f2-)
    if [ -n "$NS" ] && [ "$NS" != "$PRE_NS" ]; then
        saw_new_stop=yes
        break
    fi
    sleep 1
done
assert "$B fired second Stop with new ts_ns" "yes" "$saw_new_stop"

sleep 5
AFTER2=$(grep "\\[peer event\\] $B" "$SEND_LOG" 2>/dev/null | wc -l | tr -d ' ')
DELTA2=$((AFTER2 - AFTER))
assert "send-log gained exactly one new [peer event] for second Stop" "1" "$DELTA2"

echo
echo "## --cancel cleans up"
ORCH_PANE_ID=$A orch-subscribe --cancel >/dev/null
sleep 0.5
[ -f ~/.cache/orch-subs/${A}.${B}.pid ] && cancelled=no || cancelled=yes
assert "pidfile removed after --cancel" "yes" "$cancelled"

echo
echo "================================"
echo "PASS: $PASS / $((PASS + FAIL))"
if [ $FAIL -gt 0 ]; then
    echo "FAIL: $FAIL"
    printf '       %s\n' "${FAILED[@]}"
    exit 1
fi
echo "all green (real-harness E2E)"
