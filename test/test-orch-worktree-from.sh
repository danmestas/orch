#!/usr/bin/env bash
# Output-contract regression tests for orch-spawn's --worktree-from / --slug flags.
#
# Adds two operator-facing flags (issue #23):
#   --worktree-from=<sha> — create a git worktree at <sha> and spawn the
#                            worker there
#   --slug=<name>          — friendly identity for the worker: pane title
#                            + alias-file entry
#
# The two flags are decoupled per the Ousterhout note in the issue
# (slug = identity, worktree = filesystem). Both can be passed
# independently; passing both together is the common case where the
# slug also names the worktree directory.
#
# Strategy: this test runs the dispatcher only — no real tmux pane, no
# real agent. We force the dispatcher to exit AFTER it resolves
# worktree-from + slug but BEFORE it tries to actually spawn anything,
# by combining the flags with `--outfit X` on agent=pi (which has a
# claude-only outfit guard that fires post-resolution). That lets us
# observe the worktree dir on disk, validate slug parsing, and assert
# the mutual-exclusion behaviour without needing tmux.
#
# Run with: bash test/test-orch-worktree-from.sh
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

SPAWN=${ORCH_SPAWN_BIN:-$(command -v orch-spawn)}
[ -x "$SPAWN" ] || { echo "orch-spawn not on PATH (set ORCH_SPAWN_BIN to override)"; exit 2; }

echo "Testing $SPAWN --worktree-from / --slug contract..."

# Build a throwaway git repo with one commit so --worktree-from has a
# real sha to resolve. Use git -c to avoid polluting global config.
REPO=$(mktemp -d)
WORKTREE_ROOT=$(mktemp -d)
ALIASES_FILE=$(mktemp)
trap 'rm -rf "$REPO" "$WORKTREE_ROOT" "$ALIASES_FILE"' EXIT

(
    cd "$REPO" || exit 1
    git -c init.defaultBranch=main init -q
    git -c user.email=test@example.com -c user.name=test commit -q --allow-empty -m initial
)
SHA=$(git -C "$REPO" rev-parse HEAD)
SHA7=${SHA:0:7}

echo
echo "=== --worktree-from + --slug: happy path resolves before outfit guard ==="

# Run inside the repo (orch-spawn uses `git rev-parse --show-toplevel`
# to locate the parent). Combine with --outfit on agent=pi so the
# claude-only outfit guard fires AFTER worktree resolution — proves
# the worktree was created and slug parsed without needing tmux.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    ORCH_WORKTREE_ROOT="$WORKTREE_ROOT" ORCH_ALIASES_FILE="$ALIASES_FILE" \
    "$SPAWN" pi --worktree-from "$SHA" --slug test1 --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "happy-path: exits non-zero (outfit guard fires post-resolution)" 1 "$rc"
assert_contains "happy-path: stderr is outfit error (resolution succeeded)" "claude" "$(cat "$TMP_ERR")"
# Worktree dir should exist on disk.
if [ -d "$WORKTREE_ROOT/test1" ]; then
    echo "  PASS  happy-path: worktree dir created at \$ORCH_WORKTREE_ROOT/test1"
    PASS=$((PASS + 1))
else
    echo "  FAIL  happy-path: worktree dir NOT created at $WORKTREE_ROOT/test1"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("happy-path: worktree dir not created")
fi
# git should report the new worktree.
WT_LIST=$(git -C "$REPO" worktree list --porcelain 2>/dev/null)
assert_contains "happy-path: git knows about the new worktree" "$WORKTREE_ROOT/test1" "$WT_LIST"
rm -f "$TMP_OUT" "$TMP_ERR"
# Clean up the worktree for the next test.
git -C "$REPO" worktree remove "$WORKTREE_ROOT/test1" --force 2>/dev/null || rm -rf "$WORKTREE_ROOT/test1"

echo
echo "=== --worktree-from: bad sha surfaces helpful error, no worktree created ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    ORCH_WORKTREE_ROOT="$WORKTREE_ROOT" ORCH_ALIASES_FILE="$ALIASES_FILE" \
    "$SPAWN" pi --worktree-from deadbeefdeadbeef --slug bad1 --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "bad-sha: exits non-zero" 1 "$rc"
assert "bad-sha: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "bad-sha: stderr names the missing sha" "deadbeefdeadbeef" "$(cat "$TMP_ERR")"
assert_contains "bad-sha: stderr names the flag" "worktree-from" "$(cat "$TMP_ERR")"
if [ -d "$WORKTREE_ROOT/bad1" ]; then
    echo "  FAIL  bad-sha: worktree dir was created at $WORKTREE_ROOT/bad1 (should not exist)"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("bad-sha: worktree dir leaked")
else
    echo "  PASS  bad-sha: no worktree dir created"
    PASS=$((PASS + 1))
