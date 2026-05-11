#!/usr/bin/env bash
# Manual integration tests for the `suit prepare` + orch-spawn recipe.
# Goal: surface any composition gotcha BEFORE modifying orch-spawn.
#
# Tests:
#  T1. Headed worker: bundle + ORCH_PANE_ID + fleet doctrine + Stop hook + orch-tell round-trip
#  T2. Headless worker: same shape, in orch-headless session
#  T3. Cleanup pattern: trap-based bundle removal on pane death
#  T4. Two parallel bundled workers; orchestrator listener sees both Stops
set -uo pipefail

PROJ=$(mktemp -d -t suit-int.XXXXXX)
echo "package main" > "$PROJ/main.go"
echo "// real project" > "$PROJ/README.md"
# Marker for T1d: assert worker reads project's CLAUDE.md (cwd-inversion).
cat > "$PROJ/CLAUDE.md" <<'EOF'
# project marker
The secret word from this project's CLAUDE.md is "trapezoid". Remember it.
EOF
echo "test project: $PROJ"

# Build a merged appended system prompt: bundle's CLAUDE.md + fleet doctrine.
# Mirrors what orch-spawn does for cwd-inversion (project is cwd, bundle is
# --add-dir, and --add-dir does NOT auto-load <dir>/CLAUDE.md).
build_merged() {
    # Split the locals: under `set -u` a single `local bundle=$1 out="$bundle/..."`
    # references $bundle before the first assignment commits, which trips
    # "unbound variable". Two statements is the minimal fix.
    local bundle=$1
    local out="$bundle/.test-merged-prompt.md"
    : > "$out"
    [ -f "$bundle/CLAUDE.md" ] && cat "$bundle/CLAUDE.md" >> "$out"
    if [ -f ~/.cache/orch-fleet-prompt.md ]; then
        [ -s "$out" ] && printf '\n\n' >> "$out"
        cat ~/.cache/orch-fleet-prompt.md >> "$out"
    fi
    echo "$out"
}

BUNDLES=()
PANES=()
cleanup() {
    for p in "${PANES[@]}"; do tmux kill-pane -t "$p" 2>/dev/null; done
    for b in "${BUNDLES[@]}"; do rm -rf "$b" 2>/dev/null; done
    rm -rf "$PROJ"
}
trap cleanup EXIT

PASS=0; FAIL=0; FAILED=()
check() {
    local desc=$1 want=$2 got=$3
    if [ "$want" = "$got" ]; then
        echo "  PASS  $desc"
        PASS=$((PASS+1))
    else
        echo "  FAIL  $desc (want=$want got=$got)"
        FAIL=$((FAIL+1))
        FAILED+=("$desc")
    fi
}

contains() {
    local desc=$1 substr=$2 hay=$3
    if echo "$hay" | grep -iqE "$substr"; then
        echo "  PASS  $desc"
        PASS=$((PASS+1))
    else
        echo "  FAIL  $desc (no match for: $substr)"
        FAIL=$((FAIL+1))
        FAILED+=("$desc")
    fi
}

wait_for_stop() {
    local pane=$1 timeout=${2:-90}
    local marker=~/.cache/orch-stop/${pane}.event
    local pre=""
    [ -e "$marker" ] && pre=$(grep '^ts_ns=' "$marker" 2>/dev/null | cut -d= -f2-)
    local d=$(($(date +%s) + timeout))
    while [ "$(date +%s)" -lt "$d" ]; do
        local cur=$(grep '^ts_ns=' "$marker" 2>/dev/null | cut -d= -f2-)
        [ -n "$cur" ] && [ "$cur" != "$pre" ] && return 0
        sleep 1
    done
    return 1
}

ask() {
    local pane=$1 prompt=$2 timeout=${3:-90}
    orch-tell "$pane" "$prompt" >/dev/null
    wait_for_stop "$pane" "$timeout"
    sleep 2  # let response render
}

#==============================================================
# T1 — Headed worker: bundle + ORCH_PANE_ID + fleet + Stop + tell
#==============================================================
echo
echo "## T1 — headed worker, full stack"
B1=$(suit prepare --outfit engineer --cut focused --target claude-code 2>&1 | tail -1)
BUNDLES+=("$B1")
echo "  bundle: $B1"

