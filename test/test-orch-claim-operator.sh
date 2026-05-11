#!/usr/bin/env bash
# Regression tests for orch-claim-operator + the operator-row in orch-peek.
# Requires: tmux session, jq, a Claude Code transcript JSONL for the current
# cwd (this script is intended to run inside a Claude Code session for the
# orch project — same pattern as test-orch-peek.sh).
#
# Run with: bash test/test-orch-claim-operator.sh
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

CLAIM=${ORCH_CLAIM_OPERATOR_BIN:-$(command -v orch-claim-operator || echo "")}
PEEK=${ORCH_PEEK_BIN:-$(command -v orch-peek || echo "")}
[ -x "$CLAIM" ] || { echo "orch-claim-operator not on PATH (set ORCH_CLAIM_OPERATOR_BIN)"; exit 2; }
[ -x "$PEEK" ]  || { echo "orch-peek not on PATH (set ORCH_PEEK_BIN)"; exit 2; }

# Sandbox the cache so we don't pollute ~/.cache/orch-operator.json.
SANDBOX=$(mktemp -d)
TMP_CACHE=$SANDBOX/operator.json
export ORCH_OPERATOR_CACHE=$TMP_CACHE
trap 'rm -rf "$SANDBOX"' EXIT

PANE=$(tmux display -p '#{pane_id}' 2>/dev/null) || { echo "must run inside tmux"; exit 2; }

echo "Testing $CLAIM (sandbox cache: $TMP_CACHE)"

# --- contract 1: happy path on current pane ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$CLAIM" --pane "$PANE" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "claim: exits zero on happy path" 0 "$rc"
assert "claim: stdout is the cache path" "$TMP_CACHE" "$(cat "$TMP_OUT")"
[ -f "$TMP_CACHE" ] && exists="exists" || exists="missing"
assert "claim: cache file written" "exists" "$exists"
PCT=$(jq -r .pane_id "$TMP_CACHE" 2>/dev/null || echo "?")
assert "claim: pane_id matches" "$PANE" "$PCT"
JSONL=$(jq -r .transcript_jsonl "$TMP_CACHE" 2>/dev/null || echo "")
[ -f "$JSONL" ] && j_exists="exists" || j_exists="missing"
assert "claim: transcript_jsonl is readable" "exists" "$j_exists"
assert_contains "claim: cache has cwd field" '"cwd"' "$(cat "$TMP_CACHE")"
assert_contains "claim: cache has claimed_at_ts_ns" '"claimed_at_ts_ns"' "$(cat "$TMP_CACHE")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 2: ORCH_PANE_ID set blocks claim (worker pane) ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_PANE_ID=%99 "$CLAIM" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "claim worker-pane: exits non-zero" 1 "$rc"
assert "claim worker-pane: stdout empty" "" "$(cat "$TMP_OUT")"
assert_contains "claim worker-pane: stderr names ORCH_PANE_ID" "ORCH_PANE_ID" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 3: --pane bypasses ORCH_PANE_ID guard ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_PANE_ID=%99 "$CLAIM" --pane "$PANE" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "claim --pane override: exits zero" 0 "$rc"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 4: invalid pane id rejected ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$CLAIM" --pane "not-a-pane" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "claim invalid-pane: exits non-zero" 1 "$rc"
assert "claim invalid-pane: stdout empty" "" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 5: --quiet suppresses stderr on error path ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$CLAIM" --quiet --pane "not-a-pane" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "claim --quiet error: exits non-zero" 1 "$rc"
assert "claim --quiet error: stdout empty" "" "$(cat "$TMP_OUT")"
assert "claim --quiet error: stderr empty" "" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 6: orch-peek surfaces the operator row ---
"$CLAIM" --pane "$PANE" >/dev/null 2>&1
PEEK_OUT=$("$PEEK" --json 2>/dev/null)
OP_ROW_COUNT=$(printf '%s' "$PEEK_OUT" | jq --arg p "$PANE" '[.[] | select(.agent=="operator" and .pane_id==$p)] | length' 2>/dev/null || echo 0)
assert "peek --json: exactly one operator row for current pane" 1 "$OP_ROW_COUNT"

# --- contract 7: peek with explicit panes does NOT auto-include operator ---
# (current implementation: only the requested pane appears)
# Pick a different live pane id (any other registry entry, or the current —
# with current we'd still get it since --panes restricts to it).
# Here we just verify the operator agent label is absent when the operator
# pane is NOT among the explicit panes.
OTHER_PANE=$(tmux list-panes -a -F '#{pane_id}' 2>/dev/null | grep -v "^$PANE\$" | head -1 || true)
if [ -n "$OTHER_PANE" ]; then
    PEEK_OUT2=$("$PEEK" --json "$OTHER_PANE" 2>/dev/null || echo "[]")
    case "$PEEK_OUT2" in
        *'"agent":"operator"'*) op_present="present" ;;
        *) op_present="absent" ;;
    esac
    assert "peek --panes <other>: operator row not auto-included" "absent" "$op_present"
fi

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
