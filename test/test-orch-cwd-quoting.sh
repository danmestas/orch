#!/usr/bin/env bash
# Regression tests for #148: $CWD-quoting hygiene + ORCH_SESH_BIN
# absolute-path normalization in orch-spawn.
#
# Two concerns covered here, both grounded in the issue:
#
#   1. CWD round-trip: paths containing whitespace (and other shell-meta-ish
#      chars that pass the existing safety filter) reach the executor as
#      a single argv element, intact. The dispatcher already quotes
#      "$CWD" at every use site and rejects truly-dangerous chars (",',
#      $, \, `) up front — we lock in those guarantees here.
#
#   2. ORCH_SESH_BIN normalization: bare-name lookups on PATH resolve to
#      an absolute path BEFORE any cd. Relative paths with a directory
#      component resolve against the caller's cwd. Missing binaries
#      surface a usable error message that names the env var.
#
# These tests run against the dispatcher only; no real tmux pane, no real
# sesh hub. We use the same "stub sesh binary in tempdir" pattern as
# test-orch-spawn-sesh-session.sh, plus an outfit-on-pi guard probe to
# force orch-spawn to exit *after* resolving CWD/ORCH_SESH_BIN but
# *before* it tries to actually spawn anything.
#
# Run with: bash test/test-orch-cwd-quoting.sh
set -uo pipefail

# Drop orch-spawn's interactive pause-on-exit wrapper tail — defensive
# even though current tests early-exit before pane creation, so a future
# mutation that does spawn won't leak zombies (closes #178).
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

assert_not_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" != *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        forbidden substring: $substr"
        echo "        got:                 $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

# Default to the in-tree test/helpers/orch-spawn adapter (post-#189:
# bin/orch-spawn was deleted; the adapter execs `orch spawn` via the
# repo's bin/orch lazy-build wrapper). Operators can still override via
# ORCH_SPAWN_BIN to point at a system install.
SPAWN=${ORCH_SPAWN_BIN:-$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/helpers/orch-spawn}
[ -x "$SPAWN" ] || { echo "orch-spawn not found (set ORCH_SPAWN_BIN to override): $SPAWN"; exit 2; }

echo "Testing $SPAWN — #148 CWD quoting + ORCH_SESH_BIN normalization..."

STUB_DIR=$(mktemp -d)
trap 'rm -rf "$STUB_DIR"' EXIT

# Stub sesh: echoes the path it was asked to resolve. Lets us thread
# the test-controlled CWD through the dispatcher and back into the
# "outfit error" stderr, where we can grep it.
cat > "$STUB_DIR/sesh-ok" <<'STUB_OK'
#!/usr/bin/env bash
set -e
[ "$1" = "worker-cwd" ] || { echo "stub-sesh: expected 'worker-cwd', got $1" >&2; exit 1; }
LABEL=$2
[ -n "$LABEL" ] || { echo "stub-sesh: missing label" >&2; exit 1; }
echo "$STUB_RETURN_PATH"
STUB_OK
chmod +x "$STUB_DIR/sesh-ok"

# ---------------------------------------------------------------------------
echo
echo "=== Section 1: CWD with whitespace round-trips through the dispatcher ==="
# ---------------------------------------------------------------------------

# Pick a path containing a space. Pass it via --cwd. Force a clean
# exit AFTER cwd resolution but BEFORE any spawn attempt by combining
# with --outfit on agent=pi (orch-spawn rejects outfit-on-non-claude
# AFTER cwd resolution).
SPACE_DIR="/tmp/has space test-148"
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPAWN" pi --cwd "$SPACE_DIR" --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "cwd-with-space: exits non-zero (outfit-on-pi guard fires)" 1 "$rc"
assert "cwd-with-space: stdout empty" "" "$(cat "$TMP_OUT")"
assert_contains "cwd-with-space: stderr is the outfit error (means cwd parsing succeeded)" "claude" "$(cat "$TMP_ERR")"
# Quoting bug would have caused word-splitting; "unknown flag" would
# leak instead of the outfit error. Belt-and-braces check:
assert_not_contains "cwd-with-space: stderr did not leak 'unknown flag'" "unknown flag" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Same shape via --sesh-session: orch-spawn calls the stub which echoes
# back a space-containing path; the dispatcher must accept it and pass
# it through unchanged.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-ok" STUB_RETURN_PATH="/tmp/sesh has space" \
    "$SPAWN" pi --sesh-session alpha --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "sesh-cwd-with-space: exits non-zero (outfit guard, not unknown-flag)" 1 "$rc"
assert_contains "sesh-cwd-with-space: stderr is the outfit error" "claude" "$(cat "$TMP_ERR")"
assert_not_contains "sesh-cwd-with-space: stderr did not leak 'unknown flag'" "unknown flag" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# ---------------------------------------------------------------------------
echo
echo "=== Section 2: CWD with dangerous metachars is rejected up-front ==="
# ---------------------------------------------------------------------------