CUR=$(tmux display -p '#{pane_id}')
M1=$(build_merged "$B1")
WRAP1="export ORCH_PANE_ID=\$TMUX_PANE; cd '$PROJ' && claude --dangerously-skip-permissions --add-dir '$B1' --append-system-prompt-file '$M1'; read"
W1=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR" "$WRAP1")
PANES+=("$W1")
echo "  pane: $W1"

# Wait for boot.
sleep 12

# T1a: hook fires for this worker
ask "$W1" "Reply with one word: ready" 60
[ -e ~/.cache/orch-stop/${W1}.event ] && hook_fired=yes || hook_fired=no
check "T1a — Stop hook fires for bundled worker" yes "$hook_fired"

# T1b: worker has outfit context
ask "$W1" "What outfit are you wearing? Reply with one word." 60
RESP1=$(tmux capture-pane -t "$W1" -p)
contains "T1b — worker reports engineer outfit" "engineer" "$RESP1"

# T1c: worker has fleet doctrine awareness (knows about ORCH_PANE_ID)
ask "$W1" "What is your tmux pane id? Read the env var ORCH_PANE_ID and tell me." 60
RESP1c=$(tmux capture-pane -t "$W1" -p)
contains "T1c — worker has ORCH_PANE_ID via fleet doctrine" "$W1" "$RESP1c"

# T1d: cwd-inversion — worker reads project's CLAUDE.md from cwd.
# Pre-fix: bundle was cwd, project was --add-dir, project CLAUDE.md never
# loaded. Post-fix: project is cwd, project CLAUDE.md auto-loads.
ask "$W1" "What is the secret word in this project's CLAUDE.md? Reply with one word." 60
RESP1d=$(tmux capture-pane -t "$W1" -p)
contains "T1d — worker reads project's CLAUDE.md (cwd-inversion fix)" "trapezoid" "$RESP1d"

# T1e: registry — was the worker auto-registered? (it shouldn't be, since we
# didn't call orch-register; we'll see if we want to add this to the recipe)
[ -f ~/.cache/orch-registry/${W1}.json ] && reg=yes || reg=no
echo "  observation T1e — registry entry auto-created: $reg (note: depends on Stop hook lazy-registration)"

#==============================================================
# T2 — Headless worker
#==============================================================
echo
echo "## T2 — headless worker (in orch-headless tmux session)"
B2=$(suit prepare --outfit backend --cut focused --target claude-code 2>&1 | tail -1)
BUNDLES+=("$B2")
echo "  bundle: $B2"

M2=$(build_merged "$B2")
WRAP2="export ORCH_PANE_ID=\$TMUX_PANE; cd '$PROJ' && claude --dangerously-skip-permissions --add-dir '$B2' --append-system-prompt-file '$M2'; read"
HDLESS_SESSION=orch-headless
if tmux has-session -t "$HDLESS_SESSION" 2>/dev/null; then
    W2=$(tmux new-window -d -t "$HDLESS_SESSION:" -n "headless-test" -P -F '#{pane_id}' "$WRAP2")
else
    W2=$(tmux new-session -d -s "$HDLESS_SESSION" -n "headless-test" -P -F '#{pane_id}' "$WRAP2")
fi
PANES+=("$W2")
echo "  headless pane: $W2"

sleep 12

# T2a: Stop hook fires from headless
ask "$W2" "Reply with one word: ready" 60
[ -e ~/.cache/orch-stop/${W2}.event ] && hook_h=yes || hook_h=no
check "T2a — Stop hook fires for headless bundled worker" yes "$hook_h"

# T2b: outfit
ask "$W2" "What outfit are you wearing? Reply with one word." 60
RESP2=$(tmux capture-pane -t "$W2" -p)
contains "T2b — headless worker reports backend outfit" "backend" "$RESP2"

#==============================================================
# T3 — Cleanup pattern: does trap-on-exit remove bundle?
#==============================================================
echo
echo "## T3 — cleanup-on-pane-death pattern"
B3=$(suit prepare --outfit engineer --target claude-code 2>&1 | tail -1)
BUNDLES+=("$B3")
echo "  bundle: $B3"