fi
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --worktree-from + --cwd: mutual-exclusion error ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    "$SPAWN" pi --worktree-from "$SHA" --cwd /tmp ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "wt+cwd: exits non-zero" 1 "$rc"
assert "wt+cwd: stdout is empty" "" "$(cat "$TMP_OUT")"
assert_contains "wt+cwd: stderr names the conflict" "mutually exclusive" "$(cat "$TMP_ERR")"
assert_contains "wt+cwd: stderr names both flags" "worktree-from" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Reverse order too — flag-parsing order shouldn't change the verdict.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    "$SPAWN" pi --cwd /tmp --worktree-from "$SHA" ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "cwd+wt (reverse): exits non-zero" 1 "$rc"
assert_contains "cwd+wt (reverse): stderr names the conflict" "mutually exclusive" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --worktree-from + --sesh-session: mutual-exclusion error ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    "$SPAWN" pi --worktree-from "$SHA" --sesh-session alpha ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "wt+sesh: exits non-zero" 1 "$rc"
assert_contains "wt+sesh: stderr names the conflict" "mutually exclusive" "$(cat "$TMP_ERR")"
assert_contains "wt+sesh: stderr names both flags" "sesh-session" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --slug only (no --worktree-from): label-only mode parses ==="

# --slug without --worktree-from should NOT touch the filesystem (no
# worktree, no cwd change) — it's a pure-identity flag. We force exit
# via the outfit guard on agent=pi as before.
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    ORCH_ALIASES_FILE="$ALIASES_FILE" \
    "$SPAWN" pi --slug tag-only --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "slug-only: exits non-zero (outfit guard fires post-resolution)" 1 "$rc"
assert_contains "slug-only: stderr is outfit error" "claude" "$(cat "$TMP_ERR")"
# Crucially, the outfit error should NOT mention worktree-related noise.
ERR=$(cat "$TMP_ERR")
if [[ "$ERR" == *"worktree"* ]]; then
    echo "  FAIL  slug-only: stderr should NOT mention worktree (none was requested)"
    echo "        got: $ERR"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("slug-only: stderr leaked worktree mention")
else
    echo "  PASS  slug-only: stderr did not mention worktree"
    PASS=$((PASS + 1))
fi
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --slug: rejects unsafe characters ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    "$SPAWN" pi --slug 'has space' --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "slug-unsafe: exits non-zero" 1 "$rc"
assert_contains "slug-unsafe: stderr explains" "--slug" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== --worktree-from without --slug: auto-generates slug ==="

# Auto-slug shape: <sha7>-<rand4>. We can't predict the rand4 portion,
# but we can assert the worktree root contains exactly one new dir
# whose name starts with <sha7>-.
EMPTY_WT_ROOT=$(mktemp -d)
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    ORCH_WORKTREE_ROOT="$EMPTY_WT_ROOT" ORCH_ALIASES_FILE="$ALIASES_FILE" \
    "$SPAWN" pi --worktree-from "$SHA" --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "auto-slug: exits non-zero (outfit guard fires post-resolution)" 1 "$rc"
# Look for a dir under EMPTY_WT_ROOT whose name starts with <sha7>-.
FOUND_DIR=""
for d in "$EMPTY_WT_ROOT"/*; do
    [ -d "$d" ] || continue
    BASE=$(basename "$d")
    case "$BASE" in
        "${SHA7}-"????) FOUND_DIR=$d; break ;;
    esac
done
if [ -n "$FOUND_DIR" ]; then
    echo "  PASS  auto-slug: created dir matches <sha7>-<4hex>: $(basename "$FOUND_DIR")"
    PASS=$((PASS + 1))
else
    echo "  FAIL  auto-slug: no <sha7>-<4hex> dir under $EMPTY_WT_ROOT"
    echo "        contents: $(ls "$EMPTY_WT_ROOT" 2>/dev/null || echo '<empty>')"
    FAIL=$((FAIL + 1))
    FAILED_TESTS+=("auto-slug: dir not found")
fi
rm -f "$TMP_OUT" "$TMP_ERR"
rm -rf "$EMPTY_WT_ROOT"

echo
echo "=== --worktree-from: existing target dir is rejected ==="

mkdir -p "$WORKTREE_ROOT/already-there"
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$REPO" && \
    ORCH_WORKTREE_ROOT="$WORKTREE_ROOT" ORCH_ALIASES_FILE="$ALIASES_FILE" \
    "$SPAWN" pi --worktree-from "$SHA" --slug already-there --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "existing-dir: exits non-zero" 1 "$rc"
assert_contains "existing-dir: stderr names the conflict" "already exists" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"
rm -rf "$WORKTREE_ROOT/already-there"

echo
echo "=== --worktree-from: not a git repo surfaces clear error ==="

NONREPO=$(mktemp -d)
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
( cd "$NONREPO" && \
    ORCH_WORKTREE_ROOT="$WORKTREE_ROOT" \
    "$SPAWN" pi --worktree-from "$SHA" --slug whatever --outfit engineer ) \
    >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "no-repo: exits non-zero" 1 "$rc"
assert_contains "no-repo: stderr explains" "git repository" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"
rm -rf "$NONREPO"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
