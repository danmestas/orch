#!/usr/bin/env bash
# Regression tests for orch-version (T4).
#
# Builds a synthetic project repo + live install pair in a sandbox, then
# exercises sync / drift / missing / symlink-farm code paths against it.
#
# Run with: bash test/test-orch-version.sh
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

VERSION=$(command -v orch-version)
[ -x "$VERSION" ] || { echo "orch-version not on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

# Synthetic project repo
PROJ="$SANDBOX/project"
mkdir -p "$PROJ/bin" "$PROJ/hooks" "$PROJ/skills/foo-skill" "$PROJ/skills/bar-skill"
echo '#!/bin/sh' > "$PROJ/bin/orch-fake-a"; chmod +x "$PROJ/bin/orch-fake-a"
echo '#!/bin/sh' > "$PROJ/bin/orch-fake-b"; chmod +x "$PROJ/bin/orch-fake-b"
echo 'echo hook' > "$PROJ/hooks/fake-hook.sh"; chmod +x "$PROJ/hooks/fake-hook.sh"
echo 'foo skill' > "$PROJ/skills/foo-skill/SKILL.md"
echo 'bar skill' > "$PROJ/skills/bar-skill/SKILL.md"

# Synthetic live install
LIVE_BIN="$SANDBOX/local-bin"
LIVE_HOOKS="$SANDBOX/claude-hooks"
LIVE_SKILLS="$SANDBOX/claude-skills"
mkdir -p "$LIVE_BIN" "$LIVE_HOOKS" "$LIVE_SKILLS"

export ORCH_PROJECT_DIR="$PROJ"
export ORCH_LOCAL_BIN="$LIVE_BIN"
export ORCH_CLAUDE_HOOKS="$LIVE_HOOKS"
export ORCH_CLAUDE_SKILLS="$LIVE_SKILLS"

echo "=== scenario 1: nothing installed → all missing ==="

# 1) rc=1 (drift detected because nothing installed)
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$VERSION" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "all-missing: rc=1" 1 "$rc"
assert_contains "all-missing: report shows 0 match" "0 match" "$(cat "$TMP_OUT")"
assert_contains "all-missing: report shows missing entries" "missing" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 2) --quiet on drift returns nonzero with no output
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$VERSION" --quiet >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "quiet+drift: rc=1" 1 "$rc"
assert "quiet+drift: stdout empty" "" "$(cat "$TMP_OUT")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 3) --json shape: an array of {kind,name,state,note}
JSON_OUT=$("$VERSION" --json 2>/dev/null) || true
COUNT=$(printf '%s' "$JSON_OUT" | jq 'length')
assert "json: 5 items (2 bin + 1 hook + 2 skill)" 5 "$COUNT"
N_MISS=$(printf '%s' "$JSON_OUT" | jq '[.[] | select(.state=="missing")] | length')
assert "json: all 5 missing" 5 "$N_MISS"

echo
echo "=== scenario 2: copy-style install, all in sync ==="

cp "$PROJ/bin/orch-fake-a" "$LIVE_BIN/"
cp "$PROJ/bin/orch-fake-b" "$LIVE_BIN/"
cp "$PROJ/hooks/fake-hook.sh" "$LIVE_HOOKS/"
cp -R "$PROJ/skills/foo-skill" "$LIVE_SKILLS/"
cp -R "$PROJ/skills/bar-skill" "$LIVE_SKILLS/"

# 4) rc=0 when everything matches
"$VERSION" --quiet && rc=0 || rc=$?
assert "all-match: --quiet rc=0" 0 "$rc"

# 5) Report says match for all items
JSON_OUT=$("$VERSION" --json 2>/dev/null)
N_MATCH=$(printf '%s' "$JSON_OUT" | jq '[.[] | select(.state=="match")] | length')
assert "all-match: json 5 match" 5 "$N_MATCH"
N_DRIFT=$(printf '%s' "$JSON_OUT" | jq '[.[] | select(.state=="drift")] | length')
assert "all-match: 0 drift" 0 "$N_DRIFT"

# 6) Text report shows summary line
TEXT_OUT=$("$VERSION" 2>/dev/null)
assert_contains "all-match: text summary 5 match" "5 match" "$TEXT_OUT"