# Trap-based wrap: when claude exits and `read` returns, rm -rf the bundle.
M3=$(build_merged "$B3")
WRAP3="trap 'rm -rf $B3' EXIT; export ORCH_PANE_ID=\$TMUX_PANE; cd '$PROJ' && claude --dangerously-skip-permissions --add-dir '$B3' --append-system-prompt-file '$M3'; read"
W3=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR" "$WRAP3")
PANES+=("$W3")
sleep 8

[ -d "$B3" ] && pre_kill=yes || pre_kill=no
check "T3a — bundle exists while pane alive" yes "$pre_kill"

# Kill the pane — the wrap's trap should fire and rm -rf the bundle.
tmux kill-pane -t "$W3"
sleep 2

[ -d "$B3" ] && post_kill=yes || post_kill=no
check "T3b — bundle gone after pane killed (trap-based cleanup)" no "$post_kill"

#==============================================================
# T4 — Parallel orchestration
#==============================================================
echo
echo "## T4 — parallel orchestration of two bundled workers"
B4a=$(suit prepare --outfit engineer --target claude-code 2>&1 | tail -1)
B4b=$(suit prepare --outfit backend --target claude-code 2>&1 | tail -1)
BUNDLES+=("$B4a" "$B4b")

M4a=$(build_merged "$B4a")
M4b=$(build_merged "$B4b")
WRAP4a="export ORCH_PANE_ID=\$TMUX_PANE; cd '$PROJ' && claude --dangerously-skip-permissions --add-dir '$B4a' --append-system-prompt-file '$M4a'; read"
WRAP4b="export ORCH_PANE_ID=\$TMUX_PANE; cd '$PROJ' && claude --dangerously-skip-permissions --add-dir '$B4b' --append-system-prompt-file '$M4b'; read"

W4a=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR" "$WRAP4a")
W4b=$(tmux split-window -d -h -P -F '#{pane_id}' -t "$CUR" "$WRAP4b")
PANES+=("$W4a" "$W4b")
sleep 12

# Send prompts to both, wait for both Stops in parallel.
orch-tell "$W4a" "Reply with two words: outfit name." >/dev/null
orch-tell "$W4b" "Reply with two words: outfit name." >/dev/null

deadline=$(($(date +%s) + 60))
seen_a=no; seen_b=no
A_PRE=""; B_PRE=""
[ -e ~/.cache/orch-stop/${W4a}.event ] && A_PRE=$(grep '^ts_ns=' ~/.cache/orch-stop/${W4a}.event | cut -d= -f2-)
[ -e ~/.cache/orch-stop/${W4b}.event ] && B_PRE=$(grep '^ts_ns=' ~/.cache/orch-stop/${W4b}.event | cut -d= -f2-)
while [ "$(date +%s)" -lt "$deadline" ]; do
    [ "$seen_a" = no ] && {
        cur=$(grep '^ts_ns=' ~/.cache/orch-stop/${W4a}.event 2>/dev/null | cut -d= -f2-)
        [ -n "$cur" ] && [ "$cur" != "$A_PRE" ] && seen_a=yes
    }
    [ "$seen_b" = no ] && {
        cur=$(grep '^ts_ns=' ~/.cache/orch-stop/${W4b}.event 2>/dev/null | cut -d= -f2-)
        [ -n "$cur" ] && [ "$cur" != "$B_PRE" ] && seen_b=yes
    }
    [ "$seen_a" = yes ] && [ "$seen_b" = yes ] && break
    sleep 1
done

check "T4a — Stop fired for parallel worker A" yes "$seen_a"
check "T4b — Stop fired for parallel worker B" yes "$seen_b"

R4a=$(tmux capture-pane -t $W4a -p)
R4b=$(tmux capture-pane -t $W4b -p)
contains "T4c — A reports engineer" "engineer" "$R4a"
contains "T4d — B reports backend" "backend" "$R4b"

#==============================================================
# Summary
#==============================================================
echo
echo "================================"
echo "PASS: $PASS / $((PASS+FAIL))"
if [ $FAIL -gt 0 ]; then
    echo "FAIL: $FAIL"
    printf '       %s\n' "${FAILED[@]}"
    exit 1
fi
echo "all green"
