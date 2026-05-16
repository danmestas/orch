#!/usr/bin/env bash
# soak-runner.sh — runs INSIDE the docker-sesh image as the soak entrypoint.
#
# Boots sesh hub, spawns N workers (one per harness in SOAK_HARNESSES),
# drives prompts in a loop, samples per-worker metrics on a fixed cadence,
# emits a markdown report.
#
# Mounted via bind mount from test/bench/soak-runner.sh on the host. Output
# is written to /tmp/soak-report.md which is also bind-mounted so the host
# captures it.
set -uo pipefail

log() { printf '[soak-runner %s] %s\n' "$(date -u +%T)" "$*"; }

HARNESSES_CSV="${SOAK_HARNESSES:-claude,codex,pi,gemini}"
PROMPTS_PER_WORKER="${SOAK_PROMPTS_PER_WORKER:-100}"
DURATION="${SOAK_DURATION:-60m}"
BROADCAST_RATIO="${SOAK_BROADCAST_RATIO:-0.5}"
SAMPLE_INTERVAL="${SOAK_SAMPLE_INTERVAL:-60}"
REPORT="/tmp/soak-report.md"

# Convert duration to seconds (supports 30s/2m/1h/etc).
to_seconds() {
    local d=$1
    case $d in
        *s) echo "${d%s}" ;;
        *m) echo $(( ${d%m} * 60 )) ;;
        *h) echo $(( ${d%h} * 3600 )) ;;
        *)  echo "$d" ;;
    esac
}
DURATION_SEC=$(to_seconds "$DURATION")

# Convert csv → array.
IFS=',' read -ra HARNESSES <<< "$HARNESSES_CSV"
NUM_WORKERS=${#HARNESSES[@]}

# Per-harness Synadia metadata (token used in subjects, expected metadata.agent).
harness_subj_token() {
    case $1 in
        claude) echo "cc" ;;
        codex)  echo "codex" ;;
        pi)     echo "pi" ;;
        gemini) echo "gemini" ;;
        *)      echo "$1" ;;
    esac
}
harness_agent_name() {
    case $1 in
        claude) echo "claude-code" ;;
        *)      echo "$1" ;;
    esac
}

# --- bootstrap (same as test/docker-sesh/inside-container/bootstrap.sh
#                in spirit but tailored for soak run) -------------------
mkdir -p "$HOME/.claude/hooks" "$HOME/.cache"

# Clone wardrobe (for suit, if any harness needs outfit-based spawn).
SUIT_CONTENT="$HOME/.local/share/suit/content"
mkdir -p "$(dirname "$SUIT_CONTENT")"
rm -rf "$SUIT_CONTENT"
git clone --depth 1 https://github.com/danmestas/wardrobe.git "$SUIT_CONTENT" \
    >/tmp/wardrobe-clone.log 2>&1 || log "wardrobe clone failed"

# Boot sesh hub. We need a single shared session for all workers — soak
# treats N workers as a fleet against one hub.
SOAK_PROJ=/tmp/soak-proj
mkdir -p "$SOAK_PROJ"
cd "$SOAK_PROJ" || { log "FATAL: cannot cd $SOAK_PROJ"; exit 1; }
sesh up --session=soak --scope=session >/tmp/sesh-up.log 2>&1 &
SESH_BG=$!
export SESH_BG  # surfaced for diagnostic but not referenced — keep variable available
for _ in $(seq 1 30); do
    [ -f "$SOAK_PROJ/.sesh/sessions/soak.json" ] && break
    sleep 0.5
done
NATS_URL=$(jq -r '.nats_url // ""' "$SOAK_PROJ/.sesh/sessions/soak.json" 2>/dev/null)
if [ -z "$NATS_URL" ]; then
    log "FATAL: sesh up did not produce nats_url"; tail /tmp/sesh-up.log
    exit 1
fi
log "sesh ready: $NATS_URL"
export NATS_URL

# Need tmux running for orch-spawn.
tmux new-session -d -s soak-tmux 2>/dev/null || true

# --- spawn workers -------------------------------------------------------
declare -A WORKER_PANE   # harness → pane id
declare -A WORKER_AGENT  # harness → expected metadata.agent
declare -A WORKER_TOKEN  # harness → subject token
declare -A WORKER_SUBJ   # harness → prompt subject

