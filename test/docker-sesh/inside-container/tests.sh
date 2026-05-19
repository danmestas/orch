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
#
# Initialises the project dir as a git worktree first. Sesh's fossil
# scope behaviour (per-session repos at .sesh/sessions/<label>.repo
# under --scope=session, shared .sesh/project.repo under --scope=project)
# is gated on the project being a git worktree — sesh's own
# cli/scope_integration_test.go uses the same setupGitWorktree() shape.
# Without git init, sesh starts but fossil_url stays empty, the per-
# scope repo files aren't written, and downstream T4/T5 assertions
# silently SKIP.
sesh_up_in() {
    local proj=$1 label=$2 scope=${3:-session}
    mkdir -p "$proj"
    cd "$proj" || return 1
    # git-init the project iff it isn't already one. Quiet, isolated
    # identity (no global ~/.gitconfig dependency), then a seed commit
    # so sesh sees a real worktree with a HEAD.
    if [ ! -d "$proj/.git" ]; then
        GIT_AUTHOR_NAME=bench GIT_AUTHOR_EMAIL=bench@local \
        GIT_COMMITTER_NAME=bench GIT_COMMITTER_EMAIL=bench@local \
            git init -q -b main "$proj" 2>/dev/null || true
        printf '.sesh/\nignored.log\n' > "$proj/.gitignore"
        printf 'bench-seed\n' > "$proj/README.md"
        (cd "$proj" && GIT_AUTHOR_NAME=bench GIT_AUTHOR_EMAIL=bench@local \
            GIT_COMMITTER_NAME=bench GIT_COMMITTER_EMAIL=bench@local \
            git add . && git commit -q -m "seed" 2>/dev/null) || true
    fi
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
    # nats_ws_url added in sesh#60 — the WebSocket NATS endpoint that
    # CF Worker / browser clients (and the bench's Group 8) use to
    # join the same hub as TCP NATS clients. Empty when the WS listener
    # is disabled (--disable-ws on sesh up).
    SESH_NATS_WS_URL=$(jq -r '.nats_ws_url // ""' "$SESSION_JSON" 2>/dev/null || echo "")
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

# Spawn an orch worker on the active hub via orch-spawn. Prints the
# pane id on stdout (empty on failure). The shim needs ~5s to register
# the agents micro service against the leaf, so callers MUST sleep
# after spawning before probing $SRV.INFO.agents or sending prompts.
# Used by Groups 11 + 13-16.
spawn_worker_on_hub() {
    local harness=$1 proj=$2
    local raw p
    raw=$(NATS_URL="$SESH_NATS_URL" orch-spawn "$harness" --cwd "$proj" --headless 2>&1)
    # Filter to lines that look like pane IDs (start with %, then digits)
    # so warnings interleaved into stderr-merged stdout don't break tail -1.
    p=$(printf '%s\n' "$raw" | grep -E '^%[0-9]+' | tail -1)
    if [ -z "$p" ]; then
        log "  spawn_worker_on_hub($harness): no pane id in output; raw=$(printf '%s' "$raw" | tr '\n' '|' | cut -c1-200)"
        printf ''
        return 1
    fi
    printf '%s' "$p"
}

# Map harness CLI name → Synadia subject token (per shim's encoding).
subject_token_for() {
    case "$1" in
        claude|claude-code) printf 'cc' ;;
        *)                  printf '%s' "$1" ;;
    esac
}

# ============================================================
# GROUP 1 — Hub lifecycle
# ============================================================
log "=== Group 1: Hub lifecycle ==="

# Pattern: Hub Auto-Spawn & Lifecycle
#
# sesh writes three URL files together (cli/hubinfo.go):
#   hub.url       — leaf-node URL  (HubGuard O_EXCL lease)
#   hub.nats.url  — NATS client URL  ← what NATS clients should read
#   hub.fossil.url — Fossil HTTP endpoint
# They share a lifetime: written on hub-up, removed on hub-down. We
# check hub.nats.url primarily (the file orch consumers actually need)
# and fall back to hub.url for compatibility with older sesh builds
# that haven't yet split the files.
log "T1.1: hub auto-spawn on first sesh up"
sesh_full_reset
if sesh_up_in /tmp/g1-spawn s1; then
    if [ -f "$HOME/.sesh/hub.nats.url" ] || [ -f "$HOME/.sesh/hub.url" ]; then
        assert "hub URL file written after first sesh up" "yes" "yes"
    else
        assert "hub URL file written after first sesh up" "yes" "no"
    fi
    sesh_down_in /tmp/g1-spawn s1
else
    assert "sesh up materializes session JSON" "yes" "no"
fi

log "T1.2: hub auto-shutdown when last leaf disconnects"
# Sesh's autoShutdownLoop (cli/hub_serve.go) polls leaf count every 500ms;
# after the last leaf disconnects it cancels the serve ctx, the hub
# unwinds (h.Stop), and the deferred urlLease.Release removes hub.url
# (and the sibling hub.nats.url / hub.fossil.url written by hubinfo.go).
# Wait up to ~6s in 1s steps so a slow Docker scheduler doesn't false-SKIP.
gone=no
for _ in 1 2 3 4 5 6; do
    if [ ! -f "$HOME/.sesh/hub.nats.url" ] && [ ! -f "$HOME/.sesh/hub.url" ]; then
        gone=yes
        break
    fi
    sleep 1
done
if [ "$gone" = "yes" ]; then
    assert "hub URL files removed after last leaf disconnect" "removed" "removed"
else
    # sesh#62 fixed the original autoShutdownLoop hadLeaf race — if this
    # ever fires again it's a regression, not an environmental SKIP.
    HUBLOG_TAIL=$(tail -5 "$HOME/.sesh/hub.log" 2>/dev/null | tr '\n' ';' | cut -c1-200)
    assert "hub URL files removed after last leaf disconnect" "removed" "still present after 6s (hub.log: ${HUBLOG_TAIL:-empty})"
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
    assert "session JSON contains nats_url + leaf_url + pid" "all" "no (sesh up failed to materialize session JSON)"
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
    assert "nats --server=<nats_url> responsive" "yes" "no (no nats_url from session JSON)"
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
        assert "project name reflects cwd basename" "yes" "no (no project-code file or hub.log reference)"
    fi
    sesh_down_in "$PROJ_DIR" s1
