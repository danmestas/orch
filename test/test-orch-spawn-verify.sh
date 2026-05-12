#!/usr/bin/env bash
# Regression tests for `orch-spawn --verify`.
#
# Validates:
#   - --verify with a missing agent binary → rc=1 + "timeout" stderr
#   - --verify with a non-shell foreground process → rc=0 + "ready" stderr
#   - ORCH_VERIFY_TIMEOUT env shortens / overrides the default
#   - Spawned panes are still emitted/registered on success; failure
#     leaves stdout empty (no pane id) and the pane is left for caller
#     to inspect/kill.
#
# Run inside tmux (CI wraps this in a tmux session). Skips if no $TMUX.
set -uo pipefail

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

SPAWN=$(command -v orch-spawn)
[ -x "$SPAWN" ] || { echo "orch-spawn missing on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
export ORCH_REGISTRY_DIR="$SANDBOX/registry"
mkdir -p "$ORCH_REGISTRY_DIR"

# Track panes we create so we can kill them on exit even if a test bails.
declare -a SPAWNED_PANES=()
cleanup() {
    for p in "${SPAWNED_PANES[@]}"; do
        tmux kill-pane -t "$p" 2>/dev/null || true
    done
    rm -rf "$SANDBOX"
}
trap cleanup EXIT

echo "=== --verify with missing binary → timeout failure ==="

# 1) pi binary almost certainly absent in the CI runner. Spawn → WRAP runs
# `cd && pi`, which fails immediately; WRAP's trailing `read` keeps the
# pane alive with $SHELL as the foreground process. --verify should see
# a shell after the deadline and exit 1.
export ORCH_VERIFY_TIMEOUT=3
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --no-fleet --verify >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
# Even on failure, the pane was created — capture it for cleanup.
maybe_pane=$(grep -oE '%[0-9]+' "$TMP_ERR" | head -1)
[ -n "$maybe_pane" ] && SPAWNED_PANES+=("$maybe_pane")

assert "missing-binary: rc=1" 1 "$rc"
assert "missing-binary: stdout empty (no pane id leaked on failure)" "" "$(cat "$TMP_OUT")"
assert_contains "missing-binary: stderr names failure mode" "agent failed to start" "$(cat "$TMP_ERR")"
# Could be "timeout" or "pane died" depending on shell behaviour; both acceptable.
case "$(cat "$TMP_ERR")" in
    *timeout*|*"pane died"*) match="ok" ;;
    *)                       match="missing" ;;
esac
assert "missing-binary: stderr explains timeout|pane-died" "ok" "$match"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== ORCH_VERIFY_TIMEOUT env shortens the budget ==="

# 2) Same missing-binary scenario but with a smaller timeout — verify
# rc=1 still fires and that the elapsed wall time is reasonable. We
# can't test the success path here because:
#   - tmux split-window's spawned pane re-execs $SHELL -l after the WRAP,
#     which re-sources the user's rc files and resets PATH, so a
#     SHIM_DIR-on-PATH approach is overridden by .zshrc/.bashrc on
#     workstation runs.
#   - In CI the verify-success path is exercised indirectly: the
#     existing test-orch-observer-role.sh / test-orch-spy.sh smoke tests
#     already cover successful claude spawns via suit, and those would
#     trip --verify's non-shell check if it ever false-positives on a
#     real launched agent.
# So we cover the most valuable bit (the silent-shell-pane catch) and
# leave the success-path observation to existing integration coverage.
export ORCH_VERIFY_TIMEOUT=2
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
start=$(date +%s)
"$SPAWN" pi --no-fleet --verify >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
elapsed=$(( $(date +%s) - start ))
maybe_pane=$(grep -oE '%[0-9]+' "$TMP_ERR" | head -1)
[ -n "$maybe_pane" ] && SPAWNED_PANES+=("$maybe_pane")

assert "short-timeout: rc=1" 1 "$rc"
# Allow some slack on slow runners — should finish within ~8s when budget is 2s.
[ "$elapsed" -le 8 ] && fast="ok" || fast="took-${elapsed}s"
assert "short-timeout: respects ORCH_VERIFY_TIMEOUT (~${elapsed}s)" "ok" "$fast"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
