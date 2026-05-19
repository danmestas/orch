#!/usr/bin/env bash
# Regression tests for orch-stack.
#
# Builds throwaway git repos in a sandbox (one for the "remote", one for
# the "local" with branches stacked on top of each other) and exercises
# the list / base / push / land subcommands.
#
# Run with: bash test/test-orch-stack.sh
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
        echo "        got (head): $(printf '%s' "$haystack" | head -c 250)"
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
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

# Locate orch-stack. Prefer the working tree (so test reflects local edits),
# fall back to PATH.
REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
STACK="$REPO_ROOT/bin/orch-stack"
[ -x "$STACK" ] || STACK=$(command -v orch-stack || true)
[ -x "$STACK" ] || { echo "orch-stack not found"; exit 2; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

REMOTE="$SANDBOX/remote.git"
LOCAL="$SANDBOX/local"

# Quiet, deterministic git.
export GIT_AUTHOR_NAME=test
export GIT_AUTHOR_EMAIL=test@example.com
export GIT_COMMITTER_NAME=test
export GIT_COMMITTER_EMAIL=test@example.com

git init --quiet --bare "$REMOTE"
git -c init.defaultBranch=main init --quiet "$LOCAL"
cd "$LOCAL"
git checkout --quiet -b main
echo seed > a.txt
git add a.txt
git -c commit.gpgsign=false commit --quiet -m "seed"
git remote add origin "$REMOTE"
git push --quiet -u origin main

# Build a stack: main → bottom → mid → top
git checkout --quiet -b bottom main
echo bottom > b.txt
git add b.txt
git -c commit.gpgsign=false commit --quiet -m "bottom"

git checkout --quiet -b mid
echo mid > m.txt
git add m.txt
git -c commit.gpgsign=false commit --quiet -m "mid"

git checkout --quiet -b top
echo top > t.txt
git add t.txt
git -c commit.gpgsign=false commit --quiet -m "top"

echo
echo "=== T1: base + list ==="

git checkout --quiet bottom
"$STACK" base main >/dev/null
git checkout --quiet mid
"$STACK" base bottom >/dev/null
git checkout --quiet top
"$STACK" base mid >/dev/null

OUT=$("$STACK" list 2>&1)
assert_contains "list shows bottom" "bottom" "$OUT"
assert_contains "list shows mid" "mid" "$OUT"
assert_contains "list shows top  (HEAD)" "top  (HEAD)" "$OUT"
assert_contains "list shows main as root" "main" "$OUT"

echo
echo "=== T2: list when no parent recorded ==="

git checkout --quiet -b orphan main
echo orphan > o.txt
git add o.txt
git -c commit.gpgsign=false commit --quiet -m "orphan"
OUT=$("$STACK" list 2>&1)
assert_contains "list: notes missing parent" "no recorded parent" "$OUT"
assert_contains "list: still prints orphan as HEAD" "orphan  (HEAD)" "$OUT"

echo
echo "=== T3: base refuses self-parent + missing-branch ==="

git checkout --quiet top
OUT=$("$STACK" base top 2>&1) && rc=0 || rc=$?
assert "base self: rc=1" 1 "$rc"
assert_contains "base self: stderr names error" "own parent" "$OUT"

OUT=$("$STACK" base nope-no-branch 2>&1) && rc=0 || rc=$?
assert "base bad branch: rc=1" 1 "$rc"
assert_contains "base bad branch: stderr names error" "does not exist" "$OUT"

echo
echo "=== T4: base refuses main ==="

git checkout --quiet main
OUT=$("$STACK" base bottom 2>&1) && rc=0 || rc=$?
assert "base on main: rc=1" 1 "$rc"
assert_contains "base on main: stderr names error" "refusing to mark main" "$OUT"

echo
echo "=== T5: push rebases + pushes the stack in order ==="

# Advance main so each rebase actually has work to do.
git checkout --quiet main
echo more-seed > c.txt
git add c.txt
git -c commit.gpgsign=false commit --quiet -m "advance main"
git push --quiet origin main

git checkout --quiet top
OUT=$("$STACK" push 2>&1)
rc=$?
assert "push: rc=0" 0 "$rc"
assert_contains "push: rebases bottom onto origin/main" "rebasing bottom onto origin/main" "$OUT"
assert_contains "push: rebases mid onto bottom" "rebasing mid onto bottom" "$OUT"
assert_contains "push: rebases top onto mid" "rebasing top onto mid" "$OUT"
assert_contains "push: pushes bottom" "pushing bottom" "$OUT"
assert_contains "push: pushes top" "pushing top" "$OUT"

# Verify remote got all three branches.
REMOTE_BRANCHES=$(git --git-dir="$REMOTE" for-each-ref --format='%(refname:short)' refs/heads/)
assert_contains "remote has bottom" "bottom" "$REMOTE_BRANCHES"
assert_contains "remote has mid" "mid" "$REMOTE_BRANCHES"
assert_contains "remote has top" "top" "$REMOTE_BRANCHES"

# Verify linear history: top contains main's tip, bottom's tip, mid's tip.
TOP_LOG=$(git log --oneline top)
assert_contains "top history contains advance main" "advance main" "$TOP_LOG"
assert_contains "top history contains bottom commit" "bottom" "$TOP_LOG"
assert_contains "top history contains mid commit" "mid" "$TOP_LOG"
assert_contains "top history contains top commit" "top" "$TOP_LOG"

echo
echo "=== T6: push with explicit positional args ==="

# Build a second stack with no recorded parents.
git checkout --quiet -b e1 main
echo e1 > e1.txt
git add e1.txt
git -c commit.gpgsign=false commit --quiet -m "e1"
git checkout --quiet -b e2
echo e2 > e2.txt
git add e2.txt
git -c commit.gpgsign=false commit --quiet -m "e2"
git checkout --quiet -b e3
echo e3 > e3.txt
git add e3.txt
git -c commit.gpgsign=false commit --quiet -m "e3"

# Bump main again.
git checkout --quiet main
echo more > d.txt
git add d.txt
git -c commit.gpgsign=false commit --quiet -m "advance main 2"
git push --quiet origin main

OUT=$("$STACK" push e1 e2 e3 2>&1)
rc=$?
assert "explicit-stack push: rc=0" 0 "$rc"
assert_contains "explicit: rebases e1 onto origin/main" "rebasing e1 onto origin/main" "$OUT"
assert_contains "explicit: rebases e2 onto e1" "rebasing e2 onto e1" "$OUT"
assert_contains "explicit: rebases e3 onto e2" "rebasing e3 onto e2" "$OUT"

# Parent relationships should now be recorded.
P_E2=$(git config --get branch.e2.orchStackParent)
P_E3=$(git config --get branch.e3.orchStackParent)
assert "explicit: e2 parent recorded" "e1" "$P_E2"
assert "explicit: e3 parent recorded" "e2" "$P_E3"

echo
echo "=== T7: push aborts cleanly on conflict ==="

# Create branches that conflict with each other.
git checkout --quiet main
git checkout --quiet -b conf-bottom main
echo bottom-version > shared.txt
git add shared.txt
git -c commit.gpgsign=false commit --quiet -m "conf-bottom"

git checkout --quiet -b conf-top main
echo top-version > shared.txt
git add shared.txt
git -c commit.gpgsign=false commit --quiet -m "conf-top"

OUT=$("$STACK" push conf-bottom conf-top 2>&1) && rc=0 || rc=$?
assert "conflict push: rc=3" 3 "$rc"
assert_contains "conflict: names the failing branch" "conf-top" "$OUT"
assert_contains "conflict: prints recovery hint" "resolve manually" "$OUT"

# Worktree should be clean (rebase aborted).
git rebase --abort 2>/dev/null || true
git checkout --quiet main 2>/dev/null || true

echo
echo "=== T8: dirty worktree refused ==="

git checkout --quiet top
echo dirty >> a.txt
OUT=$("$STACK" push 2>&1) && rc=0 || rc=$?
assert "dirty push: rc=2" 2 "$rc"
assert_contains "dirty: stderr names the problem" "uncommitted changes" "$OUT"
git checkout --quiet -- a.txt

echo
echo "=== T9: list on a single-entry stack ==="

git checkout --quiet main
git checkout --quiet -b solo main
echo solo > s.txt
git add s.txt
git -c commit.gpgsign=false commit --quiet -m "solo"
"$STACK" base main >/dev/null
OUT=$("$STACK" list 2>&1)
assert_contains "list solo: HEAD shown" "solo  (HEAD)" "$OUT"

echo
echo "=== T10: bad subcommand rejected ==="

OUT=$("$STACK" frobnicate 2>&1) && rc=0 || rc=$?
assert "bad subcommand: rc=1" 1 "$rc"
assert_contains "bad subcommand: stderr names it" "unknown subcommand" "$OUT"

echo
echo "=== T11: help prints ==="

OUT=$("$STACK" --help 2>&1)
assert_contains "help mentions list" "list" "$OUT"
assert_contains "help mentions push" "push" "$OUT"
assert_contains "help mentions land" "land" "$OUT"
assert_contains "help mentions WHEN TO USE" "WHEN TO USE" "$OUT"

echo
echo "=== T12: cycle detection ==="

# Manually wire a cycle: c1 → c2 → c1.
git checkout --quiet main
git checkout --quiet -b c1 main
echo c1 > c1.txt
git add c1.txt
git -c commit.gpgsign=false commit --quiet -m "c1"
git checkout --quiet -b c2 c1
echo c2 > c2.txt
git add c2.txt
git -c commit.gpgsign=false commit --quiet -m "c2"
git config branch.c1.orchStackParent c2
git config branch.c2.orchStackParent c1
OUT=$("$STACK" list 2>&1) && rc=0 || rc=$?
assert "cycle: rc=1" 1 "$rc"
assert_contains "cycle: stderr names error" "cycle detected" "$OUT"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
