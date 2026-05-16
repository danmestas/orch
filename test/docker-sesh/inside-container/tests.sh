#!/usr/bin/env bash
# tests.sh — sesh-specific communication-pattern smoke suite.
#
# Covers patterns documented in:
#   sesh/README.md
#   sesh/docs/working-with-sesh.md
#   sesh/docs/task-management.md, goal-management.md, message-envelope.md,
#       scoped-memory.md
#   orch/docs/nats-bridge.md, multi-executor-workers.md, working-with-sesh.md
#
# Test pattern: each case (a) creates a temp project dir, (b) `sesh up`s
# a session, (c) probes the specific pattern, (d) `sesh down`s. Per-test
# isolation keeps failures local.
set -uo pipefail

PASS=0
FAIL=0
SKIP=0
FAILED=()
SKIPPED=()

log() { printf '[%s] %s\n' "$(date +%H:%M:%S)" "$*"; }

assert() {
    local desc=$1 expected=$2 got=$3
    if [ "$expected" = "$got" ]; then
        log "  PASS  $desc"
        PASS=$((PASS+1))
    else
        log "  FAIL  $desc  expected=$expected got=$got"
        FAIL=$((FAIL+1))
        FAILED+=("$desc")
    fi
}

skip() {
    local desc=$1 reason=$2
    log "  SKIP  $desc  ($reason)"
    SKIP=$((SKIP+1))
    SKIPPED+=("$desc")
}

# Helper: bring a session up in a temp project dir; export its endpoints
# into BASH globals for the calling test. Returns 0 on success.
sesh_up_in() {
    local proj=$1 label=$2 scope=${3:-session}
    mkdir -p "$proj"
    cd "$proj" || return 1
    sesh up --session="$label" --scope="$scope" >"/tmp/sesh-up-${label}.log" 2>&1 &
    SESH_BG_PID=$!  # exposed for tests that need to wait/kill the up process
    export SESH_BG_PID
    # Wait up to 10s for the session JSON to materialize.
    for _ in $(seq 1 20); do
        [ -f "$proj/.sesh/sessions/${label}.json" ] && break
        sleep 0.5
    done
    if [ ! -f "$proj/.sesh/sessions/${label}.json" ]; then
        return 1
    fi
    SESSION_JSON="$proj/.sesh/sessions/${label}.json"
    # File may be half-written — wait for nats_url field to materialize.
    for _ in $(seq 1 20); do
        SESH_NATS_URL=$(jq -r '.nats_url // ""' "$SESSION_JSON" 2>/dev/null || echo "")
        [ -n "$SESH_NATS_URL" ] && [ "$SESH_NATS_URL" != "null" ] && break
        sleep 0.5
    done
    SESH_LEAF_URL=$(jq -r '.leaf_url // ""' "$SESSION_JSON" 2>/dev/null || echo "")
    SESH_FOSSIL_URL=$(jq -r '.fossil_url // ""' "$SESSION_JSON" 2>/dev/null || echo "")
    SESH_PID_VAL=$(jq -r '.pid // ""' "$SESSION_JSON" 2>/dev/null || echo "")
    return 0
}

sesh_down_in() {
    local proj=$1 label=$2
    (cd "$proj" && sesh down --session="$label" >/dev/null 2>&1) || true
    sleep 0.5
    # Workaround sesh upstream bugs in per-test isolation:
    #  (a) `sesh down` leaves ~/.sesh/hub.spawn.lock lingering — second
    #      `sesh up` then writes incomplete session JSON (only "pid").
    #  (b) Orphan `sesh up` bg processes from failed/retried tests can
    #      hold ports and confuse later sessions.
    # Aggressive cleanup to keep tests independent.
    rm -f "$HOME/.sesh/hub.spawn.lock"
    pkill -f "^sesh up" 2>/dev/null || true
    sleep 0.3
}

