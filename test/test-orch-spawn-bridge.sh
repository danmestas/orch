#!/usr/bin/env bash
# test-orch-spawn-bridge.sh — parser/contract tests for orch-spawn's
# --bridge flag introduced in #182 (Proposal 0010 Phase A).
#
# Validates:
#   - --bridge synadia-plugin and --bridge=synadia-plugin both parse
#   - --bridge shim-adapter and --bridge=shim-adapter both parse
#   - --bridge with an invalid value exits 1 with a helpful message
#   - --bridge=synadia-plugin on a non-claude agent (codex/pi/gemini)
#     exits 1 with a "claude only" message — this is the load-bearing
#     guard that preserves codex/pi/gemini behaviour unchanged.
#
# Scope: parser-only. Does not spawn real panes (no tmux required) or
# launch claude with the plugin. The end-to-end round-trip is covered by
# commit 96c4089's spike artifact recorded in
# skills/migrating-to-synadia/SKILL.md.
#
# Run with: bash test/test-orch-spawn-bridge.sh
set -uo pipefail

# Drop orch-spawn's interactive pause-on-exit tail — defensive even though
# these tests early-exit before pane creation, so a future mutation that
# does spawn won't leak zombies.
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

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

echo "Testing $SPAWN --bridge flag..."

# --- contract 1: --bridge synadia-plugin (space form) parses for claude ---
# Use a deliberately-invalid combo downstream of the parser to discriminate
# "flag accepted by parser" from "flag rejected by parser". --bridge for an
# unknown agent would hit the agent guard or the bridge-agent guard first;
# the parser-itself rejection (unknown flag) is what we're guarding against.
#
# Strategy: invoke with the flag and assert the error message is NOT the
# parser's "unknown flag: --bridge" complaint. Any downstream error (no
# tmux, missing claude binary, etc.) is acceptable — what matters is that
# the flag was recognized.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --bridge synadia-plugin --cwd /tmp --no-fleet >"$TMP_OUT" 2>"$TMP_ERR" || rc=$?
ERR=$(cat "$TMP_ERR")
if echo "$ERR" | grep -q "unknown flag: --bridge"; then
    echo "  FAIL  bridge-space-form: parser rejected --bridge"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("bridge-space-form-parser-recognition")
else
    echo "  PASS  bridge-space-form: parser accepts --bridge synadia-plugin"
    PASS=$((PASS + 1))
fi
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 2: --bridge=value form parses ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --bridge=shim-adapter --cwd /tmp --no-fleet >"$TMP_OUT" 2>"$TMP_ERR" || rc=$?
ERR=$(cat "$TMP_ERR")
if echo "$ERR" | grep -q "unknown flag: --bridge"; then
    echo "  FAIL  bridge-equals-form: parser rejected --bridge=shim-adapter"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("bridge-equals-form-parser-recognition")
else
    echo "  PASS  bridge-equals-form: parser accepts --bridge=shim-adapter"
    PASS=$((PASS + 1))
fi
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 3: --bridge with invalid value rejected at parse time ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --bridge bogus-bridge >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "bad-bridge: exits non-zero" 1 "$rc"
assert "bad-bridge: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "bad-bridge: stderr names valid values" "synadia-plugin|shim-adapter" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- contract 4: --bridge=synadia-plugin on codex/pi/gemini is rejected ---
# This is the load-bearing guard: codex/pi/gemini default to shim-adapter
# (verified by the negative path here — they MAY NOT use the plugin until
# Phase B's NATS↔ACP bridge lands). If this assertion regresses we've
# silently changed codex/pi/gemini behaviour.
for guarded_agent in codex pi gemini; do
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    "$SPAWN" "$guarded_agent" --bridge synadia-plugin --cwd /tmp >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "$guarded_agent-rejects-plugin: exits non-zero" 1 "$rc"
    assert "$guarded_agent-rejects-plugin: stdout is empty" "" "$(cat "$TMP_OUT")"
    assert_contains "$guarded_agent-rejects-plugin: stderr explains claude-only" "claude" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"
done

# --- contract 5: --bridge=shim-adapter on codex/pi/gemini parses cleanly ---
# Explicit pass on the legacy bridge for codex/pi/gemini must work — it's
# their default anyway, but operators should be able to be explicit.
for guarded_agent in codex pi gemini; do
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    "$SPAWN" "$guarded_agent" --bridge shim-adapter --cwd /tmp >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    # rc may be 0 (if tmux/agent available) or non-zero (if not in tmux);
    # either way, the failure path MUST NOT be a bridge-rejection.
    err=$(cat "$TMP_ERR")
    if echo "$err" | grep -qE "bridge.*claude|claude only"; then
        echo "  FAIL  $guarded_agent-accepts-shim: was rejected as if plugin"
        echo "        stderr: $err"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$guarded_agent-accepts-shim-adapter")
    else
        echo "  PASS  $guarded_agent-accepts-shim: not rejected by bridge guard"
        PASS=$((PASS + 1))
    fi
    rm -f "$TMP_OUT" "$TMP_ERR"
done

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
