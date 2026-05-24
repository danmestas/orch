#!/usr/bin/env bash
#
# Composition table tests for orch-spawn's --persistence / --layout
# flags (Proposal 0008 / issue #180).
#
# Validates:
#   1. Default invocation works (no --persistence / --layout flags).
#   2. Explicit tmux+tmux works.
#   3. Cross-engine pairs (tmux+cmux, cmux+tmux, etc.) are rejected at
#      flag-parse with a clear diagnostic naming the supported pairs.
#   4. Malformed engine names are rejected at flag-parse.
#   5. The orch-engines binary is the source of truth — when we override
#      ORCH_ENGINES_BIN to a mock that always rejects, the spawn fails
#      regardless of the pair.
#
# Tests use a mock executor (env override) so no tmux session is
# required. Spawns return a fake pane id from the mock.
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

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn || true)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

# Resolve repo root the same way orch-spawn does, so we can build
# orch-engines locally for this test run.
_SELF="$SPAWN"
for _i in 1 2 3 4 5; do
    [ -L "$_SELF" ] || break
    _next=$(readlink "$_SELF")
    case "$_next" in /*) _SELF=$_next ;; *) _SELF="$(dirname "$_SELF")/$_next" ;; esac
done
ORCH_REPO_ROOT="$(cd "$(dirname "$_SELF")/.." && pwd -P)"

# Build orch-engines once for the suite.
SANDBOX=$(mktemp -d)
ENGINES_BIN="$SANDBOX/orch-engines"
(cd "$ORCH_REPO_ROOT" && go build -o "$ENGINES_BIN" ./cmd/orch-engines) || {
    echo "could not build orch-engines"
    exit 2
}

# Mock executor script: prints a fake pane id and exits cleanly. The
# composition validator runs BEFORE the executor, so for rejection
# tests this never fires; for pass tests it returns a pane id we then
# strip via --no-shim.
MOCK_DIR="$SANDBOX/mock"
mkdir -p "$MOCK_DIR"
cat > "$MOCK_DIR/orch-executor-mockcomp" <<'EOF'
#!/usr/bin/env bash
echo "%99"
EOF
chmod +x "$MOCK_DIR/orch-executor-mockcomp"

export ORCH_NO_PAUSE_ON_EXIT=1
export ORCH_ENGINES_BIN="$ENGINES_BIN"

cleanup() { rm -rf "$SANDBOX"; }
trap cleanup EXIT

# -----------------------------------------------------------------------------
echo
echo "=== 1. Default composition: no flags, tmux+tmux implied ==="
PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --no-shim --quiet >/dev/null 2>&1
assert_rc "no --persistence / --layout flags spawns OK" 0 $?

# -----------------------------------------------------------------------------
echo
echo "=== 2. Explicit tmux+tmux ==="
PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence tmux --layout tmux --no-shim --quiet >/dev/null 2>&1
assert_rc "explicit --persistence tmux --layout tmux spawns OK" 0 $?

# -----------------------------------------------------------------------------
echo
echo "=== 3. Cross-engine rejection: tmux + cmux ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence tmux --layout cmux --no-shim 2>&1)
RC=$?
assert_rc "tmux + cmux is rejected" 1 $RC
assert_contains "rejection diagnostic names supported pairs" "supported:" "$OUT"
assert_contains "rejection diagnostic mentions tmux,tmux" "persistence=tmux,layout=tmux" "$OUT"

# -----------------------------------------------------------------------------
echo
echo "=== 4. Cross-engine rejection: cmux + tmux ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence cmux --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "cmux + tmux is rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 5. cmux + cmux is rejected in Phase A ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence cmux --layout cmux --no-shim 2>&1)
RC=$?
assert_rc "cmux + cmux is rejected (Phase B target)" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 6. Malformed persistence name rejected at flag-parse ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence "TMUX" --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "--persistence TMUX (uppercase) rejected" 1 $RC
assert_contains "diagnostic names the rule" "must match [a-z]" "$OUT"

# -----------------------------------------------------------------------------
echo
echo "=== 7. Malformed layout name rejected at flag-parse ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence tmux --layout "1cmux" --no-shim 2>&1)
RC=$?
assert_rc "--layout 1cmux (digit-leading) rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 8. Empty persistence flag rejected ==="
OUT=$(PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence "" --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "--persistence '' rejected" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 9. orch-engines binary is the source of truth ==="
# Drop in a mock that always rejects, point ORCH_ENGINES_BIN at it.
cat > "$SANDBOX/always-reject" <<'EOF'
#!/usr/bin/env bash
echo "mock: always-reject" >&2
exit 1
EOF
chmod +x "$SANDBOX/always-reject"
OUT=$(ORCH_ENGINES_BIN="$SANDBOX/always-reject" PATH="$MOCK_DIR:$PATH" "$SPAWN" claude --executor mockcomp --persistence tmux --layout tmux --no-shim 2>&1)
RC=$?
assert_rc "spawn fails when ORCH_ENGINES_BIN rejects" 1 $RC

# -----------------------------------------------------------------------------
echo
echo "=== 10. orch-engines list returns the closed registry ==="
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
