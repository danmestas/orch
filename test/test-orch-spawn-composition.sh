#!/usr/bin/env bash
#
# Composition table tests for `orch spawn --persistence / --layout`
# flags (Proposal 0008 / issue #180).
#
# Validates:
#   1. Cross-engine pairs (tmux+cmux, cmux+tmux, etc.) are rejected at
#      flag-parse with a clear diagnostic naming the supported pairs.
#   2. Malformed engine names are rejected at flag-parse.
#   3. The orch-engines binary is the source of truth — when we override
#      ORCH_ENGINES_BIN to a mock that always rejects, the spawn fails
#      regardless of the pair.
#   4. orch-engines list returns the closed registry.
#
# Validator rejections short-circuit before any tmux work, so this test
# doesn't need a live tmux session. Happy-path spawn (default tmux+tmux)
# is covered by the live-tmux integration tests
# (test-orch-spawn-output.sh + test-orch-spawn-verify.sh).
#
# Run with: bash test/test-orch-spawn-composition.sh

set -uo pipefail

PASS=0
FAIL=0
FAILED_TESTS=()

assert_rc() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        echo "  PASS  $desc (rc=$got)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected rc: $expected"
        echo "        got rc:      $got"
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
        echo "        got: $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

SPAWN=${ORCH_SPAWN_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/helpers/orch-spawn}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

# Resolve repo root from this test script's own location.
ORCH_REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"

# Build orch-engines once for the suite.
SANDBOX=$(mktemp -d)
ENGINES_BIN="$SANDBOX/orch-engines"
(cd "$ORCH_REPO_ROOT" && go build -o "$ENGINES_BIN" ./cmd/orch-engines) || {
    echo "could not build orch-engines"
    exit 2
}

export ORCH_NO_PAUSE_ON_EXIT=1
export ORCH_ENGINES_BIN="$ENGINES_BIN"

cleanup() { rm -rf "$SANDBOX"; }
trap cleanup EXIT

# -----------------------------------------------------------------------------
echo
echo "=== 1. Cross-engine rejection: tmux + cmux ==="
OUT=$("$SPAWN" claude --persistence tmux --layout cmux --no-shim 2>&1)
RC=$?
assert_rc "tmux + cmux is rejected" 1 $RC
assert_contains "rejection diagnostic names supported pairs" "supported:" "$OUT"
assert_contains "rejection diagnostic mentions tmux,tmux" "persistence=tmux,layout=tmux" "$OUT"

# -----------------------------------------------------------------------------
echo
echo "=== 2. Cross-engine rejection: cmux + tmux ==="
OUT=$("$SPAWN" claude --persistence cmux --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "cmux + tmux is rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 3. cmux + cmux is rejected in Phase A ==="
OUT=$("$SPAWN" claude --persistence cmux --layout cmux --no-shim 2>&1)
RC=$?
assert_rc "cmux + cmux is rejected (Phase B target)" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 4. Malformed persistence name rejected at flag-parse ==="
OUT=$("$SPAWN" claude --persistence "TMUX" --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "--persistence TMUX (uppercase) rejected" 1 $RC
assert_contains "diagnostic names the rule" "must match [a-z]" "$OUT"

# -----------------------------------------------------------------------------
echo
echo "=== 5. Malformed layout name rejected at flag-parse ==="
OUT=$("$SPAWN" claude --persistence tmux --layout "1cmux" --no-shim 2>&1)
RC=$?
assert_rc "--layout 1cmux (digit-leading) rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 6. Empty persistence flag rejected ==="
OUT=$("$SPAWN" claude --persistence "" --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "--persistence '' rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 7. orch-engines binary is the source of truth ==="
# Drop in a mock that always rejects, point ORCH_ENGINES_BIN at it.
cat > "$SANDBOX/always-reject" <<'EOF'
#!/usr/bin/env bash
echo "mock: always-reject" >&2
exit 1
EOF
chmod +x "$SANDBOX/always-reject"
OUT=$(ORCH_ENGINES_BIN="$SANDBOX/always-reject" "$SPAWN" claude --persistence tmux --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "spawn fails when ORCH_ENGINES_BIN rejects" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 8. orch-engines list returns the closed registry ==="
OUT=$("$ENGINES_BIN" list 2>&1)
RC=$?
assert_rc "orch-engines list rc=0" 0 $RC
assert_contains "list includes tmux+tmux" "persistence=tmux layout=tmux" "$OUT"

# -----------------------------------------------------------------------------
echo
echo "=================================================================="
echo "Composition-table tests: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do
        echo "  - $t"
    done
    exit 1
fi
