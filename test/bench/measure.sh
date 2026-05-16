#!/usr/bin/env bash
# measure.sh — host-side benchmark runner: latency + cost of orch-tell vs agents.prompt.
#
# Builds a Docker image (reusing test/docker/Dockerfile) that contains NATS,
# orch, and the mock claude. Runs both code paths against the same mock worker
# and emits a Markdown table of results.
#
# Usage:
#   test/bench/measure.sh                  # build + run, print results
#   test/bench/measure.sh --no-build       # skip docker build, reuse last image
#   test/bench/measure.sh --save-baseline  # also write test/bench/baselines/<ts>.md
#   test/bench/measure.sh --help
#
# Output: test/bench/results-<utc-timestamp>.md + stdout summary.
#
# The bench is intentionally opt-in — not included in the default CI run. See
# test/docker/run-tests.sh --with-bench for the CI integration path.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
BENCH_DIR="$ROOT/test/bench"
IMAGE_TAG="orch-bench:local"

BUILD=1
SAVE_BASELINE=0

usage() {
    sed -n '1,/^set -e/p' "$0" | sed '$d' | sed 's|^# *||'
    exit 0
}

for arg in "$@"; do
    case $arg in
        --no-build)      BUILD=0 ;;
        --save-baseline) SAVE_BASELINE=1 ;;
        --help|-h)       usage ;;
        *) echo "measure.sh: unknown arg: $arg" >&2; exit 2 ;;
    esac
done

log() { printf '[bench %s] %s\n' "$(date -u +%T)" "$*"; }

TS=$(date -u +%Y%m%dT%H%M%SZ)
RESULT_FILE="$BENCH_DIR/results-${TS}.md"

cd "$ROOT"

# ---------------------------------------------------------------------------
# 1. Build (or reuse) the Docker image.
# ---------------------------------------------------------------------------
if [ "$BUILD" -eq 1 ]; then
    log "npm pack from $ROOT"
    npm_pack_out=$(npm pack --pack-destination /tmp 2>&1 | tail -1)
    PACK_PATH="/tmp/$npm_pack_out"
    [ -f "$PACK_PATH" ] || { log "ERROR: pack failed: $npm_pack_out"; exit 1; }
    cp "$PACK_PATH" "$BENCH_DIR/orch-pack.tgz"
    log "pack: $PACK_PATH ($(wc -c < "$PACK_PATH") bytes)"

    # Copy mock-claude from the test/docker tree so the bench Dockerfile can use it.
    cp "$ROOT/test/docker/inside-container/mock-agents/claude" "$BENCH_DIR/mock-claude"

    # Attempt to pre-build orch-agent-shim on the host (requires Go).
    # If Go is unavailable, the PATH B (agents.prompt) bench is skipped gracefully.
    SHIM_BIN="$BENCH_DIR/orch-agent-shim"
    if command -v go >/dev/null 2>&1; then
        log "building orch-agent-shim on host (Go $(go version | awk '{print $3}'))"
        GOARCH=$(docker info --format '{{.Architecture}}' 2>/dev/null | sed 's/x86_64/amd64/;s/aarch64/arm64/') || GOARCH=""
        if [ -f "$ROOT/cmd/orch-agent-shim/main.go" ]; then
            GOARCH="${GOARCH:-$(go env GOARCH)}" \
            GOOS=linux \
            go build -o "$SHIM_BIN" "$ROOT/cmd/orch-agent-shim/" \
                && log "shim built → $SHIM_BIN" \
                || { log "WARN: shim build failed; PATH B will be skipped"; rm -f "$SHIM_BIN"; }
        else
            log "WARN: cmd/orch-agent-shim/main.go not found; PATH B skipped"
        fi
    else
        log "WARN: go not found on PATH; PATH B (agents.prompt) bench will be skipped"
        touch "$SHIM_BIN"  # placeholder so COPY doesn't fail
    fi

    log "docker build $IMAGE_TAG"
    docker build -t "$IMAGE_TAG" "$BENCH_DIR"
    rm -f "$BENCH_DIR/orch-pack.tgz" "$BENCH_DIR/mock-claude" "$BENCH_DIR/orch-agent-shim"
else
    log "skipping build — reusing $IMAGE_TAG"
fi

