#!/usr/bin/env bash
# soak.sh — long-running stability harness for the Synadia substrate.
#
# Reuses the test/docker-sesh/ image (sesh hub + shim + 4 harness mocks),
# spawns N workers, drives M prompts each over a wall-clock window, samples
# per-worker resource + protocol metrics on a fixed interval, then emits a
# markdown report with PASS/SKIP/anomaly-flagged findings.
#
# Out-of-band relative to the docker-sesh bench: this harness invokes the
# same image but with SOAK_MODE=1 so the in-container entrypoint runs the
# soak loop instead of the smoke-suite tests.sh.
#
# Implements orch#90 (filed as part of the post-Synadia-migration e2e plan).
#
# Usage:
#   ./test/bench/soak.sh                              # default 60min, 4 harnesses
#   SOAK_DURATION=2m ./test/bench/soak.sh             # fast local validation
#   SOAK_HARNESSES=claude,codex SOAK_PROMPTS_PER_WORKER=20 ./test/bench/soak.sh
#   ./test/bench/soak.sh --no-build                   # reuse existing image
#
# Env:
#   SOAK_HARNESSES            csv  (default: claude,codex,pi,gemini)
#   SOAK_PROMPTS_PER_WORKER   int  (default: 100)
#   SOAK_DURATION             dur  (default: 60m; max wall clock; harness exits
#                                  at min(duration, all-prompts-completed))
#   SOAK_BROADCAST_RATIO      float 0-1 (default: 0.5 → half broadcast, half targeted)
#   SOAK_SAMPLE_INTERVAL      sec  (default: 60; how often metrics are sampled)
#   SOAK_OUTPUT               path (default: test/bench/soak-<utc-timestamp>.md)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
IMAGE_TAG="orch-docker-sesh-tests:local"

# --- args ----------------------------------------------------------------
BUILD=1
for arg in "$@"; do
    case $arg in
        --no-build) BUILD=0 ;;
        --help|-h)
            sed -n '1,/^set -e/p' "$0" | sed '$d' | sed 's|^# *||'
            exit 0 ;;
        *) echo "unknown arg: $arg" >&2; exit 2 ;;
    esac
done

# --- defaults ------------------------------------------------------------
SOAK_HARNESSES="${SOAK_HARNESSES:-claude,codex,pi,gemini}"
SOAK_PROMPTS_PER_WORKER="${SOAK_PROMPTS_PER_WORKER:-100}"
SOAK_DURATION="${SOAK_DURATION:-60m}"
SOAK_BROADCAST_RATIO="${SOAK_BROADCAST_RATIO:-0.5}"
SOAK_SAMPLE_INTERVAL="${SOAK_SAMPLE_INTERVAL:-60}"
SOAK_OUTPUT="${SOAK_OUTPUT:-$ROOT/test/bench/soak-$(date -u +%Y%m%dT%H%M%SZ).md}"

# --- build image ---------------------------------------------------------
cd "$ROOT"
if [ "$BUILD" -eq 1 ]; then
    echo "[soak] npm pack from $ROOT"
    out=$(npm pack --pack-destination /tmp 2>&1 | tail -1)
    cp "/tmp/$out" "$HERE/../docker-sesh/orch-pack.tgz"
    echo "[soak] docker build $IMAGE_TAG (reuses test/docker-sesh/Dockerfile)"
    docker build -t "$IMAGE_TAG" "$HERE/../docker-sesh/"
    rm -f "$HERE/../docker-sesh/orch-pack.tgz"
fi

# --- run -----------------------------------------------------------------
echo "[soak] launching"
echo "[soak]   harnesses=$SOAK_HARNESSES prompts/worker=$SOAK_PROMPTS_PER_WORKER"
echo "[soak]   duration=$SOAK_DURATION broadcast=$SOAK_BROADCAST_RATIO sample=${SOAK_SAMPLE_INTERVAL}s"
echo "[soak]   report → $SOAK_OUTPUT"

mkdir -p "$(dirname "$SOAK_OUTPUT")"
# Pre-create the report file so the docker volume mount binds to a file, not
# a directory (docker creates the path as a dir if it doesn't exist).
: > "$SOAK_OUTPUT"

# Mount the in-container soak runner from this repo and override entrypoint.
docker run --rm \
    -e SOAK_MODE=1 \
    -e SOAK_HARNESSES \
    -e SOAK_PROMPTS_PER_WORKER \
    -e SOAK_DURATION \
    -e SOAK_BROADCAST_RATIO \
    -e SOAK_SAMPLE_INTERVAL \
    -v "$HERE/soak-runner.sh:/usr/local/bin/soak-runner.sh:ro" \
    -v "$SOAK_OUTPUT:/tmp/soak-report.md" \
    --entrypoint /usr/local/bin/soak-runner.sh \
    "$IMAGE_TAG"

echo "[soak] report saved → $SOAK_OUTPUT"
echo ""
tail -50 "$SOAK_OUTPUT"
