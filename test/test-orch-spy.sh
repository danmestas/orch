#!/usr/bin/env bash
# Regression tests for orch-spy (T2).
#
# After issue #60 (retire ~/.cache/orch-registry in favor of $SRV.INFO.agents),
# orch-spy resolves targets via NATS service discovery. These tests install a
# synthetic `nats` CLI stub on PATH and drive its replies via a fixture file —
# no real nats-server is required.
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
export ORCH_SEND_LOG="$SANDBOX/send.log"
export ORCH_STOP_DIR="$SANDBOX/stop"
export ORCH_PROJECTS_DIR="$SANDBOX/projects"
# Tests mock orch-spawn so suit/wardrobe is not actually invoked; skip the
# pre-check that would otherwise require `suit` on PATH for the outfits-pack
# real-spawn case (still gated by SKIP_REAL_OUTFIT below).
export ORCH_SPY_SKIP_PRECHECK=1
# Tighter discovery timeout — the stub returns instantly.
export ORCH_SPY_DISCOVERY_TIMEOUT="0.5s"
export ORCH_TELL_DISCOVERY_TIMEOUT="0.5s"
mkdir -p "$ORCH_STOP_DIR" "$ORCH_PROJECTS_DIR"

cleanup_sandbox() {
    # Kill any panes we spawned during the run.
    if [ -n "${SPAWNED_PANES:-}" ]; then
        for p in $SPAWNED_PANES; do tmux kill-pane -t "$p" 2>/dev/null || true; done
    fi
    rm -rf "$SANDBOX"
}
trap 'cleanup_sandbox' EXIT
SPAWNED_PANES=""

# ── Synthetic $SRV.INFO.agents service ───────────────────────────────────────
#
# orch-spy / orch-tell shell out to `nats req '$SRV.INFO.agents' ''` to resolve
# target metadata. After issue #60, that's the only place pane metadata lives
# — no more ~/.cache/orch-registry or ~/.cache/orch-operator.json. We install a
# shell-script stub named `nats` at the front of PATH that emits canned replies
# in the wire format the real CLI uses. The fixture set is updated per-section
# by overwriting $NATS_STUB_FIXTURES (one JSON metadata object per line).
NATS_STUB_DIR="$SANDBOX/nats-bin"
NATS_STUB_FIXTURES="$SANDBOX/nats-fixtures.jsonl"
mkdir -p "$NATS_STUB_DIR"
: > "$NATS_STUB_FIXTURES"
cat > "$NATS_STUB_DIR/nats" <<STUB
#!/usr/bin/env bash
# Synthetic nats CLI for test-orch-spy. Replies to req '\$SRV.INFO.agents'
# from the live fixture file; other invocations no-op.
verb=""
for arg in "\$@"; do
    case "\$arg" in req) verb=req ;; esac
done
if [ "\$verb" = req ] && [ -s "$NATS_STUB_FIXTURES" ]; then
    i=0
    while IFS= read -r meta; do
        [ -n "\$meta" ] || continue
        i=\$((i + 1))
        printf 'Received on "\$SRV.INFO.agents.fake%d"\n' "\$i"
        # Emit each agent's full INFO response shape: metadata + endpoints[].
        # orch-tell's discovery filters by metadata.pane_id then extracts
        # endpoints[name=="prompt"].subject; without endpoints, discovery
        # always returns no-match and (post-#98, with the tmux fallback
        # removed) every orch-spy → orch-tell call becomes a hard error.
        printf '{"metadata":%s,"endpoints":[{"name":"prompt","subject":"agents.prompt.stub.fake.0"}]}\n' "\$meta"
    done < "$NATS_STUB_FIXTURES"
fi
exit 0
STUB
chmod +x "$NATS_STUB_DIR/nats"
export PATH="$NATS_STUB_DIR:$PATH"
export NATS_URL="nats://stub.invalid:4222"

# Helper: rewrite the fixture file from one or more "pane role cwd" tuples.
# Each tuple is a single string; whitespace-split into 3 fields.
# Usage: set_agents "%900 worker /tmp" "%901 operator /home/me"
set_agents() {
    : > "$NATS_STUB_FIXTURES"
    local entry pane role cwd
    for entry in "$@"; do
        # shellcheck disable=SC2086  # word splitting is intentional here
        set -- $entry
        pane=$1; role=$2; cwd=$3
        jq -nc --arg p "$pane" --arg r "$role" --arg c "$cwd" --arg a "claude" \
            '{pane_id:$p, role:$r, cwd:$c, agent:$a}' >> "$NATS_STUB_FIXTURES"
    done
}

echo "=== suit precheck (skip via ORCH_SPY_SKIP_PRECHECK=1, exercised via PATH) ==="

# Verify the precheck triggers when suit is not on PATH AND
# ORCH_SPY_SKIP_PRECHECK is not set. Build a minimal PATH without suit.
mkdir -p "$SANDBOX/no-suit-bin"
for b in jq tmux orch-spawn orch-tell nats; do
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

# 4) target=operator with no operator agent on the bus → rc=1
set_agents  # empty fixture
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" operator "audit me" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "operator-no-agent: rc=1" 1 "$rc"
assert_contains "operator-no-agent: stderr points to ORCH_ROLE=operator" "ORCH_ROLE=operator" "$(cat "$TMP_ERR")"
rm -f "$TMP_OUT" "$TMP_ERR"

