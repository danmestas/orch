#!/usr/bin/env bash
# Regression tests for orch-peek.
# Run with: bash test/test-orch-peek.sh
# Requires tmux session + at least one live worker in the registry.
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

assert_ge() {
    local desc=$1 floor=$2 got=$3
    if [ "$got" -ge "$floor" ] 2>/dev/null; then
        echo "  PASS  $desc  ($got >= $floor)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc  ($got < $floor)"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

assert_le() {
    local desc=$1 ceiling=$2 got=$3
    if [ "$got" -le "$ceiling" ] 2>/dev/null; then
        echo "  PASS  $desc  ($got <= $ceiling)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc  ($got > $ceiling)"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

[ -n "${TMUX:-}" ] || { echo "must run inside tmux"; exit 1; }
command -v orch-peek >/dev/null || { echo "orch-peek not on PATH"; exit 1; }
command -v jq           >/dev/null || { echo "jq not on PATH"; exit 1; }

# Need at least one live worker for the meaningful assertions.
LIVE_DEFAULT=$(orch-peek 2>/dev/null | wc -l | tr -d ' ')
if [ "$LIVE_DEFAULT" -lt 1 ]; then
    echo "skipping: no live workers in registry — start a orch-spawn'd worker and re-run"
    exit 0
fi

#---- 1. Default invocation returns lines for live workers.
echo "## Default invocation"
out=$(orch-peek 2>/dev/null)
lines=$(printf '%s\n' "$out" | grep -c '^%' || true)
assert_ge "default returns at least one '%pane' line" 1 "$lines"

# Output rows mention 'events' and 'said:' and 'tool:' (the format contract).
assert_contains "row has 'events' label"     "events"  "$out"
assert_contains "row has 'said:' label"      "said:"   "$out"
assert_contains "row has 'tool:' label"      "tool:"   "$out"

#---- 2. --json returns valid JSON array.
echo
echo "## --json"
json=$(orch-peek --json 2>/dev/null)
echo "$json" | jq -e 'type == "array"' >/dev/null
assert "--json output is a JSON array" "0" "$?"

# Each element must have the documented field set.
mismatch=$(echo "$json" | jq -e 'all(. | has("pane_id") and has("agent") and has("bucket") and has("events") and has("said") and has("tool") and has("age_seconds"))' 2>/dev/null)
assert "every JSON object has the 7 documented fields" "true" "$mismatch"

#---- 3. --since 1s ⊆ --since 1h (subset by count).
echo
echo "## --since"
sub=$(orch-peek --since 1s --json 2>/dev/null | jq 'length')
sup=$(orch-peek --since 1h --json 2>/dev/null | jq 'length')
assert_le "--since 1s count <= --since 1h count" "$sup" "$sub"

#---- 4. Bad pane id errors to stderr with non-zero exit.
echo
echo "## Bad pane id"
err=$(orch-peek notapane 2>&1 1>/dev/null || true)
ec=$(orch-peek notapane >/dev/null 2>&1; echo $?)
assert_contains "bad pane id mentions 'invalid pane id'" "invalid pane id" "$err"
assert_ge "bad pane id exits non-zero" 1 "$ec"

#---- 5. Missing-pane warning goes to stderr (not stdout).
echo
echo "## Missing-pane warning routing"
# Pick a registry pane id that's guaranteed not live: %999999 (huge unused number).
warn_stdout=$(orch-peek %999999 2>/dev/null || true)
warn_stderr=$(orch-peek %999999 2>&1 1>/dev/null || true)
assert "missing-pane stdout is empty"          ""                                                "$warn_stdout"
assert_contains "missing-pane stderr names the pane and 'not in registry'" "%999999 not in registry" "$warn_stderr"

#---- 6. --all returns >= default count (dead-pane entries are additive).
echo
echo "## --all"
all_count=$(orch-peek --all --json 2>/dev/null | jq 'length')
def_count=$(orch-peek --json 2>/dev/null | jq 'length')
assert_ge "--all count >= default count" "$def_count" "$all_count"

echo
echo "================================"
echo "PASS: $PASS / $((PASS + FAIL))"
if [ $FAIL -gt 0 ]; then
    echo "FAIL: $FAIL"
    printf '       %s\n' "${FAILED_TESTS[@]}"
    exit 1
fi
echo "all green"