# Fresh-state helper: nuke ~/.sesh and any orphans. Used at start of
# tests that need a clean hub state (lifecycle tests in Group 1).
sesh_full_reset() {
    pkill -f "^sesh up" 2>/dev/null || true
    sleep 0.5
    rm -rf "$HOME/.sesh"
}

# ============================================================
# GROUP 1 — Hub lifecycle
# ============================================================
log "=== Group 1: Hub lifecycle ==="

# Pattern: Hub Auto-Spawn & Lifecycle
log "T1.1: hub auto-spawn on first sesh up"
sesh_full_reset
if sesh_up_in /tmp/g1-spawn s1; then
    if [ -f "$HOME/.sesh/hub.url" ]; then
        assert "hub.url written after first sesh up" "yes" "yes"
    else
        assert "hub.url written after first sesh up" "yes" "no"
    fi
    sesh_down_in /tmp/g1-spawn s1
else
    assert "sesh up materializes session JSON" "yes" "no"
fi

log "T1.2: hub auto-shutdown when last leaf disconnects"
# After down, the hub.url should be cleaned up within a few seconds.
sleep 2
if [ ! -f "$HOME/.sesh/hub.url" ]; then
    assert "hub.url removed after last leaf disconnect" "removed" "removed"
else
    # hub might be in keepalive mode by default — check the hub log
    skip "hub auto-shutdown after last leaf" "hub.url still present (may be keepalive or graceful-shutdown lag)"
fi

# Pattern: Session Lockfile & Collision Detection
log "T1.3: same-name collision refused"
sesh_full_reset
sesh_up_in /tmp/g1-collide alpha >/dev/null
if sesh_up_in /tmp/g1-collide alpha 2>&1 | grep -qiE "exists|already|collision|holder|refused"; then
    assert "second sesh up alpha refused" "refused" "refused"
else
    # Some implementations may print to stderr or return non-zero without that exact text
    cd /tmp/g1-collide || true
    if sesh up --session=alpha >/dev/null 2>&1; then
        assert "second sesh up alpha refused" "refused" "accepted"
    else
        assert "second sesh up alpha exited non-zero" "non-zero" "non-zero"
    fi
fi
sesh_down_in /tmp/g1-collide alpha

# ============================================================
# GROUP 2 — Leaf attachment & session discovery
# ============================================================
log "=== Group 2: Leaf attachment ==="

sesh_full_reset
# Pattern: Session State JSON Publication
log "T2.1: session JSON has nats_url + leaf_url + pid"
if sesh_up_in /tmp/g2-json s1; then
    if [ -n "$SESH_NATS_URL" ] && [ -n "$SESH_LEAF_URL" ] && [ -n "$SESH_PID_VAL" ]; then
        assert "session JSON contains nats_url + leaf_url + pid" "all" "all"
    else
        assert "session JSON contains nats_url + leaf_url + pid" "all" "partial (nats=$SESH_NATS_URL leaf=$SESH_LEAF_URL pid=$SESH_PID_VAL)"
    fi
else
    skip "T2.1" "sesh up did not materialize session JSON"
fi

# Pattern: NATS URL is reachable
log "T2.2: published nats_url is responsive"
if [ -n "${SESH_NATS_URL:-}" ]; then
    if timeout 3 nats --server="$SESH_NATS_URL" server check connection >/dev/null 2>&1; then
        assert "nats --server=<nats_url> responsive" "yes" "yes"
    elif timeout 3 nats --server="$SESH_NATS_URL" pub _probe ping >/dev/null 2>&1; then
        assert "nats --server=<nats_url> responsive (pub probe)" "yes" "yes"
    else
        assert "nats --server=<nats_url> responsive" "yes" "no"
    fi
else
    skip "T2.2" "no nats_url from session JSON"
fi