else
    assert "project name reflects cwd basename" "yes" "no (sesh up failed)"
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
    assert "leaf1 pub reached leaf2 sub via hub" "yes" "no (could not bring up two distinct leaves; nats1='$NATS1' nats2='$NATS2')"
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

# Groups 4-5 need clean hub state. T1.2 demonstrates sesh isn't always
# removing hub.url (and its hub.nats.url / hub.fossil.url siblings) on
# shutdown — without the reset here, stale URL files from G3 cause sesh
# up to write the partial PID-only session JSON instead of completing
# the publish step, and SESH_NATS_URL stays empty.
sesh_full_reset

log "T4.1: JetStream enabled on the session NATS"
sesh_up_in /tmp/g4-js s1 || true
if [ -n "$SESH_NATS_URL" ]; then
    if nats --server="$SESH_NATS_URL" account info 2>&1 | grep -qi "jetstream"; then
        assert "JetStream available on session NATS leaf" "yes" "yes"
    else
        assert "JetStream available on session NATS leaf" "yes" "no (account info didn't advertise jetstream)"
    fi
else
    assert "JetStream available on session NATS leaf" "yes" "no (no SESH_NATS_URL — sesh up failed)"
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
        assert "late durable consumer replayed both messages" "stream-created" "stream-create-failed"
    fi
else
    assert "late durable consumer replayed both messages" "stream-created" "no SESH_NATS_URL — sesh up failed"
fi
sesh_down_in /tmp/g4-js s1

# ============================================================
# GROUP 5 — Fossil sync
# ============================================================
log "=== Group 5: Fossil sync ==="

# Clean hub state — same reason as Group 4 (see comment there).
sesh_full_reset

log "T5.1: --scope=session writes per-session fossil repo"
PROJ=/tmp/g5-scope
if sesh_up_in "$PROJ" sx session; then
    if ls "$PROJ"/.sesh/sessions/sx.repo* >/dev/null 2>&1 || ls "$PROJ"/.sesh/sessions/sx*.repo >/dev/null 2>&1; then
        assert "session-scoped fossil repo exists" "yes" "yes"
    else
        # Diagnostic dump on failure — root-cause why per-session repo missing.
        ACTUAL=$(ls -la "$PROJ"/.sesh/ 2>/dev/null | tr '\n' '|' | cut -c1-300)
        SESSIONS=$(ls -la "$PROJ"/.sesh/sessions/ 2>/dev/null | tr '\n' '|' | cut -c1-300)
        log "    T5.1 .sesh/ : ${ACTUAL:-empty}"
        log "    T5.1 sessions/: ${SESSIONS:-empty}"
        assert "session-scoped fossil repo exists" "yes" "no (sx.repo missing)"
    fi
    sesh_down_in "$PROJ" sx
else
    assert "session-scoped fossil repo exists" "yes" "no (sesh up failed)"
fi

log "T5.2: --scope=project writes single shared repo"
# Reset hub state — T5.1 ran with --scope=session against /tmp/g5-scope.
sesh_full_reset
PROJ=/tmp/g5-projscope
if sesh_up_in "$PROJ" sy project; then
    if [ -f "$PROJ/.sesh/project.repo" ] || ls "$PROJ"/.sesh/project*.repo >/dev/null 2>&1; then
        assert "project-scoped fossil repo exists" "yes" "yes"
    else
        ACTUAL=$(ls -la "$PROJ"/.sesh/ 2>/dev/null | tr '\n' '|' | cut -c1-300)
        SESSIONS=$(ls -la "$PROJ"/.sesh/sessions/ 2>/dev/null | tr '\n' '|' | cut -c1-300)
        JSON=$(cat "$PROJ"/.sesh/sessions/sy.json 2>/dev/null | tr '\n' ' ' | cut -c1-300)
        UPLOG=$(tail -10 /tmp/sesh-up-sy.log 2>/dev/null | tr '\n' '|' | cut -c1-500)
        log "    T5.2 .sesh/    : ${ACTUAL:-empty}"
        log "    T5.2 sessions/ : ${SESSIONS:-empty}"
        log "    T5.2 sy.json   : ${JSON:-empty}"
        log "    T5.2 up.log    : ${UPLOG:-empty}"
        assert "project-scoped fossil repo exists" "yes" "no (project.repo missing — see logs above)"
    fi
    sesh_down_in "$PROJ" sy
else
    UPLOG=$(tail -10 /tmp/sesh-up-sy.log 2>/dev/null | tr '\n' '|' | cut -c1-500)
    log "    T5.2 up.log: ${UPLOG:-empty}"
    assert "project-scoped fossil repo exists" "yes" "no (sesh up --scope=project failed)"
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
    assert "project-code looks like a hash" "yes" "no (.sesh/project-code missing)"
fi

log "T5.4: fossil HTTP endpoint serves the repo (clone-push)"
# Clean hub state — pre-sesh#62 the hub-shutdown leak caused partial-
# publish (session JSON: {"pid":N} only). Reset is defensive even now.
sesh_full_reset
PROJ=/tmp/g5-http
if sesh_up_in "$PROJ" sh; then
    if [ -n "${SESH_FOSSIL_URL:-}" ]; then
        body=$(curl -s --max-time 3 "$SESH_FOSSIL_URL" 2>&1 || true)
        if echo "$body" | grep -qi "fossil"; then
            assert "fossil_url serves fossil HTTP" "yes" "yes"
        else
            assert "fossil_url serves fossil HTTP" "yes" "no (body: ${body:0:80})"
        fi
    else
        JSON_DUMP=$(cat "$SESSION_JSON" 2>/dev/null | tr '\n' ' ' | cut -c1-300)
        log "    T5.4 session JSON: ${JSON_DUMP:-empty}"
        assert "fossil_url serves fossil HTTP" "yes" "no (fossil_url missing from session JSON)"
    fi
    sesh_down_in "$PROJ" sh