log "spawning $NUM_WORKERS workers: ${HARNESSES[*]}"
for h in "${HARNESSES[@]}"; do
    pane=$(NATS_URL="$NATS_URL" orch-spawn "$h" --cwd "$SOAK_PROJ" --headless 2>&1 | tail -1)
    if [ -z "$pane" ] || [ "${pane:0:1}" != "%" ]; then
        log "FATAL: spawn $h failed (got: $pane)"; exit 1
    fi
    WORKER_PANE[$h]=$pane
    WORKER_AGENT[$h]=$(harness_agent_name "$h")
    WORKER_TOKEN[$h]=$(harness_subj_token "$h")
    log "  $h → pane $pane (agent=${WORKER_AGENT[$h]}, token=${WORKER_TOKEN[$h]})"
done
sleep 5  # let shims register

# Resolve each worker's prompt subject from $SRV.INFO.agents
log "discovering prompt subjects..."
ALL_INFO=$(nats --server="$NATS_URL" req '$SRV.INFO.agents' '' --replies=0 --timeout=3s 2>/dev/null \
    | while IFS= read -r line; do
        printf '%s' "$line" | jq -e . >/dev/null 2>&1 && printf '%s\n' "$line"
      done)
for h in "${HARNESSES[@]}"; do
    pane="${WORKER_PANE[$h]}"
    subj=$(printf '%s\n' "$ALL_INFO" | while read -r entry; do
        printf '%s' "$entry" | jq -e --arg p "$pane" '. | select(.metadata.pane_id == $p) | .endpoints[] | select(.name=="prompt") | .subject' -r 2>/dev/null
    done | head -1)
    WORKER_SUBJ[$h]=$subj
    log "  $h → $subj"
done

# --- soak loop -----------------------------------------------------------
log "starting soak: duration=${DURATION_SEC}s prompts/worker=${PROMPTS_PER_WORKER}"
START_TS=$(date +%s)
DEADLINE_TS=$((START_TS + DURATION_SEC))

declare -A SUCCESS_COUNT ERROR_COUNT HB_COUNT
declare -A RSS_START RSS_LAST FD_START FD_LAST
for h in "${HARNESSES[@]}"; do
    SUCCESS_COUNT[$h]=0
    ERROR_COUNT[$h]=0
    HB_COUNT[$h]=0
    pid=$(pgrep -af "orch-agent-shim.*--pane ${WORKER_PANE[$h]}" | awk '{print $1}' | head -1)
    if [ -n "$pid" ]; then
        RSS_START[$h]=$(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ' || echo 0)
        FD_START[$h]=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l || echo 0)
    fi
    RSS_LAST[$h]="${RSS_START[$h]:-0}"
    FD_LAST[$h]="${FD_START[$h]:-0}"
done

# Background heartbeat collector — increments HB_COUNT per harness.
HB_LOG=/tmp/hb-collect.log
( nats --server="$NATS_URL" sub --raw "agents.hb.>" 2>/dev/null \
    | while IFS= read -r hb; do
        a=$(printf '%s' "$hb" | jq -r '.agent // ""' 2>/dev/null)
        [ -n "$a" ] && echo "$a" >> "$HB_LOG"
      done ) &
HB_BG=$!

PROMPT_IDX=0
LAST_SAMPLE_TS=$START_TS
SAMPLES_FILE=/tmp/soak-samples.tsv
echo -e "ts_unix\tharness\trss_kb\tfd_count\tsuccess\terror\thb_count" > "$SAMPLES_FILE"