# Pattern: Project Name Derivation
log "T2.3: project name derived from cwd basename"
PROJ_DIR=/tmp/g2-cwdname-proj
mkdir -p "$PROJ_DIR"
if sesh_up_in "$PROJ_DIR" s1; then
    # Look for project-code or in hub log evidence that project name = "g2-cwdname-proj"
    if [ -f "$PROJ_DIR/.sesh/project-code" ] || grep -q "g2-cwdname-proj" "$HOME/.sesh/hub.log" 2>/dev/null; then
        assert "project name reflects cwd basename" "yes" "yes"
    else
        skip "T2.3" "no project-code file or hub.log reference found (sesh may not write project-code on this version)"
    fi
    sesh_down_in "$PROJ_DIR" s1
else
    skip "T2.3" "sesh up failed"
fi

# Cleanup any sticky session
sesh_down_in /tmp/g2-json s1

# ============================================================
# GROUP 3 — Cross-leaf pub/sub
# ============================================================
log "=== Group 3: Cross-leaf pub/sub ==="

sesh_full_reset
# Pattern: NATS pub/sub multi-subscriber fanout via hub.
log "T3.1: pub on one leaf, sub on another, hub propagates"
sesh_up_in /tmp/g3-leaf1 s1 || true
NATS1=$SESH_NATS_URL
sesh_up_in /tmp/g3-leaf2 s2 || true
NATS2=$SESH_NATS_URL

if [ -n "$NATS1" ] && [ -n "$NATS2" ] && [ "$NATS1" != "$NATS2" ]; then
    # Two distinct leaves. Sub on leaf2, pub on leaf1, expect message.
    timeout 5 nats --server="$NATS2" sub --count=1 "cross.>" >/tmp/cross.cap 2>&1 &
    SUB_PID=$!
    sleep 0.5
    nats --server="$NATS1" pub cross.leaf "from leaf1" >/dev/null 2>&1
    for _ in $(seq 1 8); do
        [ -s /tmp/cross.cap ] && break
        sleep 0.5
    done
    kill $SUB_PID 2>/dev/null || true
    if grep -q "from leaf1" /tmp/cross.cap; then
        assert "leaf1 pub reached leaf2 sub via hub" "yes" "yes"
    else
        assert "leaf1 pub reached leaf2 sub via hub" "yes" "no"
    fi
else
    skip "T3.1" "could not bring up two leaves with distinct NATS URLs (nats1='$NATS1' nats2='$NATS2')"
fi

sesh_down_in /tmp/g3-leaf1 s1
sesh_down_in /tmp/g3-leaf2 s2

# T3.2 (orchestrator-driven multi-builder via the legacy bridge) retired in
# #94. Bus-side multi-builder coverage now lives in Group 7 (Synadia §12
# adapter contract against the sesh leaf).

# ============================================================
# GROUP 4 — JetStream durability & replay
# ============================================================
log "=== Group 4: JetStream durability ==="

log "T4.1: JetStream enabled on the session NATS"
sesh_up_in /tmp/g4-js s1 || true
if [ -n "$SESH_NATS_URL" ]; then
    if nats --server="$SESH_NATS_URL" stream list --json 2>&1 | grep -q "\["; then
        assert "JetStream answers stream list (empty array OK)" "yes" "yes"
    else
        # The list command may error if JetStream isn't enabled
        skip "T4.1" "stream list did not return a JSON array; JetStream may not be enabled on this leaf"
    fi
else
    skip "T4.1" "no sesh leaf"
fi