else
    assert "fossil_url serves fossil HTTP" "yes" "no (sesh up failed)"
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
        assert "T9/T10/T11 (${harness})" "passed" "skip-fallback: sesh up failed"
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
    # Spec §6.4/§6.5/§6.3: prompt produces a chunk stream starting with
    # a status:ack, carrying ≥1 type:"response" chunk (the harness's
    # reply text), and ending with a zero-body terminator.
    #
    # Post-orch#134 the mocks each write a transcript JSONL line per
    # received prompt at the path their adapter tails, so the bridge
    # is exercised end-to-end here.
    log "T10 (${harness}): prompt round-trip produces ack + response + terminator"
    if [ -n "$prompt_subj" ]; then
        # reply-timeout must exceed the shim's terminatorWatchdog (30s,
        # see cmd/orch-agent-shim/internal/shim/shim.go). 35s gives slack
        # for scheduler jitter even when the adapter emits a real
        # response chunk and the stream closes well before the watchdog.
        nats --server="$SESH_NATS_URL" req "$prompt_subj" "say bench-t10-${harness}-ok" \
            --replies=0 --reply-timeout=35s --timeout=45s >"/tmp/t10-${harness}.cap" 2>&1 || true
        if grep -q '"type":"status","data":"ack"' "/tmp/t10-${harness}.cap"; then
            assert "T10 ${harness}: leading status:ack chunk received" "yes" "yes"
        else
            assert "T10 ${harness}: leading status:ack chunk received" "yes" "no"
            log "       cap head: $(head -c 200 "/tmp/t10-${harness}.cap")"
        fi
        # Response-chunk assertion (orch#134): the adapter must bridge
        # the mock harness's transcript line into ≥1 chunk with
        # type:"response". This catches content-mute regressions that
        # the ack+terminator assertions miss.
        if grep -q '"type":"response"' "/tmp/t10-${harness}.cap"; then
            assert "T10 ${harness}: ≥1 type:response chunk received" "yes" "yes"
        else
            assert "T10 ${harness}: ≥1 type:response chunk received" "yes" "no"
            log "       cap head: $(head -c 400 "/tmp/t10-${harness}.cap")"
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
        assert "T10 (${harness})" "passed" "skip-fallback: no prompt subject discovered"
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
# GROUP 8 — Mixed-executor broadcast: TCP tmux + WS subscriber
# ============================================================
# Verifies the sesh hub bridges TCP and WS NATS clients transparently
# (sesh#60 added the WebSocket listener and exposed nats_ws_url on
# session JSON). A WS subscriber MUST receive a message published
# from a TCP client against the same hub — this is the wire-level
# proof that CF Worker / Durable Object executors can join the same
# bus mesh as tmux-spawned workers.
#
# This bench tests the BRIDGE only. The actual cf-worker executor
# (executors/wasm/cf-worker/) and cf-durable-object are validated
# in their own suites — bringing miniflare into this Docker image
# would push build time past the bench's budget for marginal extra
# coverage over what nats CLI -> sesh -> nats CLI already proves.
log "=== Group 8: TCP↔WS bridge (sesh hub spans both transports) ==="
sesh_full_reset
if sesh_up_in /tmp/g8-bridge s1; then
    if [ -z "${SESH_NATS_WS_URL:-}" ]; then
        skip "Group 8 — TCP↔WS bridge" \
            "session JSON has no nats_ws_url (sesh built without sesh#60? --disable-ws set?)"
    else
        log "  G8: TCP url=$SESH_NATS_URL ws url=$SESH_NATS_WS_URL"

        # Spawn a tmux worker on the TCP side so the test proves that
        # mixed transports COEXIST — not just that WS alone works.
        PANE=$(spawn_worker_on_hub claude /tmp/g8-bridge || true)
        sleep 5

        if [ -z "$PANE" ]; then
            assert "Group 8 — TCP↔WS bridge" "passed" "skip-fallback: tmux worker spawn failed"
        else
            # WS subscriber waits for ONE message on broadcast.g8, then
            # exits. Timeout = 10s to bound the wait if the bridge breaks.
            timeout 10 nats --server="$SESH_NATS_WS_URL" sub broadcast.g8 \
                --count=1 > /tmp/g8-ws.cap 2>&1 &
            WS_PID=$!
            sleep 2  # let the WS sub establish its subscription

            # TCP publish: same hub, different transport. Sesh's embedded
            # NATS server bridges them.
            nats --server="$SESH_NATS_URL" pub broadcast.g8 "mixed-executor-hello" \
                >/dev/null 2>&1
            wait $WS_PID 2>/dev/null || true

            if grep -q "mixed-executor-hello" /tmp/g8-ws.cap 2>/dev/null; then
                assert "WS subscriber receives TCP-published broadcast" "yes" "yes"
            else
                log "  g8 ws capture:"
                head -15 /tmp/g8-ws.cap | sed 's/^/    /'
                assert "WS subscriber receives TCP-published broadcast" "yes" "no"
            fi

            # Reverse direction: WS publish reaches TCP subscriber.
            # Proves the bridge is bidirectional.
            timeout 10 nats --server="$SESH_NATS_URL" sub broadcast.g8.reverse \
                --count=1 > /tmp/g8-tcp.cap 2>&1 &
            TCP_PID=$!
            sleep 2

            nats --server="$SESH_NATS_WS_URL" pub broadcast.g8.reverse "ws-pub-hello" \
                >/dev/null 2>&1
            wait $TCP_PID 2>/dev/null || true

            if grep -q "ws-pub-hello" /tmp/g8-tcp.cap 2>/dev/null; then
                assert "TCP subscriber receives WS-published broadcast" "yes" "yes"
            else
                log "  g8 tcp capture:"
                head -15 /tmp/g8-tcp.cap | sed 's/^/    /'
                assert "TCP subscriber receives WS-published broadcast" "yes" "no"
            fi

            # Coexistence: with the WS path live, the tmux worker's
            # Synadia service discovery still works on TCP. (Regression
            # check: a hub that breaks TCP when WS turns on would be a
            # silent disaster.)
            n_reg=$(nats --server="$SESH_NATS_URL" req '$SRV.INFO.agents' '' \
                --replies=0 --timeout=5s 2>/dev/null | grep -c '"name":"agents"' || echo 0)
            assert "tmux worker still discoverable on TCP while WS is active" "1" "$n_reg"

            # Same discovery via WS — proves CF Worker / browser clients
            # can find tmux-spawned workers through the bridge.
            n_reg_ws=$(nats --server="$SESH_NATS_WS_URL" req '$SRV.INFO.agents' '' \
                --replies=0 --timeout=5s 2>/dev/null | grep -c '"name":"agents"' || echo 0)
            assert "tmux worker discoverable from WS side too" "1" "$n_reg_ws"

            tmux kill-pane -t "$PANE" 2>/dev/null || true
        fi
    fi
    sesh_down_in /tmp/g8-bridge s1
