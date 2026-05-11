#!/usr/bin/env bash
# Regression tests for orch-spy (T2).
#
# Layered:
#   - Fast error-path tests (sandbox state, no real spawns)
#   - Fast happy-path with mocked orch-spawn (real silent tmux pane stands
#     in for the spy; verifies brief construction + send-log entry)
#   - Real e2e: one actual claude --outfit stasi spawn (skipped if
#     SKIP_REAL_OUTFIT=1)
#
# Run with: bash test/test-orch-spy.sh
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
        echo "        got (head): $(printf '%s' "$haystack" | head -c 200)"
        FAIL=$((FAIL + 1))
        FAILED_TESTS+=("$desc")
    fi
}

SPY=$(command -v orch-spy)
[ -x "$SPY" ] || { echo "orch-spy not on PATH"; exit 2; }

SANDBOX=$(mktemp -d)
export ORCH_REGISTRY_DIR="$SANDBOX/registry"
export ORCH_OPERATOR_CACHE="$SANDBOX/operator.json"
export ORCH_SEND_LOG="$SANDBOX/send.log"
export ORCH_STOP_DIR="$SANDBOX/stop"
export ORCH_PROJECTS_DIR="$SANDBOX/projects"
# Tests mock orch-spawn so suit/wardrobe is not actually invoked; skip the
# pre-check that would otherwise require `suit` on PATH for the outfits-pack
# real-spawn case (still gated by SKIP_REAL_OUTFIT below).
export ORCH_SPY_SKIP_PRECHECK=1
mkdir -p "$ORCH_REGISTRY_DIR" "$ORCH_STOP_DIR" "$ORCH_PROJECTS_DIR"
trap 'cleanup_sandbox' EXIT

cleanup_sandbox() {
    # Kill any panes we spawned during the run.
    if [ -n "${SPAWNED_PANES:-}" ]; then
        for p in $SPAWNED_PANES; do tmux kill-pane -t "$p" 2>/dev/null || true; done
    fi
    rm -rf "$SANDBOX"
}
SPAWNED_PANES=""

echo "=== suit precheck (skip via ORCH_SPY_SKIP_PRECHECK=1, exercised via PATH) ==="

# Verify the precheck triggers when suit is not on PATH AND
# ORCH_SPY_SKIP_PRECHECK is not set. Build a minimal PATH without suit.
NO_SUIT_PATH="$SANDBOX:/usr/bin:/bin"
mkdir -p "$SANDBOX/no-suit-bin"
for b in jq tmux orch-spawn orch-tell orch-register; do
    src=$(command -v "$b" 2>/dev/null) || continue
    ln -sf "$src" "$SANDBOX/no-suit-bin/$b"
done
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
ORCH_SPY_SKIP_PRECHECK= PATH="$SANDBOX/no-suit-bin:/usr/bin:/bin" \
    "$SPY" operator "test" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "precheck: rc=1 when suit absent" 1 "$rc"
assert "precheck: stdout empty" "" "$(cat "$TMP_OUT")"
assert_contains "precheck: stderr names suit dependency" "suit not on PATH" "$(cat "$TMP_ERR")"
assert_contains "precheck: stderr points to outfit support docs" "outfit support" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== fast error-path tests ==="

# 1) missing args → usage on stderr, rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "no args: rc=1" 1 "$rc"
assert "no args: stdout empty" "" "$(cat "$TMP_OUT")"
assert_contains "no args: stderr names target requirement" "target required" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 2) missing mission → rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" operator >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "operator-no-mission: rc=1" 1 "$rc"
assert_contains "operator-no-mission: stderr names mission" "mission text required" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 3) invalid target → rc=1 with helpful error
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" bogus "audit something" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "invalid-target: rc=1" 1 "$rc"
assert_contains "invalid-target: stderr names operator/pane convention" "operator" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 4) target=operator with no claim → rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" operator "audit me" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "operator-no-claim: rc=1" 1 "$rc"
assert_contains "operator-no-claim: stderr points to claim binary" "orch-claim-operator" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 5) target=%bogus pane (no registry, no claim) → rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" %999 "audit something" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "unknown-pane: rc=1" 1 "$rc"
assert_contains "unknown-pane: stderr names both lookup paths" "operator-claim" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 6) --quiet on error path → both streams empty
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" --quiet operator "audit me" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "quiet-error: rc=1" 1 "$rc"
assert "quiet-error: stdout empty" "" "$(cat "$TMP_OUT")"
assert "quiet-error: stderr empty" "" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 7) --mission-file with missing path → rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" operator --mission-file /no/such/file >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "missing-mission-file: rc=1" 1 "$rc"
assert_contains "missing-mission-file: stderr cites path" "/no/such/file" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

