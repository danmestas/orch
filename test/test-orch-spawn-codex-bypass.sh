#!/usr/bin/env bash
#
# Regression test for orch-spawn's codex first-run-prompt bypass (#37).
#
# Codex prompts on every new directory for trust ("Do you trust the
# contents of this directory?") and once for migrate-from-claude. The
# trust prompt is per-directory and is the recurring pain point. Both
# bypasses are injected inline in orch-spawn's codex WRAP via `-c` and
# `--enable`, with no global mutation of ~/.codex/config.toml.
#
# This test asserts the bypass flags are present in the script. Full
# integration would require codex installed in CI which isn't the case;
# the bypass mechanism itself was verified manually by state-diffing a
# fresh codex install.

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

ORCH_SPAWN_SCRIPT="$(cd "$(dirname "$0")/.." && pwd)/bin/orch-spawn"
[ -f "$ORCH_SPAWN_SCRIPT" ] || { echo "in-tree orch-spawn not found at $ORCH_SPAWN_SCRIPT"; exit 2; }

echo "=== codex WRAP injects trust-level override ==="

# Per #37: codex persists per-directory trust under [projects."<path>"].
# The WRAP must pass -c with that key shape so codex skips the prompt
# for the spawn's cwd without writing to the persisted config.
if grep -qE '^[[:space:]]*WRAP=.*codex.*-c.*projects\.\\".*\\".trust_level=\\"trusted\\"' "$ORCH_SPAWN_SCRIPT"; then
    trust_flag="present"
else
    trust_flag="missing"
fi
assert "codex WRAP: -c trust_level override present" "present" "$trust_flag"

echo
echo "=== codex WRAP enables external_migration feature ==="

# Per codex's feature system, migrate-from-claude is gated by
# features.external_migration. --enable is sugar for -c features.<name>=true.
if grep -qE '^[[:space:]]*WRAP=.*codex.*--enable[[:space:]]+external_migration' "$ORCH_SPAWN_SCRIPT"; then
    migration_flag="present"
else
    migration_flag="missing"
fi
assert "codex WRAP: --enable external_migration present" "present" "$migration_flag"

echo
echo "=== codex WRAP still passes --dangerously-bypass-approvals-and-sandbox ==="

# Sanity: the existing approval-bypass flag must not have been dropped by
# the trust/migration edit.
if grep -qE '^[[:space:]]*WRAP=.*codex.*--dangerously-bypass-approvals-and-sandbox' "$ORCH_SPAWN_SCRIPT"; then
    approval_flag="present"
else
    approval_flag="missing"
fi
assert "codex WRAP: --dangerously-bypass-approvals-and-sandbox preserved" "present" "$approval_flag"

echo
echo "=== codex WRAP expands \$CWD into the trust key ==="

# The -c key includes the spawn's cwd as a TOML quoted key path. We
# can't run codex in CI, but we can confirm the script literally
# interpolates $CWD into the key rather than hard-coding a path.
if grep -qE 'projects\.\\"\$CWD\\".trust_level' "$ORCH_SPAWN_SCRIPT"; then
    cwd_interpolation="dynamic"
else
    cwd_interpolation="static-or-missing"
fi
assert "codex WRAP: trust key uses \$CWD (per-spawn), not a hard-coded path" "dynamic" "$cwd_interpolation"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