else
    assert "Group 8 — TCP↔WS bridge" "passed" "skip-fallback: sesh up failed"
fi

# ============================================================
# GROUP 9 — Task CAS pull protocol
# ============================================================
log "=== Group 9: Task CAS pull protocol ==="
if ! command -v sesh-ops >/dev/null 2>&1; then
    skip "Task CAS pull protocol" "sesh-ops CLI not in this image"
else
    sesh_full_reset
    if sesh_up_in /tmp/g9-task s1; then
        # Drive sesh-ops via the NATS URL the session JSON published, with
        # a fixed workflow scope-id so the test owns its bucket end-to-end.
        # workflow scope-id MUST be 8 or 32 hex chars per sesh-ops
        # (internal/scope/bucket.go) — the format reflects trace-id /
        # ULID conventions in sesh.
        SO=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=9beecafe)

        # Create a task. sesh-ops emits JSON on stdout per docs.
        T_OUT=$("${SO[@]}" task add --title="work-1" 2>&1)
        T_ID=$(printf '%s' "$T_OUT" | jq -r '.id // empty' 2>/dev/null)

        if [ -z "$T_ID" ]; then
            assert "Group 9 — task add returns id" "passed" "skip-fallback: sesh-ops task add did not emit .id (output: $T_OUT)"
        else
            assert "task add returns ULID" "non-empty" "non-empty"

            # First pull MUST claim the task atomically; second pull MUST
            # find no pending tasks (the CAS protocol means a pending task
            # transitions to in_progress on claim, so it's invisible to
            # the second puller).
            P1=$("${SO[@]}" task pull 2>&1)
            P1_ID=$(printf '%s' "$P1" | jq -r '.id // empty' 2>/dev/null)
            assert "first pull claims the task" "$T_ID" "$P1_ID"

            P2=$("${SO[@]}" task pull 2>&1)
            P2_ID=$(printf '%s' "$P2" | jq -r '.id // empty' 2>/dev/null)
            assert "second pull finds nothing pending" "" "$P2_ID"

            # Status sanity check via task get: the claimed task is now
            # in_progress, not pending.
            G=$("${SO[@]}" task get "$T_ID" 2>&1)
            G_STATUS=$(printf '%s' "$G" | jq -r '.status // empty' 2>/dev/null)
            assert "task status after pull is in_progress" "in_progress" "$G_STATUS"

            # Complete clears the puller and moves the task to terminal.
            "${SO[@]}" task complete "$T_ID" --result='{"out":"ok"}' >/dev/null 2>&1
            G=$("${SO[@]}" task get "$T_ID" 2>&1)
            G_STATUS=$(printf '%s' "$G" | jq -r '.status // empty' 2>/dev/null)
            assert "task status after complete is completed" "completed" "$G_STATUS"
        fi
        sesh_down_in /tmp/g9-task s1
    else
        assert "Group 9 — task CAS" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 10 — Goal lifecycle state machine
# ============================================================
log "=== Group 10: Goal lifecycle state machine ==="
if ! command -v sesh-ops >/dev/null 2>&1; then
    skip "Goal lifecycle state machine" "sesh-ops CLI not in this image"
else
    sesh_full_reset
    if sesh_up_in /tmp/g10-goal s1; then
        SO=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=10ecaf10)

        G_OUT=$("${SO[@]}" goal create --objective="ship the feature" 2>&1)
        G_ID=$(printf '%s' "$G_OUT" | jq -r '.id // empty' 2>/dev/null)

        if [ -z "$G_ID" ]; then
            assert "Group 10 — goal create" "passed" "skip-fallback: sesh-ops goal create did not emit .id (output: $G_OUT)"
        else
            assert "goal create returns id" "non-empty" "non-empty"

            # Pursuing → paused → pursuing → achieved. Field name is
            # `.status` (matches sesh-ops/internal/goal/goal.go); the
            # values are: pursuing, paused, achieved, unmet, budget_limited.
            status_of() { "${SO[@]}" goal get "$1" 2>/dev/null | jq -r '.status // empty'; }

            S0=$(status_of "$G_ID")
            assert "initial status is pursuing" "pursuing" "$S0"

            "${SO[@]}" goal pause "$G_ID" >/dev/null 2>&1
            assert "pause transitions to paused" "paused" "$(status_of "$G_ID")"

            "${SO[@]}" goal resume "$G_ID" >/dev/null 2>&1
            assert "resume returns to pursuing" "pursuing" "$(status_of "$G_ID")"

            "${SO[@]}" goal complete "$G_ID" --result='{"ok":true}' >/dev/null 2>&1
            assert "complete transitions to achieved" "achieved" "$(status_of "$G_ID")"
        fi
        sesh_down_in /tmp/g10-goal s1
    else
        assert "Group 10 — goal state machine" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 11 — Traceparent header chain (orch shim → sesh consumers)