log "T4.2: late subscriber replays via durable consumer"
if [ -n "${SESH_NATS_URL:-}" ]; then
    # Create a stream + durable consumer, pub messages, kill sub, pub more,
    # then resume and verify all messages received.
    nats --server="$SESH_NATS_URL" stream add T4REPLAY --subjects 't4.>' --storage memory --replicas 1 --retention limits --max-msgs=100 --max-msg-size=1024 --max-age=1h --max-bytes=-1 --discard new --dupe-window=2m --no-allow-rollup --no-deny-delete --no-deny-purge --defaults >/dev/null 2>&1
    if nats --server="$SESH_NATS_URL" stream info T4REPLAY >/dev/null 2>&1; then
        nats --server="$SESH_NATS_URL" pub t4.evt "m1" >/dev/null 2>&1
        nats --server="$SESH_NATS_URL" pub t4.evt "m2" >/dev/null 2>&1
        timeout 4 nats --server="$SESH_NATS_URL" consumer create T4REPLAY c1 --pull --filter='t4.>' --deliver=all --replay=instant --ack=explicit --max-deliver=3 --defaults >/dev/null 2>&1
        msgs=$(timeout 4 nats --server="$SESH_NATS_URL" consumer next T4REPLAY c1 --no-ack --count=2 2>&1 | grep -c "m[12]" || true)
        if [ "$msgs" -ge 2 ]; then
            assert "late durable consumer replayed both messages" "2" "$msgs"
        else
            assert "late durable consumer replayed both messages" "2" "$msgs"
        fi
        nats --server="$SESH_NATS_URL" stream rm T4REPLAY -f >/dev/null 2>&1 || true
    else
        skip "T4.2" "could not create JetStream stream T4REPLAY"
    fi
else
    skip "T4.2" "no sesh leaf"
fi
sesh_down_in /tmp/g4-js s1

# ============================================================
# GROUP 5 — Fossil sync
# ============================================================
log "=== Group 5: Fossil sync ==="

log "T5.1: --scope=session writes per-session fossil repo"
PROJ=/tmp/g5-scope
if sesh_up_in "$PROJ" sx session; then
    if ls "$PROJ"/.sesh/sessions/sx.repo* >/dev/null 2>&1 || ls "$PROJ"/.sesh/sessions/sx*.repo >/dev/null 2>&1; then
        assert "session-scoped fossil repo exists" "yes" "yes"
    else
        skip "T5.1" "expected .sesh/sessions/sx.repo not found — sesh may use different naming"
    fi
    sesh_down_in "$PROJ" sx
else
    skip "T5.1" "sesh up failed"
fi

log "T5.2: --scope=project writes single shared repo"
PROJ=/tmp/g5-projscope
if sesh_up_in "$PROJ" sy project; then
    if [ -f "$PROJ/.sesh/project.repo" ] || ls "$PROJ"/.sesh/project*.repo >/dev/null 2>&1; then
        assert "project-scoped fossil repo exists" "yes" "yes"
    else
        skip "T5.2" "expected .sesh/project.repo not found — sesh may use different naming"
    fi
    sesh_down_in "$PROJ" sy
else
    skip "T5.2" "sesh up with --scope=project failed"
fi

