#!/usr/bin/env bash
# Output-contract regression tests for orch-spawn.
# Asserts that stdout carries only the canonical result (pane id) and
# all informational/error output goes to stderr — so callers can use
#   PANE=$(orch-spawn ... 2>/dev/null)
# without 2>&1 | tail -1 fragility.
#
# Run with: bash test/test-orch-spawn-output.sh
set -uo pipefail

# Drop orch-spawn's interactive pause-on-exit wrapper tail — defensive
# even though current tests early-exit before pane creation, so a future
# mutation that does spawn won't leak zombies (closes #178).
export ORCH_NO_PAUSE_ON_EXIT=1

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

SPAWN=${ORCH_SPAWN_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/helpers/orch-spawn}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

echo "Testing $SPAWN output contract..."

# --- contract 1: missing-agent error path ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "missing-agent: exits non-zero" 1 "$rc"
assert "missing-agent: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "missing-agent: stderr has usage" "usage: orch spawn" "$(cat "$TMP_ERR")"
assert_contains "missing-agent: stderr advertises --quiet" "--quiet" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 2: unknown flag error path ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --bogus-flag >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "unknown-flag: exits non-zero" 1 "$rc"
assert "unknown-flag: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "unknown-flag: stderr has flag name" "--bogus-flag" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 3: unknown agent error path ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" not-an-agent >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "unknown-agent: exits non-zero" 1 "$rc"
assert "unknown-agent: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "unknown-agent: stderr names the agent" "not-an-agent" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 4: --outfit on non-claude agent (error before any tmux/suit work) ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "outfit-on-pi: exits non-zero" 1 "$rc"
assert "outfit-on-pi: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "outfit-on-pi: stderr explains" "claude" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 5: --quiet suppresses stderr on error path ---
# Agent is positional and must come first; --quiet is parsed during the loop
# AFTER positional. So the orch-spawn call shape is:
#   orch-spawn <agent> --quiet [other flags...]
# After --quiet is parsed, exec 2>/dev/null silences subsequent error output.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --quiet --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "quiet-error: exits non-zero" 1 "$rc"
assert "quiet-error: stdout is empty" "" "$(cat "$TMP_OUT")"
assert "quiet-error: stderr is empty" "" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 6: --position rejects invalid values at parse time ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --position sideways >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "bad-position: exits non-zero" 1 "$rc"
assert "bad-position: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "bad-position: stderr names valid values" "right|left|above|below" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 7: --position with valid value parses cleanly (downstream agent-validation still fires) ---
# We pass a valid position with an invalid combo (--outfit on pi). If the parser
# accepts --position, the failure point is the outfit-on-pi guard, not parse.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --position above --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "good-position: exits non-zero (caught by outfit-on-pi)" 1 "$rc"
assert_contains "good-position: stderr is the outfit-on-pi message, not a position error" "claude" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
