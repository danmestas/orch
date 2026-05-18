#!/usr/bin/env bash
# Output-contract regression tests for orch-spawn's --sesh-session flag.
#
# Wires the worker-side leg of the fossil-as-trunk swarm workflow (sesh
# issue #64). --sesh-session <label> shells out to `sesh worker-cwd
# <label>` and uses the printed path as the worker's cwd.
#
# This test stubs `sesh` with a fake binary via ORCH_SESH_BIN so we can
# exercise the resolution path, the error-propagation path, and the
# mutually-exclusive guard without needing a real sesh hub up. End-to-end
# verification with a real hub is in the manual recipe in the PR body.
#
# Run with: bash test/test-orch-spawn-sesh-session.sh
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

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

echo "Testing $SPAWN --sesh-session contract..."

# Build a fake sesh binary in a tempdir. The fake echoes a configured
# path on stdout (mocking `sesh worker-cwd <label>` success), or writes a
# configured stderr and exits non-zero (mocking the missing-checkout
# error path).
STUB_DIR=$(mktemp -d)
trap 'rm -rf "$STUB_DIR"' EXIT

cat > "$STUB_DIR/sesh-ok" <<'STUB_OK'
#!/usr/bin/env bash
# Stub: 'sesh worker-cwd <label>' → prints a fixed path on stdout.
set -e
[ "$1" = "worker-cwd" ] || { echo "stub-sesh: expected 'worker-cwd', got $1" >&2; exit 1; }
LABEL=$2
[ -n "$LABEL" ] || { echo "stub-sesh: missing label" >&2; exit 1; }
echo "$STUB_RETURN_PATH"
STUB_OK
chmod +x "$STUB_DIR/sesh-ok"

cat > "$STUB_DIR/sesh-missing" <<'STUB_MISSING'
#!/usr/bin/env bash
# Stub: 'sesh worker-cwd <label>' → exits non-zero with a missing-checkout
# message that matches sesh's real shape.
set -e
[ "$1" = "worker-cwd" ] || { echo "stub-sesh: expected 'worker-cwd', got $1" >&2; exit 1; }
LABEL=$2
echo "no fossil checkout at /tmp/fakeproject/.sesh/checkouts/$LABEL — run 'sesh worktree $LABEL' first" >&2
exit 1
STUB_MISSING
chmod +x "$STUB_DIR/sesh-missing"

echo
echo "=== --sesh-session: mutually exclusive with --cwd ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-ok" STUB_RETURN_PATH="/tmp/fakepath" \
    "$SPAWN" pi --cwd /tmp/foo --sesh-session alpha >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "cwd+sesh-session: exits non-zero" 1 "$rc"
assert "cwd+sesh-session: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "cwd+sesh-session: stderr names the conflict" "mutually exclusive" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Also check the reverse arg order (--sesh-session first, then --cwd).
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-ok" STUB_RETURN_PATH="/tmp/fakepath" \
    "$SPAWN" pi --sesh-session alpha --cwd /tmp/foo >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "sesh-session+cwd: exits non-zero (reverse order)" 1 "$rc"
assert_contains "sesh-session+cwd: stderr names the conflict (reverse order)" "mutually exclusive" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --sesh-session: propagates sesh stderr on missing checkout ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-missing" \
    "$SPAWN" pi --sesh-session nonexistent >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "missing-checkout: exits non-zero" 1 "$rc"
assert "missing-checkout: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "missing-checkout: stderr is context-prefixed" "sesh worker-cwd nonexistent failed" "$(cat "$TMP_ERR")"
assert_contains "missing-checkout: stderr carries sesh's hint" "sesh worktree nonexistent" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --sesh-session: missing sesh binary surfaces a clear error ==="

# Point ORCH_SESH_BIN at a path that doesn't exist. orch-spawn must
# refuse early rather than spawning anything.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/no-such-binary" \
    "$SPAWN" pi --sesh-session alpha >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "missing-sesh-binary: exits non-zero" 1 "$rc"
assert "missing-sesh-binary: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "missing-sesh-binary: stderr explains" "sesh" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --sesh-session: stub returns a path, orch-spawn parses it ==="

# We can't reach a real spawn in this test (no tmux server, no agent
# binary), but we can verify orch-spawn ACCEPTS --sesh-session and
# advances past the resolution. A safe probe: combine --sesh-session
# with --outfit on agent=pi. orch-spawn's outfit-on-non-claude guard
# fires AFTER cwd resolution, so a stub-good resolution followed by
# the outfit error proves --sesh-session was parsed and CWD was set.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-ok" STUB_RETURN_PATH="/tmp/orchspawn-sesh-test" \
    "$SPAWN" pi --sesh-session alpha --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "stub-resolves-then-outfit-error: exits non-zero" 1 "$rc"
# The outfit-on-pi guard fires; the sesh-resolution did NOT fail, so the
# stderr should be the outfit error and NOT a sesh-resolution error.
assert_contains "stub-resolves-then-outfit-error: stderr is outfit error" "claude" "$(cat "$TMP_ERR")"
NEG=$(cat "$TMP_ERR")
if [[ "$NEG" == *"sesh worker-cwd"* ]]; then
    echo "  FAIL  stub-resolves-then-outfit-error: stderr should NOT mention sesh worker-cwd (resolution succeeded)"
    echo "        got: $NEG"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("resolution-then-outfit: stderr leaked sesh error")
else
    echo "  PASS  stub-resolves-then-outfit-error: stderr did not leak sesh-resolution error"
    PASS=$((PASS + 1))
fi
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