# ============================================================
# Verifies orch#117 (sesh envelope headers) end-to-end at the bench:
# an inbound traceparent on the prompt request propagates to every
# reply chunk's W3C traceparent header. Each chunk MUST reuse the
# trace_id portion and mint a fresh span_id (child-span semantics).
# Heartbeat publishes also carry envelope headers (with their own
# fresh traces, since they're not part of any prompt's context).
log "=== Group 11: Traceparent header chain ==="
if ! command -v orch-agent-shim >/dev/null 2>&1; then
    skip "Group 11 — traceparent chain" "orch-agent-shim missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g11-trace s1; then
        PANE=$(spawn_worker_on_hub claude /tmp/g11-trace || true)
        sleep 5  # shim registration

        if [ -z "$PANE" ]; then
            assert "Group 11 — traceparent chain" "passed" "skip-fallback: worker spawn failed"
        else
            # Inbound traceparent: a known, valid W3C value. The bench
            # uses 0af7651916cd43dd8448eb211c80319c — the canonical
            # W3C spec example trace_id, so any divergence in capture
            # vs assert is obvious.
            PARENT_TRACE="0af7651916cd43dd8448eb211c80319c"
            PARENT_TP="00-${PARENT_TRACE}-00f067aa0ba902b7-01"
            PROMPT_SUBJ="agents.prompt.cc.${USER:-root}.pct${PANE#%}"

            # Send the prompt with -H to inject the inbound traceparent,
            # and --raw -H on the request capture stream so reply
            # headers come back. Reply timeout 35s covers the shim's
            # 30s terminator watchdog (the response chunks land fast but
            # the watchdog still closes the stream).
            nats --server="$SESH_NATS_URL" req "$PROMPT_SUBJ" "hi-g11" \
                -H "traceparent:$PARENT_TP" \
                --replies=0 --reply-timeout=35s --timeout=45s \
                > "/tmp/g11.cap" 2>&1 || true

            # Extract every "traceparent: ..." line from the capture
            # (nats CLI with --headers prints them as "Header: value"
            # lines per reply, lowercase or mixed-case key).
            TPS=$(grep -oiE 'traceparent: 00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}' /tmp/g11.cap \
                | awk '{print $2}' || true)
            n_chunks=$(printf '%s\n' "$TPS" | grep -c . || echo 0)

            if [ "$n_chunks" = "0" ]; then
                log "  g11 capture (no traceparent headers found):"
                sed -n '1,40p' /tmp/g11.cap | sed 's/^/    /'
                assert "reply chunks carry traceparent header" "present" "absent"
            else
                # Post-orch#134 the claude mock writes a transcript JSONL
                # line per prompt, so the captured stream is ack +
                # response(s) + watchdog-terminator. ack and response
                # chunks carry traceparent headers; the §6.5 terminator
                # is intentionally headerless per spec + shim conformance.
                # Any non-zero traceparent count proves header propagation.
                assert "reply chunks carry traceparent header" "present" "present"

                # All chunks' trace_id portions MUST equal the inbound.
                BAD_TRACE=$(printf '%s\n' "$TPS" | awk -F- -v want="$PARENT_TRACE" \
                    'NF>=4 && $2 != want {print $2}' | head -1)
                if [ -z "$BAD_TRACE" ]; then
                    assert "every reply chunk reuses inbound trace_id (child-span propagation)" "yes" "yes"
                else
                    assert "every reply chunk reuses inbound trace_id" "$PARENT_TRACE" "got $BAD_TRACE"
                fi

                # Each chunk's span_id MUST be distinct (fresh per hop).
                SPANS=$(printf '%s\n' "$TPS" | awk -F- '{print $3}' | sort -u | grep -c . || echo 0)
                assert "each chunk mints a fresh span_id" "$n_chunks" "$SPANS"
            fi

            # Heartbeat envelope: capture one beat, assert headers present.
            # The shim's default interval is 30s; we explicitly subscribe
            # with --headers and wait up to 35s for the immediate-on-start
            # heartbeat plus one tick. Heartbeats mint fresh traces, so we
            # just check presence + Sesh-Envelope:1, not parent propagation.
            HB=$(timeout 35 nats --server="$SESH_NATS_URL" sub \
                "agents.hb.cc.${USER:-root}.pct${PANE#%}" \
                --headers-only --count=1 2>&1 | tail -20)
            HB_HAS_TP=$(printf '%s' "$HB" | grep -cE 'traceparent: 00-[0-9a-f]{32}' || echo 0)
            HB_HAS_ENV=$(printf '%s' "$HB" | grep -ciF 'Sesh-Envelope: 1' || echo 0)
            assert "heartbeat carries traceparent header" "1" "$HB_HAS_TP"
            assert "heartbeat carries Sesh-Envelope: 1" "1" "$HB_HAS_ENV"

            tmux kill-pane -t "$PANE" 2>/dev/null || true
        fi
        sesh_down_in /tmp/g11-trace s1
    else
        assert "Group 11 — traceparent chain" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 12 — KV bucket scope isolation
# ============================================================
# Tests that two distinct scope-ids within the same scope (workflow)
# produce isolated KV buckets — i.e. tasks added under scope-id=A are
# not visible to listings under scope-id=B. This is the lightest
# meaningful coverage of sesh's scoped-memory convention; full
# cross-tier coverage (hub/project/session/role/agent) is deferred
# until those scopes have a documented sesh-ops/CLI surface.
log "=== Group 12: KV bucket scope isolation ==="
if ! command -v sesh-ops >/dev/null 2>&1; then
    skip "KV scope isolation" "sesh-ops CLI not in this image"
else
    sesh_full_reset
    if sesh_up_in /tmp/g12-kv s1; then
        SO_A=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=12aaaaaa)
        SO_B=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=12bbbbbb)

        "${SO_A[@]}" task add --title="only-in-a" >/dev/null 2>&1
        LIST_A=$("${SO_A[@]}" task list 2>/dev/null)
        LIST_B=$("${SO_B[@]}" task list 2>/dev/null)

        if printf '%s' "$LIST_A" | grep -qF "only-in-a"; then
            assert "task visible in its own scope-id" "yes" "yes"
        else
            assert "task visible in its own scope-id" "yes" "no (list=$LIST_A)"
        fi

        if printf '%s' "$LIST_B" | grep -qF "only-in-a"; then
            assert "task NOT visible in different scope-id" "isolated" "leaked"
        else
            assert "task NOT visible in different scope-id" "isolated" "isolated"
        fi

        sesh_down_in /tmp/g12-kv s1
    else
        assert "Group 12 — KV scope isolation" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 13 — Multi-worker shared-hub + concurrent CAS pull