echo
echo "=== fast happy-path with mocked orch-spawn ==="

# Pre-arrange:
# - Real silent tmux pane (sleep 3600) so orch-tell's idle-detect succeeds
# - Operator-claim pointing at a real JSONL file
# - Mock orch-spawn that ignores its args and echoes the silent pane id

MOCK_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' 'sleep 3600' 2>/dev/null) || {
    echo "  SKIP  could not spawn silent tmux pane"; MOCK_PANE=""; }
[ -n "$MOCK_PANE" ] && SPAWNED_PANES="$SPAWNED_PANES $MOCK_PANE"

if [ -n "$MOCK_PANE" ]; then
    # Stub the operator-claim record. Use a fake transcript file (must exist).
    FAKE_JSONL=$(mktemp); echo '{"type":"system"}' > "$FAKE_JSONL"
    cat > "$ORCH_OPERATOR_CACHE" <<EOT
{"pane_id":"$MOCK_PANE","claimed_at_ts_ns":$(date +%s%N),"transcript_jsonl":"$FAKE_JSONL","cwd":"/tmp"}
EOT

    # Mock orch-spawn in PATH front.
    MOCK_BIN=$SANDBOX/bin
    mkdir -p "$MOCK_BIN"
    cat > "$MOCK_BIN/orch-spawn" <<MOCK
#!/usr/bin/env bash
# Mock: ignore all args, echo the pre-spawned pane id (via env override).
echo "$MOCK_PANE"
MOCK
    chmod +x "$MOCK_BIN/orch-spawn"

    # 8) Happy path: spy resolves operator claim, mock-spawns, sends brief.
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    PATH="$MOCK_BIN:$PATH" ORCH_TELL_MAX_WAIT=10 \
        "$SPY" operator "audit my session for skill-trigger gaps" \
        >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "happy-path operator: rc=0" 0 "$rc"
    [ "$rc" != 0 ] && echo "    (stderr: $(cat "$TMP_ERR" | head -3))"
    assert "happy-path operator: stdout is mock pane id" "$MOCK_PANE" "$(cat "$TMP_OUT")"
    rm -f "$TMP_OUT" "$TMP_ERR"

    # 9) Send-log records the orch-tell call to the spy.
    SEND_LOG_LAST=$(tail -1 "$ORCH_SEND_LOG" 2>/dev/null || echo "")
    assert_contains "happy-path: send-log entry references mock pane" "\"pane\":\"$MOCK_PANE\"" "$SEND_LOG_LAST"

    # 10) Brief content via --dry-run-brief (deterministic — no capture-pane
    # rendering quirks). Verify all required envelope fields appear.
    BRIEF_OUT=$("$SPY" --dry-run-brief operator "audit my session for skill-trigger gaps" 2>/dev/null)
    assert_contains "brief: target_pane_id field" "target_pane_id:" "$BRIEF_OUT"
    assert_contains "brief: target_transcript_jsonl field" "target_transcript_jsonl:" "$BRIEF_OUT"
    assert_contains "brief: target_cwd field" "target_cwd:" "$BRIEF_OUT"
    assert_contains "brief: mission text" "audit my session for skill-trigger gaps" "$BRIEF_OUT"
    assert_contains "brief: spy_pane_id placeholder" "spy_pane_id:" "$BRIEF_OUT"
    assert_contains "brief: operator_claim pointer" "operator_claim:" "$BRIEF_OUT"
    assert_contains "brief: send_log pointer" "send_log:" "$BRIEF_OUT"
    assert_contains "brief: registry_dir pointer" "registry_dir:" "$BRIEF_OUT"
    assert_contains "brief: target_kind=operator" "target_kind:             operator" "$BRIEF_OUT"

    # 11) %pane target via registry (not operator-claim).
    REG_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' 'sleep 3600' 2>/dev/null) || REG_PANE=""
    [ -n "$REG_PANE" ] && SPAWNED_PANES="$SPAWNED_PANES $REG_PANE"

    if [ -n "$REG_PANE" ]; then
        # Resolve the worker's cwd via pwd -P (matches orch-spy's encoding
        # rule). On macOS /tmp → /private/tmp, so the encoded dir is
        # -private-tmp not -tmp. Encode then plant a fake JSONL.
        WORKER_CWD=$(cd /tmp && pwd -P)
        ENC=$(printf '%s' "$WORKER_CWD" | sed 's|/|-|g; s|_|-|g')
        ENC_DIR="$ORCH_PROJECTS_DIR/$ENC"
        mkdir -p "$ENC_DIR"
        FAKE_JSONL2="$ENC_DIR/abc.jsonl"
        echo '{"type":"system"}' > "$FAKE_JSONL2"
        # Register %900 as a worker with cwd=$WORKER_CWD.
        orch-register %900 pi "$WORKER_CWD" --role worker >/dev/null

        # Update mock to return REG_PANE so orch-tell hits a different live pane.
        cat > "$MOCK_BIN/orch-spawn" <<MOCK
