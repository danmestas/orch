#!/usr/bin/env bash
#
# Regression tests for orch-current-jsonl + orch-session-jsonl hook (#39).
#
# Validates:
#   - The SessionStart hook reads transcript_path + session_id from a
#     stdin JSON payload and writes a deterministic per-pane sidecar
#     mapping at $ORCH_SESSIONS_DIR/<pane-id>.json.
#   - The resolver looks up the mapping by explicit pane id, by
#     $TMUX_PANE, and by $ORCH_PANE_ID — in that precedence.
#   - With no mapping, the resolver fails loudly (exit !=0 + diagnostic);
#     no silent guessing. --guess opts in to the most-recently-modified
#     fallback for legacy / non-orch-spawned sessions.
#   - --gc prunes mappings whose tmux pane no longer exists.
#
# Does NOT require tmux to run (only the --guess path queries tmux, and
# it falls back to pwd when tmux is unavailable).

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

ORCH_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
HOOK="$ORCH_ROOT/hooks/orch-session-jsonl.sh"
HELPER="$ORCH_ROOT/bin/orch-current-jsonl"

[ -x "$HOOK" ]   || { echo "hook missing or not executable: $HOOK"; exit 2; }
[ -x "$HELPER" ] || { echo "helper missing or not executable: $HELPER"; exit 2; }

command -v jq >/dev/null 2>&1 || { echo "skip: jq required for these tests"; exit 0; }

SANDBOX=$(mktemp -d)
trap 'rm -rf "$SANDBOX"' EXIT

export ORCH_SESSIONS_DIR="$SANDBOX/sessions"
export ORCH_PROJECTS_DIR="$SANDBOX/claude-projects"

echo "=== hook records mapping from SessionStart stdin payload ==="

# 1) Hook with ORCH_PANE_ID set + valid JSON → sidecar file appears.
FAKE_TRANSCRIPT="$SANDBOX/some/path/abc-123.jsonl"
PAYLOAD='{"session_id":"sess-abc","transcript_path":"'"$FAKE_TRANSCRIPT"'"}'

ORCH_PANE_ID="%999" bash "$HOOK" <<<"$PAYLOAD"
rc=$?
assert "hook: rc=0 on happy path" 0 "$rc"
assert "hook: sidecar file created" "yes" "$([ -f "$ORCH_SESSIONS_DIR/%999.json" ] && echo yes || echo no)"

# Sidecar content correctness via the helper (jq parses it).
if [ -f "$ORCH_SESSIONS_DIR/%999.json" ]; then
    stored_path=$(jq -r '.transcript_path' "$ORCH_SESSIONS_DIR/%999.json")
    stored_pane=$(jq -r '.pane_id' "$ORCH_SESSIONS_DIR/%999.json")
    stored_sid=$(jq -r '.session_id' "$ORCH_SESSIONS_DIR/%999.json")
    assert "hook: transcript_path persisted"  "$FAKE_TRANSCRIPT" "$stored_path"
    assert "hook: pane_id persisted"          "%999"             "$stored_pane"
    assert "hook: session_id persisted"       "sess-abc"         "$stored_sid"
fi

# 2) Hook with ORCH_PANE_ID unset but TMUX_PANE set → keys by TMUX_PANE.
ANOTHER_TRANSCRIPT="$SANDBOX/some/path/def-456.jsonl"
PAYLOAD2='{"session_id":"sess-def","transcript_path":"'"$ANOTHER_TRANSCRIPT"'"}'
(unset ORCH_PANE_ID; TMUX_PANE="%888" bash "$HOOK" <<<"$PAYLOAD2")
assert "hook: TMUX_PANE fallback keys correctly" "yes" \
    "$([ -f "$ORCH_SESSIONS_DIR/%888.json" ] && echo yes || echo no)"

# 3) Hook with no pane id available → silent no-op.
BEFORE=$(ls "$ORCH_SESSIONS_DIR" 2>/dev/null | wc -l)
(unset ORCH_PANE_ID; unset TMUX_PANE; bash "$HOOK" <<<"$PAYLOAD")
AFTER=$(ls "$ORCH_SESSIONS_DIR" 2>/dev/null | wc -l)
assert "hook: no pane id → no new mapping (count unchanged)" "$BEFORE" "$AFTER"

