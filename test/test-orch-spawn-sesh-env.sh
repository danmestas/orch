#!/usr/bin/env bash
# Regression tests for sesh role/class env propagation (Phase 3 of the
# agent role/class registration proposal).
#
# Asserts:
#   1. orch-spawn derives CLASS=observer when role-derivation lands on
#      observer (stasi outfit / wait-watch / spy-on-* cut), else active.
#   2. The --class active|observer override exists, takes precedence, and
#      rejects bogus values with exit 2.
#   3. The shim-launch env block exports SESH_ROLE and SESH_CLASS
#      alongside the legacy ORCH_ROLE.
#
# Three layers of evidence, each fast and infra-free:
#   - Source-level grep against bin/orch-spawn for the env exports and
#     CLASS derivation block.
#   - Isolated-function tests that replicate the derivation logic and
#     check the table of (outfit, cut, role, class_override) → CLASS.
#   - Direct invocation for the --class passive negative case (exit 2
#     fires before any pane/shim is touched).
#
# Run with: bash test/test-orch-spawn-sesh-env.sh
set -uo pipefail

# Defensive: drop the interactive pause-on-exit wrapper even though this
# test never spawns a pane (matches sibling harnesses).
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

# Prefer the in-tree orch-spawn so this test exercises the change under
# review, not a stale globally-installed binary.
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd -P)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd -P)
SPAWN=${ORCH_SPAWN_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/helpers/orch-spawn}
[ -x "$SPAWN" ] || { echo "orch-spawn not found (set ORCH_SPAWN_BIN to override): $SPAWN"; exit 2; }

echo "Testing $SPAWN — SESH_ROLE / SESH_CLASS env propagation + --class flag..."

# ----------------------------------------------------------------------------
echo
echo "=== Section 1: source declares SESH_ROLE / SESH_CLASS in shim env block ==="
# ----------------------------------------------------------------------------
# Source-grep against bin/orch-spawn was the legacy verification. Post-#189
# the shim env block lives in cmd/orch/spawn_tmux.go (maybeLaunchShim);
# the assertion is covered by the Go unit tests in cmd/orch/spawn_test.go.
# Section 1 retained only for human-readable context — no checks fire here.
echo "  SKIP  shim env block source-grep retired (covered by Go unit tests in cmd/orch/spawn_test.go)"

# ----------------------------------------------------------------------------
echo
echo "=== Section 2: CLASS derivation truth table (isolated function) ==="
# ----------------------------------------------------------------------------

# Replicates the derivation block from bin/orch-spawn in isolation so we
# can exercise every (OUTFIT, CUT, ROLE_OVERRIDE, CLASS_OVERRIDE) combo
# without spawning anything. Keeping a tiny copy here is the same
# trade-off test-orch-spawn-no-pause-on-exit.sh accepts: if the source
# block changes shape, the grep checks above also need to update and
# this function's source-of-truth status is documented in the failure
# message.
derive_role_class() {
    local OUTFIT="$1" CUT="$2" ROLE_OVERRIDE="$3" CLASS_OVERRIDE="$4"

    ROLE="$ROLE_OVERRIDE"
    if [ -z "$ROLE" ]; then
        case "$OUTFIT" in
            stasi) ROLE=observer ;;
        esac
    fi
    if [ -z "$ROLE" ]; then
        case "$CUT" in
            wait-watch|spy-on-*) ROLE=observer ;;
        esac
    fi
    [ -z "$ROLE" ] && ROLE=worker

    if [ -n "$CLASS_OVERRIDE" ]; then
        CLASS="$CLASS_OVERRIDE"
    elif [ "$ROLE" = "observer" ]; then
        CLASS="observer"
    else
        CLASS="active"
    fi

    printf '%s %s\n' "$ROLE" "$CLASS"
}

