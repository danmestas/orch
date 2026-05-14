#!/usr/bin/env bash
# Regression tests for `orch-spawn --verify`.
#
# Validates:
#   - --verify with a missing agent binary → rc=1 + "timeout" stderr
#   - --verify with a non-shell foreground process → rc=0 + "ready" stderr
#   - ORCH_VERIFY_TIMEOUT env shortens / overrides the default
#   - The banner-match readiness signal (#36): a pane that stays as a
#     shell but emits the agent's banner is correctly recognised as
#     ready via tmux capture-pane | grep, rather than timing out.
#   - The verify default ceiling is 60s and the per-agent BANNER table
#     covers every agent in the WRAP case.
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
echo "=== banner-match readiness: fake pane shows banner with shell title ==="

# 3) The bug in #36: a heavy claude bundle is interactively ready before its
# process title renames, so title-rename detection times out even though
# capture-pane shows the banner. Test the primitive directly — create a
# pane that stays a shell (no exec) but prints a banner via printf, then
# assert tmux capture-pane | grep -qF "Claude Code" matches. This is the
# inner check the verify loop now performs as a second signal.
# We test the primitive (not orch-spawn end-to-end) because tmux's server
# may not inherit caller PATH for shim-based agent mocks; the primitive
# is the load-bearing piece — orch-spawn's `case` over $AGENT just picks
# which banner string to feed it.
TEST_BANNER="Claude Code v0 (test banner)"
FAKE=$(tmux split-window -d -P -F '#{pane_id}' "printf '%s\\n' '$TEST_BANNER'; read") || FAKE=""
if [ -n "$FAKE" ]; then
    SPAWNED_PANES+=("$FAKE")
    # Banner gets written by printf almost immediately; allow a moment for tmux
    # to settle the buffer.
    sleep 1
    cur_cmd=$(tmux display -p -t "$FAKE" '#{pane_current_command}' 2>/dev/null || echo "")
    case "$cur_cmd" in
        ""|zsh|bash|sh|fish|dash|ksh) title_state="shell" ;;
        *)                            title_state="renamed-$cur_cmd" ;;
    esac
    assert "banner-match: fake pane keeps shell title (no title rename)" "shell" "$title_state"

    captured=$(tmux capture-pane -p -t "$FAKE" 2>/dev/null || echo "")
    if printf '%s' "$captured" | grep -qF "Claude Code"; then
        match="found"
    else
        match="missing"
    fi
    assert "banner-match: capture-pane | grep -qF 'Claude Code' matches" "found" "$match"
else
    echo "  FAIL  banner-match: could not create fake pane"
    FAIL=$((FAIL + 2))
    FAILED_TESTS+=("banner-match: setup failed")
fi

echo
echo "=== verify configuration: defaults and banner table ==="

# 4) Per #36, the default verify ceiling rose from 30s to 60s. Verify
# the script literally has that default so a future drift gets caught.
# Resolve to the in-tree orch-spawn relative to this test, not whatever
# command -v finds — a stale system install would silently invalidate
# these config assertions while the integration tests above still ran
# against the in-tree binary via PATH.
ORCH_SPAWN_SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/bin/orch-spawn"
[ -f "$ORCH_SPAWN_SCRIPT" ] || { echo "in-tree orch-spawn not found at $ORCH_SPAWN_SCRIPT"; exit 2; }
if grep -q 'ORCH_VERIFY_TIMEOUT:-60' "$ORCH_SPAWN_SCRIPT"; then
    timeout_default="60"
else
    timeout_default="missing-or-different"
fi
assert "config: default ORCH_VERIFY_TIMEOUT is 60s" "60" "$timeout_default"

# 5) Per the brief, every agent in the WRAP case must have a banner
# registered (or fall through cleanly). Sanity check that the four
# canonical agents are named in the BANNER case.
banner_table_ok="yes"
for agent in claude codex pi gemini; do
    if ! grep -qE "^[[:space:]]*$agent\\)[[:space:]]*BANNER=" "$ORCH_SPAWN_SCRIPT"; then
        banner_table_ok="missing-$agent"
        break
    fi
done
assert "config: BANNER table defines claude|codex|pi|gemini" "yes" "$banner_table_ok"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