# The dispatcher's case "$CWD" in *[\"\'\$\\\`]* refuses paths that
# could craft shell injection through the WRAP. Verify that guard
# still works as intended.
# shellcheck disable=SC2016  # single quotes are intentional — we want literal $, `, etc. in the paths.
for bad in '/tmp/has$dollar' '/tmp/has`backtick' '/tmp/has\\backslash' "/tmp/has'quote"; do
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    "$SPAWN" pi --cwd "$bad" --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "cwd-rejects-dangerous ($bad): exits non-zero" 1 "$rc"
    assert_contains "cwd-rejects-dangerous ($bad): stderr names 'unsafe characters'" "unsafe characters" "$(cat "$TMP_ERR")"
    rm -f "$TMP_OUT" "$TMP_ERR"
done

# ---------------------------------------------------------------------------
echo
echo "=== Section 3: ORCH_SESH_BIN normalization ==="
# ---------------------------------------------------------------------------

# 3a. Unset → defaults to PATH lookup of `sesh`. If `sesh` isn't on
# PATH, we expect a clear error (not a silent NOOP). If `sesh` IS on
# PATH, we expect orch-spawn to advance past the resolution and hit
# the outfit guard (since pi+outfit fails).
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
# Strip our stub dir from PATH; rely on whatever sesh exists on the
# operator's PATH (often nothing in CI).
PATH_WITHOUT_STUB=$(echo "$PATH" | tr ':' '\n' | grep -v -F "$STUB_DIR" | paste -sd: -)
PATH="$PATH_WITHOUT_STUB" "$SPAWN" pi --sesh-session alpha >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "orch-sesh-bin-unset: exits non-zero (no sesh on PATH or stub harness error)" 1 "$rc"
# Either: "sesh binary on PATH" message (no sesh installed), or
# orch-spawn got past resolution and failed for some other reason.
# We don't pin to one of these — we just lock in non-zero exit.
rm -f "$TMP_OUT" "$TMP_ERR"

# 3b. ORCH_SESH_BIN=<absolute, executable> → resolves cleanly.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="$STUB_DIR/sesh-ok" STUB_RETURN_PATH="/tmp/abs-path-test" \
    "$SPAWN" pi --sesh-session alpha --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "orch-sesh-bin-absolute-ok: exits non-zero (outfit guard, NOT sesh error)" 1 "$rc"
assert_contains "orch-sesh-bin-absolute-ok: stderr is the outfit error" "claude" "$(cat "$TMP_ERR")"
assert_not_contains "orch-sesh-bin-absolute-ok: stderr did NOT mention sesh-on-PATH" "sesh' binary on PATH" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 3c. ORCH_SESH_BIN=<absolute, missing> → clear error naming the env var.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SESH_BIN="/tmp/no-such-sesh-binary-148" \
    "$SPAWN" pi --sesh-session alpha >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "orch-sesh-bin-missing: exits non-zero" 1 "$rc"
assert_contains "orch-sesh-bin-missing: stderr names ORCH_SESH_BIN" "ORCH_SESH_BIN" "$(cat "$TMP_ERR")"
assert_contains "orch-sesh-bin-missing: stderr echoes the offending value" "/tmp/no-such-sesh-binary-148" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 3d. ORCH_SESH_BIN=<bare name on stub PATH> → resolves to an absolute
# path internally via `command -v`. We can't directly inspect the
# resolved path, but we lock in that the resolution succeeds (no
# "sesh binary" error in stderr).
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$STUB_DIR:$PATH" ORCH_SESH_BIN="sesh-ok" STUB_RETURN_PATH="/tmp/bare-name-test" \
    "$SPAWN" pi --sesh-session alpha --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "orch-sesh-bin-bare-on-path: exits non-zero (outfit guard)" 1 "$rc"
assert_contains "orch-sesh-bin-bare-on-path: stderr is the outfit error" "claude" "$(cat "$TMP_ERR")"
assert_not_contains "orch-sesh-bin-bare-on-path: stderr did NOT mention sesh-on-PATH error" "sesh' binary on PATH" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 3e. ORCH_SESH_BIN=<relative ./path> → resolves against pwd BEFORE
# any cd. Confirm a relative path that exists at invocation time is
# accepted.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
(
    cd "$STUB_DIR" || exit 1
    ORCH_SESH_BIN="./sesh-ok" STUB_RETURN_PATH="/tmp/relative-test" \
        "$SPAWN" pi --sesh-session alpha --outfit engineer >"$TMP_OUT" 2>"$TMP_ERR" || true
)
rc=$?
# rc here is the subshell's exit; either way we want the stderr to
# show outfit-error (resolution succeeded), not sesh-not-found.
assert_contains "orch-sesh-bin-relative: stderr is the outfit error (relative path resolved)" "claude" "$(cat "$TMP_ERR")"
assert_not_contains "orch-sesh-bin-relative: stderr did NOT mention sesh-on-PATH" "sesh' binary on PATH" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# ---------------------------------------------------------------------------
echo
echo "=== Section 4: usage line documents ORCH_SESH_BIN ==="
# ---------------------------------------------------------------------------

# orch-spawn emits its usage on no-args. Verify ORCH_SESH_BIN appears
# in that line so operators discover the override.
TMP_ERR=$(mktemp)
"$SPAWN" >/dev/null 2>"$TMP_ERR" || true
assert_contains "usage-line: mentions ORCH_SESH_BIN" "ORCH_SESH_BIN" "$(cat "$TMP_ERR")"
rm -f "$TMP_ERR"

# ---------------------------------------------------------------------------
echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
