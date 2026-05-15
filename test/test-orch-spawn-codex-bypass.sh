#!/usr/bin/env bash
#
# Regression test for orch-spawn's first-run-prompt bypasses (#37, #46).
#
# Codex's per-directory trust prompt and migrate-from-claude prompt block
# headless spawning. The prior fix (#45) attempted inline `-c` overrides
# but missed the canonical-vs-raw cwd mismatch (e.g. /tmp vs /private/tmp
# on macOS), and used --enable external_migration which is precisely the
# trigger for the migrate dialog rather than its bypass. #46 documents
# the docker repro that uncovered both regressions.
#
# Current approach (verified end-to-end in a fresh node:24-slim container):
#   * trust prompt — pre-stage ~/.codex/config.toml with a canonical
#     [projects."<canon>"] trust_level="trusted" block, idempotently.
#   * migrate-from-claude prompt — flip --enable external_migration to
#     --disable external_migration so the feature gate is OFF.
#
# Pi and gemini wraps were hardened in the same change: pi gains
# PI_TELEMETRY=0 + --offline (cosmetic / network suppression, no blocking
# dialogs exist); gemini gains --skip-trust which also prevents --yolo
# from being silently downgraded in untrusted dirs.
#
# This test asserts each piece is wired correctly in the script. Full
# end-to-end verification requires the docker harness in #46.

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

echo "=== codex: pre-stages canonical trust key in ~/.codex/config.toml ==="

if grep -qE 'CANON_CWD=\$\(cd "\$CWD" && pwd -P\)' "$ORCH_SPAWN_SCRIPT"; then
    canon="present"
else
    canon="missing"
fi
assert "codex: CANON_CWD computed via pwd -P" "present" "$canon"

if grep -qE 'grep -qF.*projects\.\\"\$CANON_CWD\\".*config\.toml' "$ORCH_SPAWN_SCRIPT"; then
    idem="present"
else
    idem="missing"
fi
assert "codex: pre-stage write is idempotent (grep -qF guard)" "present" "$idem"

if grep -qE 'printf.*projects.*trust_level.*trusted.*CANON_CWD.*config\.toml' "$ORCH_SPAWN_SCRIPT"; then
    write="present"
else
    write="missing"
fi
assert "codex: pre-stage writes [projects.\"<canon>\"] trust_level=trusted" "present" "$write"

echo
echo "=== codex: --disable external_migration (was --enable, which TRIGGERED the dialog) ==="

if grep -qE '^[[:space:]]*WRAP=.*codex.*--disable[[:space:]]+external_migration' "$ORCH_SPAWN_SCRIPT"; then
    flip="present"
else
    flip="missing"
fi
assert "codex WRAP: --disable external_migration present" "present" "$flip"

if grep -qE '^[[:space:]]*WRAP=.*codex.*--enable[[:space:]]+external_migration' "$ORCH_SPAWN_SCRIPT"; then
    legacy_enable="present"
else
    legacy_enable="absent"
fi
assert "codex WRAP: legacy --enable external_migration removed" "absent" "$legacy_enable"

if grep -qE "^[[:space:]]*WRAP=.*codex.*-c[[:space:]]*'projects" "$ORCH_SPAWN_SCRIPT"; then
    legacy_inline="present"
else
    legacy_inline="absent"
fi
assert "codex WRAP: legacy inline -c projects override removed" "absent" "$legacy_inline"

echo
echo "=== codex: --dangerously-bypass-approvals-and-sandbox preserved ==="

if grep -qE '^[[:space:]]*WRAP=.*codex.*--dangerously-bypass-approvals-and-sandbox' "$ORCH_SPAWN_SCRIPT"; then
    approval_flag="present"
else
    approval_flag="missing"
fi
assert "codex WRAP: --dangerously-bypass-approvals-and-sandbox preserved" "present" "$approval_flag"

echo
echo "=== pi: --offline + PI_TELEMETRY=0 ==="

if grep -qE '^[[:space:]]*WRAP=.*PI_TELEMETRY=0.*pi --offline' "$ORCH_SPAWN_SCRIPT"; then
    pi_flags="present"
else
    pi_flags="missing"
fi
assert "pi WRAP: PI_TELEMETRY=0 + --offline present" "present" "$pi_flags"

echo
echo "=== gemini: --skip-trust ==="

if grep -qE '^[[:space:]]*WRAP=.*gemini --yolo --skip-trust' "$ORCH_SPAWN_SCRIPT"; then
    gem_flag="present"
else
    gem_flag="missing"
fi
assert "gemini WRAP: --yolo --skip-trust present" "present" "$gem_flag"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