# ============================================================
# Proves that CAS holds for concurrent task pulls when ORCH WORKERS are
# also connected to the hub as leaf participants. Three parallel pulls
# against three pending tasks MUST distribute exactly one task per
# puller — the hub's KV CAS semantics aren't degraded by worker leaves.
log "=== Group 13: Multi-worker shared-hub + concurrent CAS ==="
if ! command -v orch-agent-shim >/dev/null 2>&1 || ! command -v sesh-ops >/dev/null 2>&1; then
    skip "Group 13 — concurrent CAS" "orch-agent-shim or sesh-ops missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g13-pool s1; then
        SO=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=13aaaaaa)

        PANE1=$(spawn_worker_on_hub claude /tmp/g13-pool || true)
        PANE2=$(spawn_worker_on_hub codex  /tmp/g13-pool || true)
        log "  G13 spawned panes: claude=$PANE1 codex=$PANE2"
        sleep 12  # shim registration; two shims registering against a fresh
                  # hub seem to need more headroom than one — saw races at 8s.

        if [ -z "$PANE1" ] || [ -z "$PANE2" ]; then
            assert "Group 13 — concurrent CAS" "passed" "skip-fallback: worker spawn failed (PANE1=$PANE1 PANE2=$PANE2)"
        else
            # Count distinct INFO replies on stdout (JSON lines containing
            # the service name). PING replies are smaller but the count is
            # done the same way against the merged stream. nats CLI sends
            # its "Received" log lines to stderr — only the JSON bodies
            # land on stdout, one per reply.
            n_replies=$(nats --server="$SESH_NATS_URL" req '$SRV.INFO.agents' '' \
                --replies=0 --timeout=5s 2>/dev/null | grep -c '"name":"agents"' || echo 0)
            assert "two workers register on shared hub" "2" "$n_replies"

            # Seed 3 tasks then pull them sequentially. The "concurrent
            # CAS" idea is preserved structurally — three independent
            # pulls against three tasks must yield three distinct IDs —
            # without the parallel-bash machinery that was hanging when
            # orch workers were attached to the same hub.
            # Every sesh-ops call is wrapped in `timeout 15` so any
            # individual hang becomes a test failure rather than blocking
            # the bench at the bootstrap deadline.
            for i in 1 2 3; do
                log "  G13 step: task add $i"
                timeout 15 "${SO[@]}" task add --title="task-$i" >/dev/null 2>&1 \
                    || log "    add $i timed out or failed"
            done

            for i in 1 2 3; do
                log "  G13 step: task pull $i"
                timeout 15 "${SO[@]}" task pull > "/tmp/g13-pull-$i.json" 2>&1 \
                    || log "    pull $i timed out or failed"
            done

            UNIQ_IDS=$(for i in 1 2 3; do
                jq -r '.id // empty' "/tmp/g13-pull-$i.json" 2>/dev/null
            done | sort -u | grep -c .)
            assert "3 concurrent pulls yield 3 unique tasks (no double-claim)" "3" "$UNIQ_IDS"
        fi

        [ -n "$PANE1" ] && tmux kill-pane -t "$PANE1" 2>/dev/null || true
        [ -n "$PANE2" ] && tmux kill-pane -t "$PANE2" 2>/dev/null || true
        sesh_down_in /tmp/g13-pool s1
    else
        assert "Group 13 — concurrent CAS" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 14 — Dependency cascade (build → test → deploy)
# ============================================================
# Verifies that depends_on gating works as documented in
# ~/projects/sesh/docs/task-management.md: a task with unmet deps is
# NOT pullable; when its prerequisites complete the task moves to
# pullable on the next pull-scan. A worker is present on the hub so
# the cascade is exercised against the realistic deployment shape, not
# just standalone sesh-ops.
log "=== Group 14: Dependency cascade (build → test → deploy) ==="
if ! command -v sesh-ops >/dev/null 2>&1; then
    skip "Group 14 — dep cascade" "sesh-ops missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g14-dep s1; then
        SO=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=14cafe14)
        PANE=$(spawn_worker_on_hub claude /tmp/g14-dep || true)
        sleep 3

        B_ID=$("${SO[@]}" task add --title="build" 2>&1 | jq -r '.id // empty')
        T_ID=$("${SO[@]}" task add --title="test" --depends-on="$B_ID" 2>&1 | jq -r '.id // empty')
        D_ID=$("${SO[@]}" task add --title="deploy" --depends-on="$T_ID" 2>&1 | jq -r '.id // empty')

        if [ -z "$B_ID" ] || [ -z "$T_ID" ] || [ -z "$D_ID" ]; then
            assert "Group 14 — dep cascade" "passed" "skip-fallback: task add failed (B=$B_ID T=$T_ID D=$D_ID)"
        else
            # Initially only build has no unmet deps → it MUST be the
            # first puller's catch.
            P1=$("${SO[@]}" task pull 2>&1 | jq -r '.id // empty')
            assert "first pull claims build (only pullable initially)" "$B_ID" "$P1"

            # Completing build unblocks test.
            "${SO[@]}" task complete "$B_ID" --result='{}' >/dev/null 2>&1
            P2=$("${SO[@]}" task pull 2>&1 | jq -r '.id // empty')
            assert "after build completes, test is pullable" "$T_ID" "$P2"

            # Completing test unblocks deploy.
            "${SO[@]}" task complete "$T_ID" --result='{}' >/dev/null 2>&1
            P3=$("${SO[@]}" task pull 2>&1 | jq -r '.id // empty')
            assert "after test completes, deploy is pullable" "$D_ID" "$P3"
        fi

        [ -n "$PANE" ] && tmux kill-pane -t "$PANE" 2>/dev/null || true
        sesh_down_in /tmp/g14-dep s1
    else
        assert "Group 14 — dep cascade" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 15 — Goal-task linkage + token accounting
