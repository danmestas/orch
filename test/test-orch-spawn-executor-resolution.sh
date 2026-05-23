#!/usr/bin/env bash
#
# Resolution-order test for orch-spawn's hybrid executor discovery
# (Proposal 0003 / issue #142). Validates the resolve_executor()
# precedence chain:
#
#   1. ORCH_EXECUTOR_<NAME>_CMD env var  — operator override (shell string)
#   2. command -v orch-executor-<name>   — PATH-installed sister-repo binary
#   3. ${ORCH_REPO_ROOT}/executors/<n>/spawn.sh — in-tree fallback
#
# Each test wires up exactly one layer and asserts orch-spawn delegates to
# the expected mock script. The mock prints a unique pane-id-shaped string
# on stdout; orch-spawn copies that to its own stdout, so we can inspect
# `$(orch-spawn ...)` to confirm which layer fired.
#
# Mocks deliberately bypass tmux/shim work — no `%`-prefixed pane id is
# returned (so slug-labeling is a no-op) and --no-shim disables shim
# launch. The whole test is self-contained: no tmux session, no live
# bus, no real claude binary.
#
# Run with: bash test/test-orch-spawn-executor-resolution.sh

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

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn || true)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

# Resolve the repo root the same way orch-spawn does, so the in-tree
# fallback test can write to executors/<name>/spawn.sh under it.
_SELF="$SPAWN"
for _i in 1 2 3 4 5; do
    [ -L "$_SELF" ] || break
    _next=$(readlink "$_SELF")
    case "$_next" in /*) _SELF=$_next ;; *) _SELF="$(dirname "$_SELF")/$_next" ;; esac
done
ORCH_REPO_ROOT="$(cd "$(dirname "$_SELF")/.." && pwd -P)"

# Drop the interactive pause tail so any (improbable) fall-through to a
# real spawn closes cleanly.
export ORCH_NO_PAUSE_ON_EXIT=1

SANDBOX=$(mktemp -d)
INTREE_DIR="${ORCH_REPO_ROOT}/executors/testresolve"
cleanup() {
    rm -rf "$SANDBOX"
    rm -rf "$INTREE_DIR"
}
trap cleanup EXIT

echo "Testing $SPAWN executor-resolution precedence..."

# --- Layer 1: ORCH_EXECUTOR_<NAME>_CMD env override wins ---
# The override is a shell string; bash -c interprets it inside the spawn
# subshell, so it can be a binary path, a curl pipeline, or `echo ...`.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_EXECUTOR_CF_WORKER_CMD="echo override-handle-1" \
    "$SPAWN" dummy --executor cf-worker --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "env override: exits 0"            0                     "$rc"
assert "env override: stdout is override" "override-handle-1"   "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- Layer 1 normalisation: cf-durable-object → CF_DURABLE_OBJECT ---
# Confirms the upper-snake-case mapping handles multi-hyphen names.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_EXECUTOR_CF_DURABLE_OBJECT_CMD="echo override-handle-2" \
    "$SPAWN" dummy --executor cf-durable-object --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "env override (multi-hyphen): exits 0"            0                   "$rc"
assert "env override (multi-hyphen): stdout is override" "override-handle-2" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- Layer 2: PATH discovery via `command -v orch-executor-<name>` ---
# Stage a fake binary in a tempdir and prepend to PATH. orch-spawn must
# pick it ahead of any in-tree fallback (none exists for this name, but
# we still want Layer 2 explicitly preferred over Layer 3).
PATH_MOCK_DIR="$SANDBOX/path-mock"
mkdir -p "$PATH_MOCK_DIR"
cat > "$PATH_MOCK_DIR/orch-executor-foo" <<'EOF'
#!/usr/bin/env bash
echo path-handle-3
EOF
chmod +x "$PATH_MOCK_DIR/orch-executor-foo"

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$PATH_MOCK_DIR:$PATH" \
    "$SPAWN" dummy --executor foo --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "PATH discovery: exits 0"           0              "$rc"
assert "PATH discovery: stdout is binary"  "path-handle-3" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- Layer 2 priority: PATH wins over in-tree when both exist ---
# Stage both the path mock and an in-tree script with the same name; PATH
# should fire and the in-tree script must NOT run.
mkdir -p "$INTREE_DIR"
cat > "$INTREE_DIR/spawn.sh" <<'EOF'
#!/usr/bin/env bash
echo intree-handle-NEVER
EOF
chmod +x "$INTREE_DIR/spawn.sh"

PATH_MOCK_DIR2="$SANDBOX/path-mock-2"
mkdir -p "$PATH_MOCK_DIR2"
cat > "$PATH_MOCK_DIR2/orch-executor-testresolve" <<'EOF'
#!/usr/bin/env bash
echo path-handle-4
EOF
chmod +x "$PATH_MOCK_DIR2/orch-executor-testresolve"

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$PATH_MOCK_DIR2:$PATH" \
    "$SPAWN" dummy --executor testresolve --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "PATH > in-tree: exits 0"           0              "$rc"
assert "PATH > in-tree: PATH binary wins"  "path-handle-4" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- Layer 3: in-tree fallback when neither env nor PATH match ---
# Re-use the in-tree script staged above but drop the PATH mock so only
# Layer 3 can fire. Rewrite to a sentinel that distinguishes the success.
cat > "$INTREE_DIR/spawn.sh" <<'EOF'
#!/usr/bin/env bash
echo intree-handle-5
EOF
chmod +x "$INTREE_DIR/spawn.sh"

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" dummy --executor testresolve --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "in-tree fallback: exits 0"          0                 "$rc"
assert "in-tree fallback: stdout is intree" "intree-handle-5" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- env > PATH: env override wins even when PATH binary also exists ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_EXECUTOR_FOO_CMD="echo override-wins-6" \
PATH="$PATH_MOCK_DIR:$PATH" \
    "$SPAWN" dummy --executor foo --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "env > PATH: exits 0"             0                  "$rc"
assert "env > PATH: env override wins"   "override-wins-6"  "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- No layer resolves: diagnostic enumerates all three ---
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" dummy --executor doesnotexist --no-shim \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "no-resolve: exits non-zero"       1   "$rc"
assert "no-resolve: stdout is empty"      ""  "$(cat "$TMP_OUT")"
assert_contains "no-resolve: diagnostic mentions env var"   \
    "ORCH_EXECUTOR_DOESNOTEXIST_CMD" "$(cat "$TMP_ERR")"
assert_contains "no-resolve: diagnostic mentions PATH"      \
    "orch-executor-doesnotexist"     "$(cat "$TMP_ERR")"
assert_contains "no-resolve: diagnostic mentions in-tree"   \
    "executors/doesnotexist/spawn.sh" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# --- Name validation: reject anything outside [a-z][a-z0-9-]* ---
# Refuses uppercase, underscores, leading digits, empty values — these
# would map to ambiguous env-var names or unsafe path components.
for bad in "BadCase" "with_underscore" "1starts-with-digit"; do
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    "$SPAWN" dummy --executor "$bad" --no-shim \
        >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "name validation: rejects '$bad' (exit)"   1  "$rc"
    assert "name validation: rejects '$bad' (stdout)" "" "$(cat "$TMP_OUT")"
    assert_contains "name validation: rejects '$bad' (msg)" \
        "--executor must match" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"
done

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