while [ "$(date +%s)" -lt "$DEADLINE_TS" ]; do
    # Stop when every harness has hit its prompt budget.
    all_done=1
    for h in "${HARNESSES[@]}"; do
        total=$(( SUCCESS_COUNT[$h] + ERROR_COUNT[$h] ))
        [ "$total" -lt "$PROMPTS_PER_WORKER" ] && { all_done=0; break; }
    done
    [ "$all_done" -eq 1 ] && { log "all workers reached prompt budget — stopping early"; break; }

    # Pick a harness (round-robin by PROMPT_IDX) and decide broadcast vs targeted
    h="${HARNESSES[$(( PROMPT_IDX % NUM_WORKERS ))]}"
    total=$(( SUCCESS_COUNT[$h] + ERROR_COUNT[$h] ))
    if [ "$total" -ge "$PROMPTS_PER_WORKER" ]; then
        PROMPT_IDX=$(( PROMPT_IDX + 1 ))
        continue
    fi

    # Coin flip vs broadcast ratio (0-100 random vs ratio*100)
    coin=$(( RANDOM % 100 ))
    bratio_pct=$(awk -v r="$BROADCAST_RATIO" 'BEGIN {print int(r*100)}')
    if [ "$coin" -lt "$bratio_pct" ]; then
        # Broadcast: pub to every worker
        for hh in "${HARNESSES[@]}"; do
            subj="${WORKER_SUBJ[$hh]}"
            [ -z "$subj" ] && continue
            if nats --server="$NATS_URL" req "$subj" "soak-$PROMPT_IDX-bcast" \
                --replies=0 --reply-timeout=3s --timeout=8s >/dev/null 2>&1; then
                SUCCESS_COUNT[$hh]=$(( SUCCESS_COUNT[$hh] + 1 ))
            else
                ERROR_COUNT[$hh]=$(( ERROR_COUNT[$hh] + 1 ))
            fi
        done
    else
        # Targeted to the round-robin pick
        subj="${WORKER_SUBJ[$h]}"
        if [ -n "$subj" ] && nats --server="$NATS_URL" req "$subj" "soak-$PROMPT_IDX-targeted" \
            --replies=0 --reply-timeout=3s --timeout=8s >/dev/null 2>&1; then
            SUCCESS_COUNT[$h]=$(( SUCCESS_COUNT[$h] + 1 ))
        else
            ERROR_COUNT[$h]=$(( ERROR_COUNT[$h] + 1 ))
        fi
    fi
    PROMPT_IDX=$(( PROMPT_IDX + 1 ))

    # Sample at SAMPLE_INTERVAL boundaries
    now=$(date +%s)
    if [ "$(( now - LAST_SAMPLE_TS ))" -ge "$SAMPLE_INTERVAL" ]; then
        for h in "${HARNESSES[@]}"; do
            pid=$(pgrep -af "orch-agent-shim.*--pane ${WORKER_PANE[$h]}" | awk '{print $1}' | head -1)
            rss=0; fd=0
            if [ -n "$pid" ]; then
                rss=$(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ' || echo 0)
                fd=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l || echo 0)
            fi
            RSS_LAST[$h]=$rss
            FD_LAST[$h]=$fd
            HB_COUNT[$h]=$(grep -cE "^${WORKER_AGENT[$h]}\$" "$HB_LOG" 2>/dev/null || echo 0)
            printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\n' \
                "$now" "$h" "$rss" "$fd" \
                "${SUCCESS_COUNT[$h]}" "${ERROR_COUNT[$h]}" "${HB_COUNT[$h]}" \
                >> "$SAMPLES_FILE"
        done
        LAST_SAMPLE_TS=$now
        elapsed=$(( now - START_TS ))
        log "sample at +${elapsed}s: $(for h in "${HARNESSES[@]}"; do printf '%s=%d/%d ' "$h" "${SUCCESS_COUNT[$h]}" "$PROMPTS_PER_WORKER"; done)"
    fi
done

# Stop the heartbeat collector
kill $HB_BG 2>/dev/null || true

END_TS=$(date +%s)
ELAPSED=$(( END_TS - START_TS ))

# --- final samples (capture end-state regardless of last sample tick) ----
for h in "${HARNESSES[@]}"; do
    pid=$(pgrep -af "orch-agent-shim.*--pane ${WORKER_PANE[$h]}" | awk '{print $1}' | head -1)
    rss=0; fd=0
    if [ -n "$pid" ]; then
        rss=$(ps -o rss= -p "$pid" 2>/dev/null | tr -d ' ' || echo 0)
        fd=$(ls "/proc/$pid/fd" 2>/dev/null | wc -l || echo 0)
    fi
    RSS_LAST[$h]=$rss
    FD_LAST[$h]=$fd
    HB_COUNT[$h]=$(grep -cE "^${WORKER_AGENT[$h]}\$" "$HB_LOG" 2>/dev/null || echo 0)
done

# Expected heartbeats per harness for the actual run window:
# floor(ELAPSED / 30) - 1   (shim's default 30s cadence; -1 for startup grace)
EXPECTED_HB=$(( ELAPSED / 30 - 1 ))
[ "$EXPECTED_HB" -lt 1 ] && EXPECTED_HB=1