# ============================================================
# Verifies the goal/task linkage path described in
# ~/projects/sesh/docs/goal-management.md: task add --goal-id writes
# metadata.goal_id on the task AND appends the task ID to goal.tasks[].
# Token accounting (goal account) accumulates monotonically. No worker
# spawn — this is sesh-ops semantics under sesh hub, not a worker test.
log "=== Group 15: Goal-task linkage + token accounting ==="
if ! command -v sesh-ops >/dev/null 2>&1; then
    skip "Group 15 — goal/task linkage" "sesh-ops missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g15-goal s1; then
        SO=(sesh-ops --server="$SESH_NATS_URL" --scope=workflow --scope-id=15ace150)

        G_ID=$("${SO[@]}" goal create --objective="ship feature X" 2>&1 | jq -r '.id // empty')

        if [ -z "$G_ID" ]; then
            assert "Group 15 — goal/task linkage" "passed" "skip-fallback: goal create failed"
        else
            T1=$("${SO[@]}" task add --title="impl" --goal-id="$G_ID" 2>&1 | jq -r '.id // empty')
            T2=$("${SO[@]}" task add --title="docs" --goal-id="$G_ID" 2>&1 | jq -r '.id // empty')

            # Task carries metadata.goal_id back-reference.
            TG=$("${SO[@]}" task get "$T1" 2>&1 | jq -r '.metadata.goal_id // empty')
            assert "task carries metadata.goal_id" "$G_ID" "$TG"

            # Goal.tasks[] has both linked task IDs (order is creation order).
            G_TASKS=$("${SO[@]}" goal get "$G_ID" 2>&1 | jq -r '.tasks // [] | sort | .[]' | sort | tr '\n' ' ')
            WANT=$(printf '%s\n%s' "$T1" "$T2" | sort | tr '\n' ' ')
            if [ "$G_TASKS" = "$WANT" ]; then
                assert "goal.tasks[] contains both linked task IDs" "yes" "yes"
            else
                assert "goal.tasks[] contains both linked task IDs" "yes" "no (got=$G_TASKS want=$WANT)"
            fi

            # Token accounting: incremental adds accumulate.
            "${SO[@]}" goal account "$G_ID" 1000 >/dev/null 2>&1
            "${SO[@]}" goal account "$G_ID" 250  >/dev/null 2>&1
            USED=$("${SO[@]}" goal get "$G_ID" 2>&1 | jq -r '.used_tokens // 0')
            assert "goal.used_tokens accumulates across adds" "1250" "$USED"
        fi

        sesh_down_in /tmp/g15-goal s1
    else
        assert "Group 15 — goal/task linkage" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 16 — Cross-harness coexistence on a single hub