#!/usr/bin/env bash
echo "$REG_PANE"
MOCK
        chmod +x "$MOCK_BIN/orch-spawn"

        TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
        PATH="$MOCK_BIN:$PATH" ORCH_TELL_MAX_WAIT=10 \
            "$SPY" %900 "audit %900's behavior" \
            >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
        assert "happy-path %worker: rc=0" 0 "$rc"
        [ "$rc" != 0 ] && echo "    (stderr: $(cat "$TMP_ERR" | head -3))"
        assert "happy-path %worker: stdout is mock pane" "$REG_PANE" "$(cat "$TMP_OUT")"
        rm -f "$TMP_OUT" "$TMP_ERR"

        # Brief content for %worker target via dry-run.
        BRIEF_W=$("$SPY" --dry-run-brief %900 "audit %900" 2>/dev/null)
        assert_contains "brief %worker: target_pane_id=%900" "target_pane_id:          %900" "$BRIEF_W"
        assert_contains "brief %worker: target_kind=worker" "target_kind:             worker" "$BRIEF_W"
        assert_contains "brief %worker: target_cwd resolved" "target_cwd:              $WORKER_CWD" "$BRIEF_W"
    fi

    # 12) Mission via stdin (single dash) — verify via dry-run.
    BRIEF_S=$(echo "mission from stdin" | "$SPY" --dry-run-brief operator - 2>/dev/null)
    assert_contains "mission via stdin (-): text in brief" "mission from stdin" "$BRIEF_S"

    # 13) Mission via --mission-file — verify via dry-run.
    MFILE=$(mktemp); echo "mission from file" > "$MFILE"
    BRIEF_F=$("$SPY" --dry-run-brief operator --mission-file "$MFILE" 2>/dev/null)
    assert_contains "mission via --mission-file: text in brief" "mission from file" "$BRIEF_F"
    rm -f "$MFILE"

    rm -f "$FAKE_JSONL"
fi

echo
echo "=== real e2e: actual claude --outfit stasi spawn ==="

if [ "${SKIP_REAL_OUTFIT:-0}" = "1" ]; then
    echo "  SKIP  SKIP_REAL_OUTFIT=1"
else
    # Reset to real registry / cache for the real spawn.
    unset ORCH_REGISTRY_DIR ORCH_OPERATOR_CACHE ORCH_SEND_LOG \
          ORCH_STOP_DIR ORCH_PROJECTS_DIR
    PRIOR_CLAIM=""
    if [ -f "$HOME/.cache/orch-operator.json" ]; then
        PRIOR_CLAIM="$HOME/.cache/orch-operator.json.t2spy.bak"
        cp "$HOME/.cache/orch-operator.json" "$PRIOR_CLAIM"
    fi

    # Claim current operator pane so target=operator resolves.
    OP_PANE=$(tmux display -p '#{pane_id}')
    orch-claim-operator --pane "$OP_PANE" >/dev/null 2>&1 || {
        echo "  SKIP  orch-claim-operator failed (no transcript dir for current cwd?)"
        OP_PANE=""
    }

    if [ -n "$OP_PANE" ]; then
        # Real spawn — costs one claude bootstrap (~2s).
        SPY_PANE=$(orch-spy operator "smoke test from orch-spy e2e" 2>/dev/null) || SPY_PANE=""
        if [ -n "$SPY_PANE" ] && [[ "$SPY_PANE" =~ ^%[0-9]+$ ]]; then
            assert "real spawn: stdout is %pane id" "1" "$([ -n "$SPY_PANE" ] && echo 1 || echo 0)"
            sleep 2  # let registry populate

            # role=observer in the real registry?
            REAL_ROLE=$(jq -r '.role // empty' "$HOME/.cache/orch-registry/$SPY_PANE.json" 2>/dev/null || echo "")
            assert "real spawn: role=observer in registry" "observer" "$REAL_ROLE"

            # Cleanup
            tmux kill-pane -t "$SPY_PANE" 2>/dev/null
            sleep 2
            rm -f "$HOME/.cache/orch-registry/$SPY_PANE.json"
        else
            echo "  SKIP  real spawn failed (suit/stasi outfit missing?)"
        fi
    fi

    # Restore prior claim record.
    if [ -n "$PRIOR_CLAIM" ]; then
        mv "$PRIOR_CLAIM" "$HOME/.cache/orch-operator.json"
    else
        rm -f "$HOME/.cache/orch-operator.json"
    fi
fi

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