# --- report --------------------------------------------------------------
{
    echo "# Soak report"
    echo
    echo "- Started: $(date -u -d "@$START_TS" +%FT%TZ)"
    echo "- Ended:   $(date -u -d "@$END_TS" +%FT%TZ)"
    echo "- Duration: ${ELAPSED}s (target: ${DURATION_SEC}s)"
    echo "- Harnesses: $HARNESSES_CSV"
    echo "- Prompts/worker target: $PROMPTS_PER_WORKER"
    echo "- Broadcast ratio: $BROADCAST_RATIO"
    echo "- Sample interval: ${SAMPLE_INTERVAL}s"
    echo
    echo "## Per-harness summary"
    echo
    echo "| harness | success | error | success-rate | rss start (kB) | rss end (kB) | rss growth % | fd start | fd end | hb count | hb coverage |"
    echo "|---|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|"
    for h in "${HARNESSES[@]}"; do
        s=${SUCCESS_COUNT[$h]}
        e=${ERROR_COUNT[$h]}
        total=$(( s + e ))
        rate=0
        [ "$total" -gt 0 ] && rate=$(( s * 100 / total ))
        rss_s=${RSS_START[$h]:-0}
        rss_e=${RSS_LAST[$h]:-0}
        rss_pct=0
        [ "$rss_s" -gt 0 ] && rss_pct=$(( (rss_e - rss_s) * 100 / rss_s ))
        fd_s=${FD_START[$h]:-0}
        fd_e=${FD_LAST[$h]:-0}
        hb=${HB_COUNT[$h]}
        hb_cov=0
        [ "$EXPECTED_HB" -gt 0 ] && hb_cov=$(( hb * 100 / EXPECTED_HB ))
        printf '| %s | %d | %d | %d%% | %d | %d | %s%% | %d | %d | %d | %d%% |\n' \
            "$h" "$s" "$e" "$rate" "$rss_s" "$rss_e" "$rss_pct" "$fd_s" "$fd_e" "$hb" "$hb_cov"
    done
    echo

    # Findings — auto-flag anomalies
    echo "## Findings"
    echo
    found_any=0
    for h in "${HARNESSES[@]}"; do
        s=${SUCCESS_COUNT[$h]}
        e=${ERROR_COUNT[$h]}
        total=$(( s + e ))
        # Error rate >1%
        if [ "$total" -gt 0 ]; then
            err_pct=$(( e * 100 / total ))
            if [ "$err_pct" -gt 1 ]; then
                echo "- ⚠️  **${h}**: error rate ${err_pct}% (${e}/${total}) — investigate shim/transport"
                found_any=1
            fi
        fi
        # RSS growth >20%
        rss_s=${RSS_START[$h]:-0}
        rss_e=${RSS_LAST[$h]:-0}
        if [ "$rss_s" -gt 0 ]; then
            rss_pct=$(( (rss_e - rss_s) * 100 / rss_s ))
            if [ "$rss_pct" -gt 20 ]; then
                echo "- ⚠️  **${h}**: RSS grew ${rss_pct}% (${rss_s} → ${rss_e} kB) — possible leak"
                found_any=1
            fi
        fi
        # FD growth (any net growth)
        fd_s=${FD_START[$h]:-0}
        fd_e=${FD_LAST[$h]:-0}
        if [ "$fd_e" -gt "$fd_s" ]; then
            growth=$(( fd_e - fd_s ))
            if [ "$growth" -ge 5 ]; then
                echo "- ⚠️  **${h}**: file handles grew by ${growth} (${fd_s} → ${fd_e}) — possible FD leak"
                found_any=1
            fi
        fi
        # Heartbeat coverage <95%
        hb=${HB_COUNT[$h]}
        if [ "$EXPECTED_HB" -gt 0 ]; then
            hb_cov=$(( hb * 100 / EXPECTED_HB ))
            if [ "$hb_cov" -lt 95 ]; then
                echo "- ⚠️  **${h}**: heartbeat coverage ${hb_cov}% (${hb}/${EXPECTED_HB}) — possible substrate flakiness or shim stall"
                found_any=1
            fi
        fi
    done
    if [ "$found_any" -eq 0 ]; then
        echo "- ✅ No anomalies detected. Substrate looks stable across the run."
    fi
    echo

    echo "## Raw samples"
    echo
    echo "Time-series at $SAMPLE_INTERVAL s intervals. Format: ts_unix / harness / rss_kb / fd_count / success / error / hb_count"
    echo
    echo '```'
    cat "$SAMPLES_FILE" 2>/dev/null || echo "(no samples — run too short for the interval)"
    echo '```'
} > "$REPORT"

# Cleanup
sesh down --session=soak 2>/dev/null || true
rm -f "$HOME/.sesh/hub.spawn.lock" 2>/dev/null || true

log "done. report at $REPORT"
echo
cat "$REPORT" | head -60
