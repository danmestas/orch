#!/usr/bin/env bash
# Regression tests for `orch-bundle-gc`.
#
# Validates:
#   - Default age (30m) lists only stale suit-*/orch-bundle-* dirs.
#   - --age overrides the cutoff.
#   - Unrelated dirs (not matching the name patterns) are ignored.
#   - --clean removes the listed set; without --clean nothing is touched.
#   - Fresh dirs (mtime < cutoff) are never listed or removed.
#   - Bad --age values are rejected at parse time.
#
# Run with: bash test/test-orch-bundle-gc.sh
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

assert_not_contains() {
    local desc=$1 substr=$2 haystack=$3
    if [[ "$haystack" != *"$substr"* ]]; then
        echo "  PASS  $desc"
        PASS=$((PASS + 1))
    else
        echo "  FAIL  $desc"
        echo "        expected NOT to contain: $substr"
        echo "        got:                     $haystack"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

GC=$(command -v orch-bundle-gc)
[ -x "$GC" ] || { echo "orch-bundle-gc missing on PATH"; exit 2; }

# Sandbox $TMPDIR — bundle-gc respects it.
SANDBOX=$(mktemp -d)
export TMPDIR="$SANDBOX"
trap 'rm -rf "$SANDBOX"' EXIT

# Fixtures:
#   suit-stale, orch-bundle-stale     → mtime 60m ago (should be listed at age 30)
#   suit-fresh, orch-bundle-fresh     → mtime now (never listed at age 30)
#   unrelated-tmp                     → never listed (name doesn't match)
mkdir -p "$SANDBOX/suit-stale"        "$SANDBOX/orch-bundle-stale"
mkdir -p "$SANDBOX/suit-fresh"        "$SANDBOX/orch-bundle-fresh"
mkdir -p "$SANDBOX/unrelated-tmp"
echo "content" > "$SANDBOX/suit-stale/marker"
echo "content" > "$SANDBOX/orch-bundle-stale/marker"
echo "content" > "$SANDBOX/suit-fresh/marker"
echo "content" > "$SANDBOX/orch-bundle-fresh/marker"
echo "content" > "$SANDBOX/unrelated-tmp/marker"

# Backdate the "stale" dirs by 60 minutes. touch -t YYYYMMDDhhmm[.SS]
# accepts an absolute timestamp; build one 60 minutes in the past.
backdate() {
    local d=$1
    if date -v-60M +%Y%m%d%H%M >/dev/null 2>&1; then
        # BSD date (macOS)
        local ts; ts=$(date -v-60M +%Y%m%d%H%M)
        touch -t "$ts" "$d"
    else
        # GNU date
        local ts; ts=$(date -d '60 minutes ago' +%Y%m%d%H%M)
        touch -t "$ts" "$d"
    fi
}
backdate "$SANDBOX/suit-stale"
backdate "$SANDBOX/orch-bundle-stale"

echo "=== default age 30m: only stale matches listed ==="

OUT=$("$GC" 2>&1)
assert_contains "default-age: lists suit-stale" "suit-stale" "$OUT"
assert_contains "default-age: lists orch-bundle-stale" "orch-bundle-stale" "$OUT"
assert_not_contains "default-age: skips suit-fresh" "suit-fresh" "$OUT"
assert_not_contains "default-age: skips orch-bundle-fresh" "orch-bundle-fresh" "$OUT"
assert_not_contains "default-age: skips unrelated-tmp" "unrelated-tmp" "$OUT"

echo
echo "=== --age 120 (longer than fixture age): no matches ==="

OUT=$("$GC" --age 120 2>&1)
assert_contains "age-120: friendly no-match message" "no matches" "$OUT"

echo
echo "=== --age 10 catches the 60m-old fixtures, fresh still skipped ==="

OUT=$("$GC" --age 10 2>&1)
assert_contains "age-10: lists suit-stale" "suit-stale" "$OUT"
assert_contains "age-10: lists orch-bundle-stale" "orch-bundle-stale" "$OUT"
assert_not_contains "age-10: skips suit-fresh" "suit-fresh" "$OUT"
assert_not_contains "age-10: still ignores unrelated-tmp" "unrelated-tmp" "$OUT"

echo
echo "=== --clean removes stale, leaves fresh + unrelated alone ==="

"$GC" --clean >/dev/null 2>&1
[ ! -d "$SANDBOX/suit-stale" ]            && d1=gone || d1=present
[ ! -d "$SANDBOX/orch-bundle-stale" ]     && d2=gone || d2=present
[ -d "$SANDBOX/suit-fresh" ]              && d3=present || d3=gone
[ -d "$SANDBOX/orch-bundle-fresh" ]       && d4=present || d4=gone
[ -d "$SANDBOX/unrelated-tmp" ]           && d5=present || d5=gone
assert "clean: suit-stale removed" "gone" "$d1"
assert "clean: orch-bundle-stale removed" "gone" "$d2"
assert "clean: suit-fresh preserved" "present" "$d3"
assert "clean: orch-bundle-fresh preserved" "present" "$d4"
assert "clean: unrelated-tmp preserved" "present" "$d5"

echo
echo "=== bad --age rejected at parse ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$GC" --age forty-five >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "bad-age: rc=1" 1 "$rc"
assert_contains "bad-age: stderr names the bad value" "forty-five" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