# 4) Hook with no transcript_path → silent no-op.
NO_TRANSCRIPT_PAYLOAD='{"session_id":"sess-xyz"}'
BEFORE=$(ls "$ORCH_SESSIONS_DIR" 2>/dev/null | wc -l)
ORCH_PANE_ID="%777" bash "$HOOK" <<<"$NO_TRANSCRIPT_PAYLOAD"
AFTER=$(ls "$ORCH_SESSIONS_DIR" 2>/dev/null | wc -l)
assert "hook: missing transcript_path → no new mapping" "$BEFORE" "$AFTER"

echo
echo "=== resolver looks up mapping by pane id ==="

# 5) Explicit pane id arg.
got=$("$HELPER" "%999")
assert "resolver: <pane-id> arg returns mapped transcript path" "$FAKE_TRANSCRIPT" "$got"

# 6) Defaults to $TMUX_PANE when no arg.
got=$(TMUX_PANE="%888" "$HELPER")
assert "resolver: defaults to \$TMUX_PANE when no arg" "$ANOTHER_TRANSCRIPT" "$got"

# 7) Defaults to $ORCH_PANE_ID when neither arg nor $TMUX_PANE.
got=$(unset TMUX_PANE; ORCH_PANE_ID="%999" "$HELPER" 2>/dev/null)
# (TMUX_PANE takes precedence by spec; with TMUX_PANE unset, ORCH_PANE_ID
# fills in.)
assert "resolver: falls back to \$ORCH_PANE_ID when \$TMUX_PANE unset" "$FAKE_TRANSCRIPT" "$got"

# 8) No mapping → exit non-zero, diagnostic on stderr.
TMP_ERR=$(mktemp)
"$HELPER" "%404" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "resolver: nonexistent pane → exit non-zero" 1 "$rc"
assert_contains "resolver: nonexistent pane → diagnostic names pane" "%404" "$(cat "$TMP_ERR")"
rm -f "$TMP_ERR"

# 9) No pane id available at all → exit non-zero with diagnostic.
TMP_ERR=$(mktemp)
(unset TMUX_PANE; unset ORCH_PANE_ID; "$HELPER" 2>"$TMP_ERR" && rc=0 || rc=$?)
assert "resolver: no pane id → exit non-zero" 1 "$rc"
assert_contains "resolver: no pane id → mentions TMUX_PANE / ORCH_PANE_ID" "TMUX_PANE" "$(cat "$TMP_ERR")"
rm -f "$TMP_ERR"

echo
echo "=== resolver --guess fallback ==="

# 10) With no mapping but a real claude project dir + jsonl, --guess
# returns the most-recently-modified .jsonl. Match the helper's
# `cd && pwd -P` symlink resolution (on macOS /var/... → /private/var/...)
# when computing the expected encoded path.
PROJ_DIR=$(mktemp -d "$SANDBOX/proj-XXX")
PROJ_DIR_RESOLVED=$(cd "$PROJ_DIR" && pwd -P)
PROJ_ENCODED=$(printf '%s' "$PROJ_DIR_RESOLVED" | sed 's|/|-|g; s|_|-|g')
mkdir -p "$ORCH_PROJECTS_DIR/$PROJ_ENCODED"
touch -t 202001010101 "$ORCH_PROJECTS_DIR/$PROJ_ENCODED/old.jsonl"
touch -t 202501010101 "$ORCH_PROJECTS_DIR/$PROJ_ENCODED/newest.jsonl"
got=$(cd "$PROJ_DIR" && "$HELPER" --guess "%no-such-pane" 2>/dev/null || true)
assert "resolver: --guess returns newest jsonl in encoded project dir" \
    "$ORCH_PROJECTS_DIR/$PROJ_ENCODED/newest.jsonl" "$got"

echo
echo "=== resolver --gc prunes mappings for dead panes ==="

# 11) Pre-populate a mapping for a pane that doesn't exist in tmux, then
# run --gc. If tmux isn't available, --gc treats "no live panes" as
# "everything is dead" and removes all mappings — for this test that's
# the right outcome.
DEAD_BEFORE="$ORCH_SESSIONS_DIR/%111.json"
echo '{"pane_id":"%111","transcript_path":"/tmp/dead.jsonl","session_id":"x","started_at":0}' \
    > "$DEAD_BEFORE"
"$HELPER" --gc
if [ -f "$DEAD_BEFORE" ]; then
    gc_state="not-pruned"
else
    gc_state="pruned"
fi
assert "resolver: --gc removed stale %111 mapping" "pruned" "$gc_state"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