# 5) target=%bogus pane (not registered on the bus) → rc=1
TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
"$SPY" %999 "audit something" >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
assert "unknown-pane: rc=1" 1 "$rc"
assert_contains "unknown-pane: stderr names SRV.INFO.agents" "SRV.INFO.agents" "$(cat "$TMP_ERR")"
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
# - Operator-agent fixture pointing at a real cwd with a planted JSONL
# - Mock orch-spawn that ignores its args and echoes the silent pane id

MOCK_PANE=$(tmux split-window -d -h -P -F '#{pane_id}' 'sleep 3600' 2>/dev/null) || {
    echo "  SKIP  could not spawn silent tmux pane"; MOCK_PANE=""; }
[ -n "$MOCK_PANE" ] && SPAWNED_PANES="$SPAWNED_PANES $MOCK_PANE"

if [ -n "$MOCK_PANE" ]; then
    # Plant a fake JSONL under ~/.claude/projects/<encoded cwd>/ — orch-spy
    # resolves the transcript by encoding metadata.cwd and looking inside.
    # On macOS /tmp → /private/tmp, so encode the resolved path.
    OP_CWD=$(cd /tmp && pwd -P)
    OP_ENC=$(printf '%s' "$OP_CWD" | sed 's|/|-|g; s|_|-|g')
    OP_DIR="$ORCH_PROJECTS_DIR/$OP_ENC"
    mkdir -p "$OP_DIR"
    FAKE_JSONL="$OP_DIR/operator.jsonl"
    echo '{"type":"system"}' > "$FAKE_JSONL"

    # Register the operator agent on the stub bus.
    set_agents "$MOCK_PANE operator $OP_CWD"

    # Mock orch-spawn in PATH front.
    MOCK_BIN=$SANDBOX/bin
    mkdir -p "$MOCK_BIN"
    cat > "$MOCK_BIN/orch-spawn" <<MOCK
#!/usr/bin/env bash
# Mock: ignore all args, echo the pre-spawned pane id (via env override).
echo "$MOCK_PANE"
MOCK
    chmod +x "$MOCK_BIN/orch-spawn"

    # 8) Happy path: spy resolves operator agent, mock-spawns, sends brief.
    TMP_OUT=$(mktemp); TMP_ERR=$(mktemp)
    PATH="$MOCK_BIN:$PATH" ORCH_TELL_MAX_WAIT=10 \
        "$SPY" operator "audit my session for skill-trigger gaps" \
        >"$TMP_OUT" 2>"$TMP_ERR" && rc=0 || rc=$?
    assert "happy-path operator: rc=0" 0 "$rc"
    [ "$rc" != 0 ] && echo "    (stderr: $(head -3 "$TMP_ERR"))"
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
    assert_contains "brief: agent_registry pointer" "agent_registry:" "$BRIEF_OUT"
    assert_contains "brief: send_log pointer" "send_log:" "$BRIEF_OUT"
    assert_contains "brief: target_kind=operator" "target_kind:             operator" "$BRIEF_OUT"

    # 11) %pane target via $SRV.INFO.agents (worker fixture).
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

        # Replace the operator fixture with operator + a %900 worker, plus
        # REG_PANE (the spawn-target). In production, orch-spawn registers
        # the new pane on the bus via the shim before orch-spy briefs it;
        # the mock here doesn't run a real shim, so we pre-register REG_PANE
        # in the fixture so orch-tell's discovery finds it.
        set_agents "$MOCK_PANE operator $OP_CWD" "%900 worker $WORKER_CWD" "$REG_PANE worker $WORKER_CWD"

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
        [ "$rc" != 0 ] && echo "    (stderr: $(head -3 "$TMP_ERR"))"
        assert "happy-path %worker: stdout is mock pane" "$REG_PANE" "$(cat "$TMP_OUT")"
        rm -f "$TMP_OUT" "$TMP_ERR"

        # Brief content for %worker target via dry-run.
        BRIEF_W=$("$SPY" --dry-run-brief %900 "audit %900" 2>/dev/null)
        assert_contains "brief %worker: target_pane_id=%900" "target_pane_id:          %900" "$BRIEF_W"
        assert_contains "brief %worker: target_kind=worker" "target_kind:             worker" "$BRIEF_W"
        assert_contains "brief %worker: target_cwd resolved" "target_cwd:              $WORKER_CWD" "$BRIEF_W"
    fi

    # 12) Mission via stdin (single dash) — verify via dry-run.
    # Reset to operator-only fixture.
    set_agents "$MOCK_PANE operator $OP_CWD"
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

# Real e2e requires a real NATS server + nats CLI + the shim binary. The
# pre-migration version of this test wrote ~/.cache/orch-operator.json and
# checked ~/.cache/orch-registry/<pane>.json — neither exists post-#60. The
# corresponding new contract (operator agent on $SRV.INFO.agents, spy agent
# auto-registers with role=observer) requires the orchestrator's own shim
# to be running, which is out of scope for unit-test infrastructure.
#
# Always skip — the path is covered by hand-run validation on the
# orchestrator host.
echo "  SKIP  real e2e requires live NATS + shim setup (post-#60); covered by hand validation"

echo
echo "Results: $PASS passed, $FAIL failed"
if [ $FAIL -gt 0 ]; then
    echo "Failed tests:"
    for t in "${FAILED_TESTS[@]}"; do echo "  - $t"; done
    exit 1
fi