echo
echo "=== scenario 3: drift on one binary ==="

# Modify the live copy of orch-fake-a so it differs from project.
echo 'extra line' >> "$LIVE_BIN/orch-fake-a"

"$VERSION" --quiet && rc=0 || rc=$?
assert "drift-binary: --quiet rc=1" 1 "$rc"

JSON_OUT=$("$VERSION" --json 2>/dev/null)
ROW=$(printf '%s' "$JSON_OUT" | jq -c '.[] | select(.name=="orch-fake-a")')
DRIFT_STATE=$(printf '%s' "$ROW" | jq -r '.state')
assert "drift-binary: orch-fake-a state=drift" "drift" "$DRIFT_STATE"
DRIFT_NOTE=$(printf '%s' "$ROW" | jq -r '.note')
assert_contains "drift-binary: note mentions line delta" "live" "$DRIFT_NOTE"

# Restore for next scenario.
cp "$PROJ/bin/orch-fake-a" "$LIVE_BIN/orch-fake-a"

echo
echo "=== scenario 4: symlink farm — live points at project ==="

# Replace the live copy of foo-skill with a symlink to the project.
rm -rf "$LIVE_SKILLS/foo-skill"
ln -s "$PROJ/skills/foo-skill" "$LIVE_SKILLS/foo-skill"

"$VERSION" --quiet && rc=0 || rc=$?
assert "symlink-farm: --quiet rc=0" 0 "$rc"

JSON_OUT=$("$VERSION" --json 2>/dev/null)
ROW=$(printf '%s' "$JSON_OUT" | jq -c '.[] | select(.name=="foo-skill")')
S_STATE=$(printf '%s' "$ROW" | jq -r '.state')
S_NOTE=$(printf '%s' "$ROW" | jq -r '.note')
assert "symlink-farm: foo-skill state=match" "match" "$S_STATE"
assert_contains "symlink-farm: note says 'symlink → project'" "symlink" "$S_NOTE"

echo
echo "=== scenario 5: missing live entry for one skill ==="

rm -rf "$LIVE_SKILLS/bar-skill"

"$VERSION" --quiet && rc=0 || rc=$?
assert "missing-skill: --quiet rc=1" 1 "$rc"

JSON_OUT=$("$VERSION" --json 2>/dev/null)
ROW=$(printf '%s' "$JSON_OUT" | jq -c '.[] | select(.name=="bar-skill")')
M_STATE=$(printf '%s' "$ROW" | jq -r '.state')
assert "missing-skill: bar-skill state=missing" "missing" "$M_STATE"

echo
echo "=== scenario 6: hook drift via symlink to non-project ==="

# Restore bar-skill so we isolate the hook test.
cp -R "$PROJ/skills/bar-skill" "$LIVE_SKILLS/bar-skill"

OTHER=$(mktemp); chmod +x "$OTHER"
echo 'echo non-project hook' > "$OTHER"; chmod +x "$OTHER"
rm -f "$LIVE_HOOKS/fake-hook.sh"
ln -s "$OTHER" "$LIVE_HOOKS/fake-hook.sh"

"$VERSION" --quiet && rc=0 || rc=$?
assert "hook-symlink-elsewhere: --quiet rc=1" 1 "$rc"

JSON_OUT=$("$VERSION" --json 2>/dev/null)
ROW=$(printf '%s' "$JSON_OUT" | jq -c '.[] | select(.name=="fake-hook.sh")')
H_STATE=$(printf '%s' "$ROW" | jq -r '.state')
H_NOTE=$(printf '%s' "$ROW" | jq -r '.note')
assert "hook-symlink-elsewhere: state=drift" "drift" "$H_STATE"
assert_contains "hook-symlink-elsewhere: note names target" "$OTHER" "$H_NOTE"
rm -f "$OTHER"

echo
echo "=== scenario 7: usage error path ==="

TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$VERSION" --bogus >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "unknown-flag: rc=2" 2 "$rc"
assert_contains "unknown-flag: stderr names flag" "--bogus" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# Project dir missing → rc=2
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_PROJECT_DIR=/no/such/dir "$VERSION" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "missing-project: rc=2" 2 "$rc"
assert_contains "missing-project: stderr names path" "/no/such/dir" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
