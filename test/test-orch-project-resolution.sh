#!/usr/bin/env bash
# Tests for orch-spawn --project resolution (zoxide-optional fallback).
#
# The original implementation hard-required zoxide. PR 2 made it optional:
#   1. zoxide query <name> if zoxide is on PATH and the name is indexed
#   2. ${ORCH_PROJECTS_ROOT:-~/projects}/<name> as a directory fallback
#   3. Helpful error if neither resolves
#
# Run with: bash test/test-orch-project-resolution.sh
set -uo pipefail

# Drop orch-spawn's interactive pause-on-exit wrapper tail so any
# pane this test spawns (--headless --project) closes cleanly when
# the agent (pi) is absent on the runner (closes #178).
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
        echo "        got:                $(printf '%s' "$haystack" | head -c 200)"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"; for p in $SPAWNED_PANES; do tmux kill-pane -t "$p" 2>/dev/null || true; rm -f ~/.cache/orch-registry/$p.json 2>/dev/null; done' EXIT
SPAWNED_PANES=""

# Build a fake projects-root with one valid project dir.
mkdir -p "$SANDBOX/projects/myapp"
mkdir -p "$SANDBOX/projects/another"
export ORCH_PROJECTS_ROOT="$SANDBOX/projects"

# Build a stub PATH with NO zoxide installed (simulating a user without zoxide).
# Copy orch-spawn + its sibling binaries it depends on (tmux, jq;
# suit-prepare can be missing — we only test --project resolution).
NOZX_BIN="$SANDBOX/nozx-bin"
mkdir -p "$NOZX_BIN"
for b in orch-spawn tmux jq fswatch; do
    src=$(command -v "$b" 2>/dev/null) || continue
    ln -sf "$src" "$NOZX_BIN/$b"
done
# Also need the standard system binaries for the spawn script to function.
NOZX_PATH="$NOZX_BIN:/usr/bin:/bin"

# Stub a fake zoxide that fails (for one of the tests below).
FAKE_ZOXIDE_BIN="$SANDBOX/fake-zoxide-bin"
mkdir -p "$FAKE_ZOXIDE_BIN"
cat > "$FAKE_ZOXIDE_BIN/zoxide" <<'EOT'
#!/usr/bin/env bash
# Always exits non-zero to simulate "name not indexed in zoxide"
exit 1
EOT
chmod +x "$FAKE_ZOXIDE_BIN/zoxide"
for b in orch-spawn tmux jq fswatch; do
    src=$(command -v "$b" 2>/dev/null) || continue
    ln -sf "$src" "$FAKE_ZOXIDE_BIN/$b"
done
FAKE_ZX_PATH="$FAKE_ZOXIDE_BIN:/usr/bin:/bin"

echo "=== --project resolution: zoxide absent → falls back to ORCH_PROJECTS_ROOT ==="

# 1) zoxide not on PATH; project dir exists at ORCH_PROJECTS_ROOT/myapp.
# Use --headless so we don't fight for the visible window; --no-fleet to skip
# the fleet-prompt path; pi to avoid the suit dependency entirely.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$NOZX_PATH" "$SPAWN" pi --headless --no-fleet --project myapp >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
PANE=$(cat "$TMP_OUT")
assert "no-zoxide + project exists: rc=0" 0 "$rc"
[[ "$PANE" =~ ^%[0-9]+$ ]] && pane_ok="ok" || pane_ok="invalid: [$PANE]"
assert "no-zoxide + project exists: stdout is %pane" "ok" "$pane_ok"
[ -n "$PANE" ] && SPAWNED_PANES="$SPAWNED_PANES $PANE"
# Verify the registry recorded the resolved cwd
sleep 1
if [ -f "$HOME/.cache/orch-registry/$PANE.json" ]; then
    REG_CWD=$(jq -r .cwd "$HOME/.cache/orch-registry/$PANE.json")
    assert "no-zoxide + project exists: registry cwd points at fallback" "$SANDBOX/projects/myapp" "$REG_CWD"
fi
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --project resolution: fake zoxide fails → falls back ==="

# 2) zoxide IS on PATH but errors; project dir exists at fallback.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$FAKE_ZX_PATH" "$SPAWN" pi --headless --no-fleet --project another >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
PANE2=$(cat "$TMP_OUT")
assert "zoxide-failed + fallback exists: rc=0" 0 "$rc"
[[ "$PANE2" =~ ^%[0-9]+$ ]] && pane_ok="ok" || pane_ok="invalid: [$PANE2]"
assert "zoxide-failed + fallback exists: stdout is %pane" "ok" "$pane_ok"
[ -n "$PANE2" ] && SPAWNED_PANES="$SPAWNED_PANES $PANE2"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --project resolution: neither zoxide nor fallback resolves → helpful error ==="

# 3) No zoxide; project dir does NOT exist anywhere we look.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
PATH="$NOZX_PATH" "$SPAWN" pi --headless --no-fleet --project does-not-exist >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "neither resolves: exits non-zero" 1 "$rc"
assert "neither resolves: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "neither resolves: stderr names the project" "does-not-exist" "$(cat "$TMP_ERR")"
assert_contains "neither resolves: stderr names the fallback path" "$SANDBOX/projects/does-not-exist" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