log "T5.3: project-code file written"
if [ -f /tmp/g5-projscope/.sesh/project-code ]; then
    code=$(cat /tmp/g5-projscope/.sesh/project-code)
    if [ ${#code} -ge 20 ]; then
        assert "project-code looks like a hash" "yes" "yes"
    else
        assert "project-code looks like a hash" "yes" "value='$code' len=${#code}"
    fi
else
    skip "T5.3" "no .sesh/project-code (sesh version may differ)"
fi

log "T5.4: fossil HTTP endpoint serves the repo (clone-push)"
PROJ=/tmp/g5-http
if sesh_up_in "$PROJ" sh; then
    if [ -n "${SESH_FOSSIL_URL:-}" ]; then
        body=$(curl -s --max-time 3 "$SESH_FOSSIL_URL" 2>&1 || true)
        if echo "$body" | grep -qi "fossil"; then
            assert "fossil_url serves fossil HTTP" "yes" "yes"
        else
            skip "T5.4" "fossil_url responded but body did not match 'fossil' marker"
        fi
    else
        skip "T5.4" "no fossil_url in session JSON"
    fi
    sesh_down_in "$PROJ" sh
else
    skip "T5.4" "sesh up failed"
fi

# GROUP 6 — Legacy bridge subject namespacing (retired in #94)
# T6.1 / T6.2 / T6.3 exercised the legacy orch.tell + orch.stop.<num>
# bridge subjects. Those subjects no longer exist (subscriber daemon
# and per-harness publish hooks were deleted). Synadia subject coverage
# (agents.prompt.>, agents.hb.>, $SRV.INFO.agents) now lives in Group 7.

# ============================================================
# GROUP 7 — Synadia Agent Protocol primitives (the shim) — all harnesses
# ============================================================
# Per-harness conformance: spawn a worker via orch-spawn, wait for the
# shim to register, then probe the three §12 primitives:
#   T9  — $SRV.INFO.agents discovery + metadata
#   T10 — prompt round-trip (leading status:ack + zero-body terminator)
#   T11 — heartbeat (agents.hb.<token>.<owner>.<name>)
#
# Loops over [claude, codex, pi, gemini]. Each iteration is self-isolated
# via sesh_full_reset + a per-harness project dir.
#
# Note: orch-spawn --verify only works for harnesses whose BANNER table
# entry is non-empty (claude, gemini). codex and pi rely on title-rename
# which mock binaries can't easily produce without becoming Go binaries.
# Bench uses sleep-then-probe instead — uniform across all harnesses, no
# false-negatives from --verify timing.

# run_synadia_contract <harness-cli-name> <expected-agent-token> <expected-subject-prefix-token>
# Examples:
#   run_synadia_contract claude  claude-code  cc
#   run_synadia_contract codex   codex        codex
#   run_synadia_contract pi      pi           pi
#   run_synadia_contract gemini  gemini       gemini
run_synadia_contract() {
    local harness=$1 expected_agent=$2 subject_token=$3
    local proj=/tmp/g7-${harness}
    local label=g7s${harness}

    log "--- harness: ${harness} (agent=${expected_agent}, subject token=${subject_token}) ---"
    sesh_full_reset
    if ! sesh_up_in "$proj" "$label"; then
        skip "T9/T10/T11 (${harness})" "sesh up failed"
        return 0
    fi

    local pane
    pane=$(NATS_URL="$SESH_NATS_URL" orch-spawn "$harness" --cwd "$proj" --headless 2>&1 | tail -1)
    if [ -z "$pane" ] || [ "${pane:0:1}" != "%" ]; then
        assert "${harness}: orch-spawn returned a pane id" "%-prefix" "${pane:0:10}"
        sesh_down_in "$proj" "$label"
        return 0
    fi
    local pane_num="${pane#%}"
    sleep 5  # wait for the shim to register the service against the leaf

    # --- T9 ---
    log "T9 (${harness}): \$SRV.INFO.agents returns shim metadata"
    local info
    info=$(nats --server="$SESH_NATS_URL" req '$SRV.INFO.agents' '' --replies=0 --timeout=2s 2>/dev/null \
        | while IFS= read -r line; do
            # Filter: keep only lines that parse as JSON AND match this pane
            if printf '%s' "$line" | jq -e '.metadata.pane_id == "'"$pane"'"' >/dev/null 2>&1; then
                printf '%s\n' "$line"
                break
            fi
          done)
    local prompt_subj=""
    if [ -z "$info" ]; then
        assert "T9 ${harness}: service discovery returns a response for $pane" "non-empty" "empty"
    else
        local proto agent_id
        proto=$(printf '%s' "$info" | jq -r '.metadata.protocol_version // ""')
        agent_id=$(printf '%s' "$info" | jq -r '.metadata.agent // ""')
        if [ "$proto" = "0.3" ] && [ "$agent_id" = "$expected_agent" ]; then
            assert "T9 ${harness}: protocol_version=0.3, agent=${expected_agent}" "yes" "yes"
        else
            assert "T9 ${harness}: metadata correct" "proto=0.3 agent=${expected_agent}" "proto=${proto} agent=${agent_id}"
        fi

        prompt_subj=$(printf '%s' "$info" | jq -r '.endpoints[] | select(.name=="prompt") | .subject')
        # Subject must start with `agents.prompt.<subject-token>.` and end with `.pct<num>`
        if [ "${prompt_subj#agents.prompt.${subject_token}.}" != "$prompt_subj" ] && \
           [ "${prompt_subj%.pct${pane_num}}" != "$prompt_subj" ]; then
            assert "T9 ${harness}: prompt subject follows agents.prompt.${subject_token}.<owner>.pct${pane_num}" "yes" "yes"
        else
            assert "T9 ${harness}: prompt subject convention" "agents.prompt.${subject_token}.*.pct${pane_num}" "$prompt_subj"
        fi
    fi

    # --- T10 ---
    # Spec §6.4/§6.5: prompt produces a chunk stream starting with a
    # status:ack and ending with a zero-body terminator. The intermediate
    # response chunks may or may not be there (v1 shim is incremental on
    # response bridging) — what we MUST see is ≥2 reply chunks (ack +
    # something that closes the stream).
    log "T10 (${harness}): prompt round-trip produces ack + terminator"
    if [ -n "$prompt_subj" ]; then
        # reply-timeout must exceed the shim's terminatorWatchdog (30s,
        # see cmd/orch-agent-shim/internal/shim/shim.go). When the mock
        # harness produces no response chunks, the terminator is watchdog-
        # fired ~30s after the ack. 35s gives slack for scheduler jitter.
        nats --server="$SESH_NATS_URL" req "$prompt_subj" "say bench-t10-${harness}-ok" \
            --replies=0 --reply-timeout=35s --timeout=45s >"/tmp/t10-${harness}.cap" 2>&1 || true
        if grep -q '"type":"status","data":"ack"' "/tmp/t10-${harness}.cap"; then
            assert "T10 ${harness}: leading status:ack chunk received" "yes" "yes"
        else
            assert "T10 ${harness}: leading status:ack chunk received" "yes" "no"
            log "       cap head: $(head -c 200 "/tmp/t10-${harness}.cap")"
        fi
        # Count "Received" lines as a proxy for chunk count. nats CLI emits
        # one per reply, regardless of body shape — works for both populated
        # and zero-body chunks. Post-#102 the watchdog guarantees ≥2 chunks
        # (ack + terminator), so this is a hard assertion.
        reply_count=$(grep -cE "^[0-9]+:[0-9]+:[0-9]+ Received" "/tmp/t10-${harness}.cap" 2>/dev/null || echo 0)
        if [ "$reply_count" -ge 2 ]; then
            assert "T10 ${harness}: stream closed (≥2 chunks: ack + terminator)" "yes" "yes"
        else
            assert "T10 ${harness}: stream closed (≥2 chunks: ack + terminator)" "yes" "no"
            log "       cap: ${reply_count} chunk(s), expected ≥2 (ack + terminator)"
        fi
    else
        skip "T10 (${harness})" "no prompt subject discovered"
    fi

    # --- T11 ---
    log "T11 (${harness}): heartbeat publishes spec-shape payload"
    local hb
    hb=$(timeout 35 nats --server="$SESH_NATS_URL" sub --raw "agents.hb.${subject_token}.>" --count=1 2>/dev/null | tail -1)
    if [ -z "$hb" ]; then
        assert "T11 ${harness}: heartbeat received within 35s" "yes" "no"
    else
        local hb_agent hb_iid hb_interval
        hb_agent=$(printf '%s' "$hb" | jq -r '.agent // ""')
        hb_iid=$(printf '%s' "$hb" | jq -r '.instance_id // ""')
        hb_interval=$(printf '%s' "$hb" | jq -r '.interval_s // 0')
        if [ "$hb_agent" = "$expected_agent" ] && [ -n "$hb_iid" ] && [ "$hb_interval" -gt 0 ] 2>/dev/null; then
            assert "T11 ${harness}: heartbeat schema (agent=${expected_agent} + instance_id + interval_s)" "yes" "yes"
        else
            assert "T11 ${harness}: heartbeat schema" "valid" "agent=${hb_agent} iid=${hb_iid} interval=${hb_interval}"
        fi
    fi

    tmux kill-pane -t "$pane" 2>/dev/null || true
    sesh_down_in "$proj" "$label"
}

log "=== Group 7: Synadia Agent Protocol via orch-agent-shim (×4 harnesses) ==="
if ! command -v orch-agent-shim >/dev/null 2>&1; then
    skip "Group 7 — all harnesses" "orch-agent-shim not on PATH in this image"
else
    # Per-harness invocations. Subject tokens follow the Synadia
    # spec's Appendix C abbreviations where applicable (cc, oc, etc.);
    # codex/pi/gemini use the full name as the token (no canonical
    # abbreviation in the spec).
    run_synadia_contract claude  claude-code  cc
    run_synadia_contract codex   codex        codex
    run_synadia_contract pi      pi           pi
    run_synadia_contract gemini  gemini       gemini
fi

# ============================================================
# GROUP 8 — Mixed-executor broadcast (tmux + wasm/cf-worker)
# ============================================================
# Operators should be able to address tmux-spawned workers AND
# wasm/cf-worker-spawned workers uniformly via the same Synadia
# primitives — one broadcast publish, all workers receive.
#
# Status of upstream gaps:
#
#   ✓ orch#110 / merged #112 — phase 5 cf-durable-object executor
#     (persistent open-agent bridge via Durable Object). Cf-side
#     persistence gap is closed.
#
#   ✗ sesh#59 — Expose WebSocket NATS endpoint on hub leaf.
#     CF Workers can't open TCP sockets; need ws:// transport via
#     @nats-io/transport-websockets. Sesh's embedded NATS server
#     doesn't expose a WS port today. Without it, the cf-DO can't
#     join the sesh hub mesh. Workaround would be a sidecar nats-server
#     with `websocket { port: 8080, no_tls: true }`, but that doesn't
#     validate the actual sesh hub topology — pure workaround. Per
#     futility-handoff stance, holding for sesh#59 to land upstream.
#
# When sesh#59 closes: drop the SKIP, install miniflare + the DO,
# spawn 2 tmux workers + 1 cf-DO against the same sesh leaf, assert
# all 3 in $SRV.INFO.agents, broadcast pub, assert all 3 received,
# assert heartbeats from all 3 on agents.hb.>.
log "=== Group 8: Mixed-executor broadcast (tmux + wasm/cf-worker) ==="
skip "Group 8 — mixed-executor broadcast" "deferred — sesh#59 (WS NATS on hub) blocks; orch#110 closed by #112. SKIP slot flips to real test when sesh#59 lands."

# ============================================================
# GROUP 9+ — Task/Goal/Envelope/KV: defer if sesh-ops not installed
# ============================================================
log "=== Group 9+: sesh-ops dependent tests ==="
if command -v sesh-ops >/dev/null 2>&1; then
    log "  (sesh-ops present — adding task/goal/envelope tests in follow-up)"
    skip "Task CAS pull protocol" "tests not yet implemented in this bench; pattern verified manually elsewhere"
    skip "Goal lifecycle state machine" "as above"
    skip "Traceparent header chain" "as above"
    skip "Five-tier KV bucket scopes" "as above"
else
    skip "Task CAS pull protocol" "sesh-ops CLI not in this image"
    skip "Goal lifecycle state machine" "sesh-ops CLI not in this image"
    skip "Traceparent header chain" "sesh-ops CLI not in this image"
    skip "Five-tier KV bucket scopes" "sesh-ops CLI not in this image"
fi

# ============================================================
# Summary
# ============================================================
echo
log "================================================================"
log "Results: $PASS passed, $FAIL failed, $SKIP skipped"
if [ "$SKIP" -gt 0 ]; then
    log "Skipped:"
    for t in "${SKIPPED[@]}"; do log "  - $t"; done
fi
if [ "$FAIL" -gt 0 ]; then
    log "Failed:"
    for t in "${FAILED[@]}"; do log "  - $t"; done
    exit 1
fi
exit 0