# ---------------------------------------------------------------------------
# 2. Run the benchmark inside the container.
#    The container script writes /tmp/bench-results.json on exit.
#    We mount /tmp/bench-out as the output directory.
# ---------------------------------------------------------------------------
BENCH_OUT=$(mktemp -d)
log "running benchmark container → $BENCH_OUT"

docker run --rm \
    -e BENCH_SAMPLES="${BENCH_SAMPLES:-50}" \
    -e BENCH_WARMUP="${BENCH_WARMUP:-5}" \
    -e BENCH_HB_DURATION="${BENCH_HB_DURATION:-60}" \
    -e BENCH_HB_INTERVAL="${BENCH_HB_INTERVAL:-2}" \
    -v "$BENCH_OUT:/tmp/bench-out" \
    --entrypoint /usr/local/bin/bench-runner.sh \
    "$IMAGE_TAG" \
    | tee /tmp/bench-run.log

# The container writes /tmp/bench-out/results.json
RESULTS_JSON="$BENCH_OUT/results.json"
[ -f "$RESULTS_JSON" ] || { log "ERROR: container did not produce results.json"; cat /tmp/bench-run.log; exit 1; }

# ---------------------------------------------------------------------------
# 3. Render Markdown table from results JSON.
# ---------------------------------------------------------------------------
log "rendering $RESULT_FILE"

jq -r '
def pct(arr; p):
  (arr | sort | .[(p/100 * length | floor)]);

def fmtms(ns): (ns / 1e6 | "*" + (. * 10 | round | . / 10 | tostring) + " ms*");

def tbl_row(label; arr):
  "| \(label) | \(fmtms(pct(arr;50))) | \(fmtms(pct(arr;95))) | \(fmtms(pct(arr;99))) |";

"## Round-trip latency (p50 / p95 / p99)",
"",
"Sample size: \(.latency.samples) prompts, warm-up \(.latency.warmup) discarded.",
"",
"| Path | p50 | p95 | p99 |",
"|------|-----|-----|-----|",
tbl_row("orch-tell (legacy)"; .latency.legacy_ns),
tbl_row("agents.prompt (shim)"; .latency.shim_ns),
"",
"## Chunk overhead (bytes per round-trip)",
"",
"| Path | Msgs in | Msgs out | Bytes in | Bytes out | Total bytes |",
"|------|---------|----------|----------|-----------|-------------|",
"| orch-tell (legacy) | \(.chunk.legacy.msgs_in) | \(.chunk.legacy.msgs_out) | \(.chunk.legacy.bytes_in) | \(.chunk.legacy.bytes_out) | \(.chunk.legacy.bytes_in + .chunk.legacy.bytes_out) |",
"| agents.prompt (shim) | \(.chunk.shim.msgs_in) | \(.chunk.shim.msgs_out) | \(.chunk.shim.bytes_in) | \(.chunk.shim.bytes_out) | \(.chunk.shim.bytes_in + .chunk.shim.bytes_out) |",
"",
"## Heartbeat bandwidth",
"",
"| Fleet size | agents.hb bytes/s (measured) | extrapolated ×10 | extrapolated ×50 |",
"|------------|------------------------------|-----------------|-----------------|",
( .heartbeat[] |
  "| \(.fleet_size) | \(.bytes_per_sec | round) | \((.bytes_per_sec * 10 | round)) | \((.bytes_per_sec * 50 | round)) |"
)
' "$RESULTS_JSON" > /tmp/bench-table.md

cat > "$RESULT_FILE" <<EOF
# Benchmark: orch-tell vs agents.prompt

**Run:** $TS
**Host:** $(uname -n)
**Kernel:** $(uname -r)
**Docker image:** $IMAGE_TAG

$(cat /tmp/bench-table.md)

---
*Generated by test/bench/measure.sh — measurement only, no optimisations applied.*
EOF

echo
log "=== RESULTS ==="
cat "$RESULT_FILE"
echo

# ---------------------------------------------------------------------------
# 4. Optionally write to baselines/.
# ---------------------------------------------------------------------------
if [ "$SAVE_BASELINE" -eq 1 ]; then
    BASELINE_FILE="$BENCH_DIR/baselines/baseline-${TS}.md"
    cp "$RESULT_FILE" "$BASELINE_FILE"
    log "baseline written → $BASELINE_FILE"
fi

rm -rf "$BENCH_OUT"
log "done — results at $RESULT_FILE"