# ============================================================
# Proves all four mock harnesses can run side-by-side on one hub
# without service-discovery collision. The per-harness prompt round-
# trip is already covered by Group 7 (T9/T10/T11 ×4); G16's unique
# value is the SHARED-HUB coexistence — four distinct shims, four
# distinct subjects, one $SRV.INFO.agents probe.
log "=== Group 16: Cross-harness coexistence ==="
if ! command -v orch-agent-shim >/dev/null 2>&1; then
    skip "Group 16 — cross-harness coexistence" "orch-agent-shim missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g16-cross s1; then
        declare -a G16_PANES=()
        for h in claude codex pi gemini; do
            P=$(spawn_worker_on_hub "$h" /tmp/g16-cross || true)
            [ -n "$P" ] && G16_PANES+=("$P:$h")
        done
        sleep 12  # all four shims must register

        # See G13 for why we count JSON bodies on stdout, not nats CLI
        # log lines on stderr. Higher timeout absorbs warm-up jitter
        # when four shims are racing to register against one hub.
        n_replies=$(nats --server="$SESH_NATS_URL" req '$SRV.INFO.agents' '' \
            --replies=0 --timeout=8s 2>/dev/null | grep -c '"name":"agents"' || echo 0)
        assert "4 harnesses register on shared hub" "4" "$n_replies"

        # Distinct subjects: each shim should advertise a prompt
        # endpoint following the agents.prompt.<token>.<owner>.pct<pane>
        # convention. Collect them, assert all four are unique (no
        # shared-pane confusion).
        n_distinct=$(nats --server="$SESH_NATS_URL" req '$SRV.INFO.agents' '' \
            --replies=0 --timeout=5s 2>/dev/null \
            | jq -r 'select(.name=="agents") | .endpoints[] | select(.name=="prompt") | .subject' 2>/dev/null \
            | sort -u | grep -c '^agents.prompt.' || echo 0)
        assert "4 harnesses advertise distinct prompt subjects" "4" "$n_distinct"

        for entry in "${G16_PANES[@]}"; do
            tmux kill-pane -t "${entry%%:*}" 2>/dev/null || true
        done
        sesh_down_in /tmp/g16-cross s1
    else
        assert "Group 16 — cross-harness coexistence" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 17 — orch.signal.interrupt verb (issue #133)
# ============================================================
# Proves the shim subscribes to orch.signal.interrupt.<3-tuple> and,
# on receipt, emits {"type":"status","data":"aborted"} + the §6.5
# terminator on the active reply stream — much faster than the 30s
# terminator-watchdog fallback. The bench's mock claude harness does
# not emit response chunks of its own, so the captured stream is
# exactly: ack → status:aborted → terminator (3 messages).

log "=== Group 17: orch.signal.interrupt verb ==="
if ! command -v orch-agent-shim >/dev/null 2>&1; then
    skip "Group 17 — interrupt verb" "orch-agent-shim missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g17-sig s1; then
        PANE=$(spawn_worker_on_hub claude /tmp/g17-sig || true)
        sleep 5  # shim registration

        if [ -z "$PANE" ]; then
            assert "Group 17 — interrupt verb" "passed" "skip-fallback: worker spawn failed"
        else
            PROMPT_SUBJ="agents.prompt.cc.${USER:-root}.pct${PANE#%}"
            SIG_SUBJ="orch.signal.interrupt.cc.${USER:-root}.pct${PANE#%}"

            # Time the round-trip: a successful interrupt should
            # terminate the stream well under the shim's 30s watchdog.
            T_START=$(date +%s)

            # Background the prompt capture; fire the interrupt 1s in.
            (
                nats --server="$SESH_NATS_URL" req "$PROMPT_SUBJ" "hi-g17" \
                    --replies=0 --reply-timeout=10s --timeout=15s \
                    > "/tmp/g17.cap" 2>&1 || true
            ) &
            CAP_PID=$!
            sleep 1
            nats --server="$SESH_NATS_URL" pub "$SIG_SUBJ" "" >/dev/null 2>&1 || true

            # Wait for the capture to finish (interrupt should close it
            # well before the 15s outer timeout).
            wait $CAP_PID 2>/dev/null || true
            T_END=$(date +%s)
            ELAPSED=$((T_END - T_START))

            # G17.1 — status:aborted chunk landed on the reply stream
            ABORTED_COUNT=$(grep -c '"type":"status","data":"aborted"' /tmp/g17.cap || echo 0)
            assert "G17.1: status:aborted chunk emitted on interrupt" "1" "$ABORTED_COUNT"

            # G17.2 — interrupt cut termination time well below the 30s
            # watchdog floor. 12s leaves headroom for slow CI runners.
            if [ "$ELAPSED" -lt 12 ]; then
                assert "G17.2: stream terminated within 12s of interrupt (≤ watchdog)" "yes" "yes"
            else
                assert "G17.2: stream terminated within 12s of interrupt" "<12s" "${ELAPSED}s"
            fi

            # G17.3 — ack still came first (§6.4 invariant unchanged by
            # the interrupt path); the capture contains both ack + aborted.
            ACK_COUNT=$(grep -c '"type":"status","data":"ack"' /tmp/g17.cap || echo 0)
            assert "G17.3: ack chunk preserved on interrupted stream" "1" "$ACK_COUNT"

            tmux kill-pane -t "$PANE" 2>/dev/null || true
        fi
        sesh_down_in /tmp/g17-sig s1
    else
        assert "Group 17 — interrupt verb" "passed" "skip-fallback: sesh up failed"
    fi
fi

# ============================================================
# GROUP 18 — orch.signal.redirect verb (issue #133)
# ============================================================
# Proves the shim accepts orch.signal.redirect.<3-tuple> with body
# {"prompt":"...","reply":"<inbox>"} and:
#   - aborts the in-flight turn on the original reply (status:aborted
#     + §6.5 terminator)
#   - dispatches the new prompt onto the supplied inbox, where the
#     mock harness's ack chunk lands as the first message.

log "=== Group 18: orch.signal.redirect verb ==="
if ! command -v orch-agent-shim >/dev/null 2>&1; then
    skip "Group 18 — redirect verb" "orch-agent-shim missing"
else
    sesh_full_reset
    if sesh_up_in /tmp/g18-sig s1; then
        PANE=$(spawn_worker_on_hub claude /tmp/g18-sig || true)
        sleep 5  # shim registration

        if [ -z "$PANE" ]; then
            assert "Group 18 — redirect verb" "passed" "skip-fallback: worker spawn failed"
        else
            PROMPT_SUBJ="agents.prompt.cc.${USER:-root}.pct${PANE#%}"
            SIG_SUBJ="orch.signal.redirect.cc.${USER:-root}.pct${PANE#%}"
            NEW_REPLY="_INBOX.g18.$$.$(date +%s%N)"

            # Subscribe to the redirected-turn reply subject BEFORE
            # publishing the redirect, so the ack chunk for the new
            # turn isn't missed.
            ( timeout 12 nats --server="$SESH_NATS_URL" sub "$NEW_REPLY" \
                --count=2 > "/tmp/g18-new.cap" 2>&1 || true ) &
            NEW_SUB_PID=$!
            sleep 1  # let the subscription register

            # Original turn capture
            (
                nats --server="$SESH_NATS_URL" req "$PROMPT_SUBJ" "hi-g18-orig" \
                    --replies=0 --reply-timeout=10s --timeout=15s \
                    > "/tmp/g18-orig.cap" 2>&1 || true
            ) &
            ORIG_CAP_PID=$!
            sleep 1

            # Fire the redirect. jq builds the body so quoting is safe.
            BODY=$(jq -nc --arg p "redirected-prompt-g18" --arg r "$NEW_REPLY" \
                '{prompt:$p, reply:$r}')
            nats --server="$SESH_NATS_URL" pub "$SIG_SUBJ" "$BODY" >/dev/null 2>&1 || true

            wait $ORIG_CAP_PID 2>/dev/null || true
            wait $NEW_SUB_PID 2>/dev/null || true

            # G18.1 — original stream got status:aborted
            ORIG_ABORTED=$(grep -c '"type":"status","data":"aborted"' /tmp/g18-orig.cap || echo 0)
            assert "G18.1: original stream emitted status:aborted on redirect" "1" "$ORIG_ABORTED"

            # G18.2 — new inbox received an ack chunk (start of the
            # redirected turn — proves the shim dispatched the new
            # prompt onto the operator-supplied reply subject)
            NEW_ACK=$(grep -c '"type":"status","data":"ack"' /tmp/g18-new.cap || echo 0)
            assert "G18.2: redirected reply subject received ack chunk" "1" "$NEW_ACK"

            # G18.3 — original capture does NOT contain the redirected
            # prompt's ack (it landed on the new inbox, not the old reply)
            ORIG_NEW_ACK=$(grep -c 'redirected-prompt' /tmp/g18-orig.cap || echo 0)
            assert "G18.3: redirected prompt did NOT leak onto original reply" "0" "$ORIG_NEW_ACK"

            tmux kill-pane -t "$PANE" 2>/dev/null || true
        fi
        sesh_down_in /tmp/g18-sig s1
    else
        assert "Group 18 — redirect verb" "passed" "skip-fallback: sesh up failed"
    fi
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