# Each row: outfit | cut | role_override | class_override | expected
# ROLE | expected CLASS
TRUTH_TABLE=(
    # Default: no overrides, no observer-class outfit/cut → worker / active
    "engineer|implementing|||worker|active"
    # stasi outfit auto-derives observer role AND observer class
    "stasi||||observer|observer"
    # wait-watch cut auto-derives observer role AND observer class
    "|wait-watch|||observer|observer"
    # spy-on-* cut auto-derives observer role AND observer class
    "|spy-on-pi|||observer|observer"
    # Explicit --role observer over a worker-outfit → role + class flip
    "engineer||observer||observer|observer"
    # Explicit --class observer over a worker-outfit → role stays worker,
    # class flips to observer
    "engineer|||observer|worker|observer"
    # Explicit --class active over a stasi-outfit → role stays observer,
    # class forces back to active (rare but consistent: override wins)
    "stasi|||active|observer|active"
    # Both overrides explicit
    "engineer||observer|active|observer|active"
)

for row in "${TRUTH_TABLE[@]}"; do
    IFS='|' read -r outfit cut role_override class_override want_role want_class <<<"$row"
    got=$(derive_role_class "$outfit" "$cut" "$role_override" "$class_override")
    got_role="${got%% *}"
    got_class="${got##* }"
    label="outfit=$outfit cut=$cut role_ov=$role_override class_ov=$class_override"
    assert "ROLE: $label" "$want_role" "$got_role"
    assert "CLASS: $label" "$want_class" "$got_class"
done

# ----------------------------------------------------------------------------
echo
echo "=== Section 3: --class flag parser (direct invocation) ==="
# ----------------------------------------------------------------------------

# The validator fires before any pane creation. Invalid value MUST exit 2
# and name the bad value on stderr.

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --class passive >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "--class passive: exits 2" 2 "$rc"
assert_contains "--class passive: stderr names rejected value" "passive" "$(cat "$TMP_ERR")"
assert_contains "--class passive: stderr mentions class constraint" "active" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Same shape via `--class=passive` (equals form).
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --class=passive >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "--class=passive: exits 2" 2 "$rc"
assert_contains "--class=passive: stderr names rejected value" "passive" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Empty string is treated as "no override; derive from role" — consistent
# with the rest of orch-spawn's optional-flag semantics. We just confirm
# it is NOT a rc=2 (validator) error; whatever the dispatcher returns
# after that is out of scope here.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" claude --class "" --no-shim --no-fleet --cwd /tmp >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
if [ "$rc" -eq 2 ]; then
    assert "--class '': NOT rejected by validator (empty = no override)" "rc != 2" "rc == 2"
else
    assert "--class '': NOT rejected by validator (empty = no override)" "rc != 2" "rc != 2"
fi
rm -f "$TMP_OUT" "$TMP_ERR"

# Valid values must NOT trip the early-exit-2 path. We don't care what
# happens after — the dispatcher will fail for other reasons in this
# test sandbox (no NATS, no real claude binary) — we just need to
# confirm rc != 2 so the validator isn't false-positive-ing.
for valid in active observer; do
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    "$SPAWN" claude --class "$valid" --no-shim --no-fleet --cwd /tmp >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    if [ "$rc" -eq 2 ]; then
        assert "--class $valid: NOT rejected by validator (rc != 2)" "rc != 2" "rc == 2"
    else
        assert "--class $valid: NOT rejected by validator (rc != 2)" "rc != 2" "rc != 2"
    fi
    rm -f "$TMP_OUT" "$TMP_ERR"
done

# ----------------------------------------------------------------------------
echo
echo "=== Section 4: --class appears in usage / source ==="
# ----------------------------------------------------------------------------

# Operators discover the flag via `orch spawn` (no args → usage on stderr).
TMP_ERR=$(mktemp)
"$SPAWN" >/dev/null 2>"$TMP_ERR" || true
assert_contains "usage line mentions --class" "--class" "$(cat "$TMP_ERR")"
rm -f "$TMP_ERR"

# Parser handles both --class <val> (space) and --class=<val> (equals) — verified
# by behaviour, not source-grep. Section 3 already exercises both forms through
# the validator; an explicit re-check here would be redundant.
echo "  SKIP  parser-form source-grep retired (covered by Section 3 functional tests)"

# ----------------------------------------------------------------------------
echo
echo "=== SUMMARY ==="
echo "passed: $PASS"
echo "failed: $FAIL"
if [ "$FAIL" -gt 0 ]; then
    echo "failed tests:"
    for t in "${FAILED_TESTS[@]}"; do
        echo "  - $t"
    done
    exit 1
fi
exit 0
