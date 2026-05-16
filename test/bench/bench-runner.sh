#!/usr/bin/env bash
# bench-runner.sh — container-side benchmark worker.
#
# Runs inside the Docker image built by test/bench/measure.sh.
# Measures:
#   1. Round-trip latency: orch-tell (legacy) vs agents.prompt (shim)
#   2. Chunk overhead (bytes per round-trip) for both paths
#   3. Heartbeat bandwidth at fleet sizes 1, 10, 50
#
# Writes /tmp/bench-out/results.json on exit.
#
# Environment (set by measure.sh via docker run -e):
#   BENCH_SAMPLES     number of prompts per path (default 50)
#   BENCH_WARMUP      warm-up rounds to discard (default 5)
#   BENCH_HB_DURATION heartbeat measurement window seconds (default 60)
#   BENCH_HB_INTERVAL heartbeat interval seconds (default 2)
set -uo pipefail

SAMPLES="${BENCH_SAMPLES:-50}"
WARMUP="${BENCH_WARMUP:-5}"
HB_DURATION="${BENCH_HB_DURATION:-60}"
HB_INTERVAL="${BENCH_HB_INTERVAL:-2}"
OUT_DIR=/tmp/bench-out
mkdir -p "$OUT_DIR"

log() { printf '[bench-runner %s] %s\n' "$(date -u +%T)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

# ---------------------------------------------------------------------------
# 0. Bootstrap: NATS server + orch hooks.
# ---------------------------------------------------------------------------
log "starting nats-server"
mkdir -p /tmp/jetstream
nats-server --jetstream --store_dir=/tmp/jetstream --port 4222 \
    >/tmp/nats.log 2>&1 &
NATS_PID=$!
sleep 1
kill -0 $NATS_PID 2>/dev/null || die "nats-server failed: $(tail /tmp/nats.log)"
log "nats-server alive (PID $NATS_PID)"

ORCH_PKG_DIR=/usr/lib/node_modules/@agent-ops/orch
mkdir -p "$HOME/.claude/hooks" "$HOME/.cache"
for f in "$ORCH_PKG_DIR"/hooks/*; do
    [ -f "$f" ] || continue
    ln -sf "$f" "$HOME/.claude/hooks/$(basename "$f")"
done

# Write minimal settings.json so orch-spawn can resolve paths.
sed "s|\$HOME|$HOME|g" "$ORCH_PKG_DIR/settings-snippet.json" \
    | jq 'del(._INSTRUCTIONS)' \
    > "$HOME/.claude/settings.json"

# Start tmux server (needed for orch-spawn / orch-tell).
tmux start-server
log "tmux server started"

# ---------------------------------------------------------------------------
# Helper: compute percentile from a newline-delimited list of ns integers.
# ---------------------------------------------------------------------------
percentile() {
    local p=$1
    sort -n | awk -v p="$p" '
        { vals[NR] = $1 }
        END {
            idx = int(NR * p / 100)
            if (idx < 1) idx = 1
            if (idx > NR) idx = NR
            print vals[idx]
        }
    '
}

# ---------------------------------------------------------------------------
# 1. PATH A — legacy orch-tell
#    Operator publishes to orch.tell; mock claude receives via bridge;
#    mock fires Stop hook → orch.stop.<num> published.
#    Round-trip: t0 = before nats pub; t1 = ts_ns from orch.stop message.
# ---------------------------------------------------------------------------
log "--- PATH A: orch-tell (legacy) ---"

export PATH="/usr/lib/node_modules/@agent-ops/orch/bin:$PATH"

# Spawn a mock worker pane.
PANE_A=$(orch-spawn claude --cwd /tmp --headless --verify 2>/dev/null | tail -1)
[ -n "$PANE_A" ] && [ "${PANE_A:0:1}" = "%" ] || die "orch-spawn failed for PATH A (got: $PANE_A)"
PANE_NUM_A="${PANE_A#%}"
log "worker pane A: $PANE_A"
sleep 0.5

# Start bridge.
BRIDGE_LOG="$HOME/.cache/orch-nats-bridge-a.log"
BRIDGE_PID=$(ORCH_NATS_BRIDGE_LOG="$BRIDGE_LOG" orch-nats-bridge-in --background)
sleep 0.5
kill -0 "$BRIDGE_PID" 2>/dev/null || die "bridge failed"

# Warm up.
log "warming up ($WARMUP rounds)…"
for _ in $(seq 1 "$WARMUP"); do
    nats sub --raw "orch.stop.${PANE_NUM_A}" --count=1 >/dev/null 2>&1 &
    SUB=$!
    nats pub orch.tell "$(jq -nc --arg p "$PANE_A" --arg t "warmup" '{pane:$p,prompt:$t}')" >/dev/null 2>&1
    wait $SUB 2>/dev/null || true
    sleep 0.2
done

log "measuring $SAMPLES samples…"
LATENCY_A_NS=""
BYTES_IN_A=0
BYTES_OUT_A=0
MSGS_IN_A=0
MSGS_OUT_A=0

for i in $(seq 1 "$SAMPLES"); do
    PROMPT="bench-a-prompt-${i}"
    MSG_BODY=$(jq -nc --arg p "$PANE_A" --arg t "$PROMPT" '{pane:$p,prompt:$t}')
    BYTES_IN_A=$((BYTES_IN_A + ${#MSG_BODY}))
    MSGS_IN_A=$((MSGS_IN_A + 1))

    # Subscribe for the stop event before publishing.
    STOP_CAP=$(mktemp)
    nats sub --raw "orch.stop.${PANE_NUM_A}" --count=1 >"$STOP_CAP" 2>&1 &
    SUB=$!

    T0=$(date +%s%N)
    nats pub orch.tell "$MSG_BODY" >/dev/null 2>&1

    # Wait for stop event (max 5s).
    deadline=$(( $(date +%s) + 5 ))
    while kill -0 $SUB 2>/dev/null; do
        [ "$(date +%s)" -ge "$deadline" ] && break
        sleep 0.05
    done
    T1=$(date +%s%N)
    wait $SUB 2>/dev/null || true

    # Extract ts_ns from stop payload if available; fall back to T1.
    if command -v jq >/dev/null 2>&1 && grep -q '"ts_ns"' "$STOP_CAP" 2>/dev/null; then
        TS_NS=$(jq -r '.ts_ns // empty' "$STOP_CAP" 2>/dev/null | head -1)
        [ -n "$TS_NS" ] && T1=$TS_NS
    fi

    LATENCY_NS=$(( T1 - T0 ))
    [ "$LATENCY_NS" -gt 0 ] || LATENCY_NS=1  # guard against clock skew
    LATENCY_A_NS="${LATENCY_A_NS}${LATENCY_NS}\n"

    STOP_BYTES=$(wc -c < "$STOP_CAP" 2>/dev/null || echo 0)
    BYTES_OUT_A=$((BYTES_OUT_A + STOP_BYTES))
    MSGS_OUT_A=$((MSGS_OUT_A + 1))
    rm -f "$STOP_CAP"
done

# Per-round-trip averages.
AVG_BYTES_IN_A=$(( BYTES_IN_A / SAMPLES ))
AVG_BYTES_OUT_A=$(( BYTES_OUT_A / SAMPLES ))

P50_A=$(printf "%b" "$LATENCY_A_NS" | grep -v '^$' | percentile 50)
P95_A=$(printf "%b" "$LATENCY_A_NS" | grep -v '^$' | percentile 95)
P99_A=$(printf "%b" "$LATENCY_A_NS" | grep -v '^$' | percentile 99)
log "PATH A: p50=${P50_A}ns p95=${P95_A}ns p99=${P99_A}ns"

# Stop bridge.
kill "$BRIDGE_PID" 2>/dev/null || true

# ---------------------------------------------------------------------------
# 2. PATH B — agents.prompt (shim)
#    Operator calls `nats request agents.prompt.<token>.<owner>.<pane-enc>`;
#    shim receives, runs mock adapter, returns chunks + terminator.
#    Round-trip: t0 = before request; t1 = when empty terminator received.
# ---------------------------------------------------------------------------
log "--- PATH B: agents.prompt (shim) ---"

# The shim binary is pre-built on the host by measure.sh and injected at
# /usr/local/bin/orch-agent-shim. Check size > 0 to guard against placeholder.
SHIM_BIN=""
if [ -x /usr/local/bin/orch-agent-shim ] && [ "$(wc -c < /usr/local/bin/orch-agent-shim)" -gt 1024 ]; then
    SHIM_BIN=/usr/local/bin/orch-agent-shim
    log "using pre-built shim: $SHIM_BIN"
fi

LATENCY_B_NS=""
MSGS_IN_B=0
MSGS_OUT_B=0
AVG_BYTES_IN_B=0
AVG_BYTES_OUT_B=0

if [ -z "$SHIM_BIN" ]; then
    log "SKIP: orch-agent-shim not available; PATH B skipped"
    P50_B="null"
    P95_B="null"
    P99_B="null"
else
    # Spawn a mock worker pane for the shim path.
    PANE_B=$(orch-spawn claude --cwd /tmp --headless --verify 2>/dev/null | tail -1)
    [ -n "$PANE_B" ] && [ "${PANE_B:0:1}" = "%" ] || die "orch-spawn failed for PATH B"
    log "worker pane B: $PANE_B"

    # Start shim with a 2s heartbeat interval for the bench.
    SHIM_LOG="/tmp/bench-shim.log"
    "$SHIM_BIN" \
        --agent claude-code \
        --pane "$PANE_B" \
        --nats nats://localhost:4222 \
        --interval "${HB_INTERVAL}s" \
        >"$SHIM_LOG" 2>&1 &
    SHIM_PID=$!
    sleep 1
    kill -0 $SHIM_PID 2>/dev/null || die "shim failed to start: $(cat "$SHIM_LOG")"
    log "shim alive (PID $SHIM_PID)"

    # Resolve the agents.prompt subject.
    # Format: agents.prompt.<token>.<owner>.<pane-enc>
    # encodePane: strip '%' prefix and prepend "pct" — e.g. %37 → pct37.
    OWNER=$(id -un)
    PANE_B_ENC="pct${PANE_B#%}"
    PROMPT_SUBJECT="agents.prompt.cc.${OWNER}.${PANE_B_ENC}"
    log "prompt subject: $PROMPT_SUBJECT"

    # Warm up.
    log "warming up ($WARMUP rounds)…"
    for _ in $(seq 1 "$WARMUP"); do
        nats request "$PROMPT_SUBJECT" "warmup" >/dev/null 2>&1 || true
        sleep 0.2
    done

    log "measuring $SAMPLES samples…"
    LATENCY_B_NS=""
    BYTES_IN_B=0
    BYTES_OUT_B=0
    MSGS_IN_B=0
    MSGS_OUT_B=0

    for i in $(seq 1 "$SAMPLES"); do
        PROMPT="bench-b-prompt-${i}"
        BYTES_IN_B=$((BYTES_IN_B + ${#PROMPT}))
        MSGS_IN_B=$((MSGS_IN_B + 1))

        # Capture reply. The shim sends ack + response chunks + empty terminator.
        # `nats request` waits for the first reply message; we time full round-trip
        # to include terminator delivery (shim streams are short in the mock path).
        REPLY_CAP=$(mktemp)
        T0=$(date +%s%N)
        nats request "$PROMPT_SUBJECT" "$PROMPT" --timeout 5s >"$REPLY_CAP" 2>&1 || true
        T1=$(date +%s%N)

        LATENCY_NS=$(( T1 - T0 ))
        [ "$LATENCY_NS" -gt 0 ] || LATENCY_NS=1
        LATENCY_B_NS="${LATENCY_B_NS}${LATENCY_NS}\n"

        REPLY_BYTES=$(wc -c < "$REPLY_CAP" 2>/dev/null || echo 0)
        BYTES_OUT_B=$((BYTES_OUT_B + REPLY_BYTES))
        MSGS_OUT_B=$((MSGS_OUT_B + 1))
        rm -f "$REPLY_CAP"
    done

    AVG_BYTES_IN_B=$(( BYTES_IN_B / SAMPLES ))
    AVG_BYTES_OUT_B=$(( BYTES_OUT_B / SAMPLES ))

    P50_B=$(printf "%b" "$LATENCY_B_NS" | grep -v '^$' | percentile 50)
    P95_B=$(printf "%b" "$LATENCY_B_NS" | grep -v '^$' | percentile 95)
    P99_B=$(printf "%b" "$LATENCY_B_NS" | grep -v '^$' | percentile 99)
    log "PATH B: p50=${P50_B}ns p95=${P95_B}ns p99=${P99_B}ns"

    kill $SHIM_PID 2>/dev/null || true
fi

# ---------------------------------------------------------------------------
# 3. Heartbeat bandwidth at fleet sizes 1, 10, 50.
#    For each fleet size: spawn N shims with HB_INTERVAL cadence; sub
#    agents.hb.> for HB_DURATION seconds; measure total bytes received.
# ---------------------------------------------------------------------------
log "--- Heartbeat bandwidth (fleet sizes 1, 10, 50) ---"

HB_RESULTS="[]"

if [ -z "$SHIM_BIN" ]; then
    log "SKIP: shim not available; heartbeat bench skipped"
else
    for FLEET in 1 10 50; do
        log "fleet size $FLEET (${HB_DURATION}s window)…"
        SHIM_PIDS=()
        for _ in $(seq 1 "$FLEET"); do
            PANE_HB=$(orch-spawn claude --cwd /tmp --headless 2>/dev/null | tail -1) || true
            [ -z "$PANE_HB" ] && continue
            "$SHIM_BIN" \
                --agent claude-code \
                --pane "$PANE_HB" \
                --nats nats://localhost:4222 \
                --interval "${HB_INTERVAL}s" \
                >/dev/null 2>&1 &
            SHIM_PIDS+=($!)
        done
        sleep 1  # let shims register

        HB_CAP=$(mktemp)
        # Subscribe to all heartbeat subjects for the window duration.
        timeout "$HB_DURATION" nats sub --raw 'agents.hb.>' >"$HB_CAP" 2>/dev/null &
        SUB_HB=$!
        sleep "$HB_DURATION"
        kill $SUB_HB 2>/dev/null || true
        wait $SUB_HB 2>/dev/null || true

        TOTAL_BYTES=$(wc -c < "$HB_CAP" 2>/dev/null || echo 0)
        BYTES_PER_SEC=0
        if [ "$HB_DURATION" -gt 0 ] && [ "$TOTAL_BYTES" -gt 0 ]; then
            BYTES_PER_SEC=$(echo "scale=2; $TOTAL_BYTES / $HB_DURATION" | bc)
        fi
        log "fleet=$FLEET total_bytes=$TOTAL_BYTES bytes/s=$BYTES_PER_SEC"
        rm -f "$HB_CAP"

        HB_RESULTS=$(printf '%s' "$HB_RESULTS" | jq \
            --argjson fleet "$FLEET" \
            --argjson dur "$HB_DURATION" \
            --argjson total "$TOTAL_BYTES" \
            --argjson bps "$BYTES_PER_SEC" \
            '. + [{"fleet_size": $fleet, "duration_s": $dur, "total_bytes": $total, "bytes_per_sec": $bps}]')

        # Tear down shims.
        for pid in "${SHIM_PIDS[@]}"; do
            kill "$pid" 2>/dev/null || true
        done
        sleep 0.5
    done
fi

# ---------------------------------------------------------------------------
# 4. Write results.json.
# ---------------------------------------------------------------------------
log "writing $OUT_DIR/results.json"

# Build latency arrays from ns strings.
to_json_array() {
    # Reads "ns1\nns2\n..." from argument, emits JSON array.
    printf "%b" "$1" | grep -v '^$' | jq -s 'map(tonumber)' 2>/dev/null || echo "[]"
}

LA=$(to_json_array "$LATENCY_A_NS")
if [ "$P50_B" != "null" ]; then
    LB=$(to_json_array "$LATENCY_B_NS")
else
    LB="[]"
fi

jq -n \
    --argjson samples "$SAMPLES" \
    --argjson warmup "$WARMUP" \
    --argjson la "$LA" \
    --argjson lb "$LB" \
    --argjson legacy_msgs_in  "$MSGS_IN_A" \
    --argjson legacy_msgs_out "$MSGS_OUT_A" \
    --argjson legacy_bytes_in  "$AVG_BYTES_IN_A" \
    --argjson legacy_bytes_out "$AVG_BYTES_OUT_A" \
    --argjson shim_msgs_in  "$MSGS_IN_B" \
    --argjson shim_msgs_out "$MSGS_OUT_B" \
    --argjson shim_bytes_in  "$AVG_BYTES_IN_B" \
    --argjson shim_bytes_out "$AVG_BYTES_OUT_B" \
    --argjson hb "$HB_RESULTS" \
    '{
        latency: {
            samples: $samples,
            warmup: $warmup,
            legacy_ns: $la,
            shim_ns: $lb
        },
        chunk: {
            legacy: {
                msgs_in:   $legacy_msgs_in,
                msgs_out:  $legacy_msgs_out,
                bytes_in:  $legacy_bytes_in,
                bytes_out: $legacy_bytes_out
            },
            shim: {
                msgs_in:   $shim_msgs_in,
                msgs_out:  $shim_msgs_out,
                bytes_in:  $shim_bytes_in,
                bytes_out: $shim_bytes_out
            }
        },
        heartbeat: $hb
    }' > "$OUT_DIR/results.json"

log "results.json written"
cat "$OUT_DIR/results.json"

# Shut down NATS server.
kill $NATS_PID 2>/dev/null || true
log "done"
