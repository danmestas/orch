#!/usr/bin/env bash
#
# Regression test for orch-spawn pane-source bug (#38).
#
# Validates that orch-spawn splits off the *invoker's* pane (the pane the
# orch-spawn process is running in), not the active tmux client's focused
# pane. Distinguishing the two matters whenever the user clicks into another
# pane between two of the invoking process's commands, or when orch-spawn is
# invoked from inside a tool subshell (e.g. Claude Code's Bash tool) where
# focus state can shift unobserved.
#
# Run inside tmux (CI wraps this in a tmux session). Skips if no $TMUX.

set -uo pipefail

# Drop orch-spawn's interactive pause-on-exit wrapper tail so the
# `orch-spawn pi --cwd /tmp` invocation below closes its pane cleanly
# when pi is absent on the runner (closes #178).
export ORCH_NO_PAUSE_ON_EXIT=1

[ -n "${TMUX:-}" ] || { echo "skip: must run inside tmux"; exit 0; }

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

SPAWN=$(command -v orch-spawn)
[ -x "$SPAWN" ] || { echo "orch-spawn missing on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
export ORCH_REGISTRY_DIR="$SANDBOX/registry"
mkdir -p "$ORCH_REGISTRY_DIR"

# Track every pane we create so we can kill them even if a test bails.
declare -a SPAWNED_PANES=()
cleanup() {
    for p in "${SPAWNED_PANES[@]}"; do
        tmux kill-pane -t "$p" 2>/dev/null || true
    done
}
trap cleanup EXIT

# --- contract: split source is the invoker's pane, not the focused pane ---
#
# Setup: this script is running in pane A (the invoker). Create a sibling
# pane B by splitting A horizontally. Then move the *focus* to B (simulating
# the user clicking into another pane mid-flow). Invoke orch-spawn from this
# same bash process (still inside A).
#
# Discriminator: compare the new pane's x-coordinate to B's. With the fix,
# the new pane lands between A and B (orch-spawn splits A). With the bug,
# the new pane lands to B's right (orch-spawn splits B). Width-shrink isn't
# reliable on its own — tmux rebalances neighbor widths in either case.

A=$TMUX_PANE

# Create B as a right-half split of A.
B=$(tmux split-window -h -d -t "$A" -P -F '#{pane_id}' "sleep 600")
SPAWNED_PANES+=("$B")
B_LEFT=$(tmux display -p -t "$B" '#{pane_left}')

# Drift focus to B. orch-spawn must NOT be misled by this.
tmux select-pane -t "$B"

# Spawn. The agent binary (`pi`) is probably absent on CI; that's fine —
# the WRAP's trailing `read` keeps the pane alive after the missing-binary
# failure, so we have something to inspect for position.
NEW=$(orch-spawn pi --cwd /tmp --position right --quiet 2>/dev/null || true)
[ -n "$NEW" ] && SPAWNED_PANES+=("$NEW")

# Restore focus so subsequent tests behave predictably.
tmux select-pane -t "$A"

if [ -z "$NEW" ]; then
    echo "  FAIL  split-source test: orch-spawn produced no pane id"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("split-source produced no pane id")
else
    NEW_LEFT=$(tmux display -p -t "$NEW" '#{pane_left}' 2>/dev/null || echo "")
    if [ -z "$NEW_LEFT" ]; then
        echo "  FAIL  split-source test: cannot read pane_left for $NEW"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("split-source pane_left missing")
    else
        # Fix: NEW sits between A and B → NEW_LEFT < B_LEFT.
        # Bug: NEW sits to B's right → NEW_LEFT >= B_LEFT.
        verdict="splits-focused-pane-bug"
        [ "$NEW_LEFT" -lt "$B_LEFT" ] && verdict="splits-invoker-pane-correct"
        assert "orch-spawn split source is invoker, not focused" \
            "splits-invoker-pane-correct" "$verdict"
    fi
fi

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
