#!/usr/bin/env bash
# run-tests.sh — host-side helper that builds the Docker image (after
# pack'ing the current working tree) and runs the smoke suite. Exits with
# the container's exit code so CI can gate on this directly.
#
# Usage:
#   ./test/docker/run-tests.sh                # build + run
#   ./test/docker/run-tests.sh --no-build     # re-use existing image
#   ./test/docker/run-tests.sh --shell        # drop into shell after entry
#   ./test/docker/run-tests.sh --with-bench   # also run latency benchmark
#                                             # (opt-in; adds ~2 min)
#   ./test/docker/run-tests.sh --with-shim    # enable T9/T10/T11 adapter
#                                             # contract tests (MOCK_USE_SHIM=1)
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
IMAGE_TAG="orch-docker-tests:local"

BUILD=1
SHELL_MODE=0
WITH_BENCH=0
USE_SHIM=0
for arg in "$@"; do
    case $arg in
        --no-build)    BUILD=0 ;;
        --shell)       SHELL_MODE=1 ;;
        --with-bench)  WITH_BENCH=1 ;;
        --with-shim)   USE_SHIM=1 ;;
        --help|-h)
            sed -n '1,/^set -e/p' "$0" | sed '$d' | sed 's|^# *||'
            exit 0 ;;
        *) echo "unknown arg: $arg"; exit 2 ;;
    esac
done

cd "$ROOT"

if [ "$BUILD" -eq 1 ]; then
    echo "[run-tests] npm pack from $ROOT"
    npm_pack_out=$(npm pack --pack-destination /tmp 2>&1 | tail -1)
    PACK_PATH="/tmp/$npm_pack_out"
    [ -f "$PACK_PATH" ] || { echo "pack failed: $npm_pack_out"; exit 1; }
    cp "$PACK_PATH" "$HERE/orch-pack.tgz"
    echo "[run-tests] pack: $PACK_PATH ($(wc -c < "$PACK_PATH") bytes)"

    # Create a Go source tarball so the Docker build stage can compile
    # orch-agent-shim without requiring the full git repo on the host.
    echo "[run-tests] creating Go source tarball for shim build"
    (cd "$ROOT" && tar -cf "$HERE/orch-src.tar" \
        go.mod go.sum \
        cmd/orch-agent-shim \
        internal/shim \
        internal/adapter)

    echo "[run-tests] docker build $IMAGE_TAG"
    docker build -t "$IMAGE_TAG" "$HERE"
    rm -f "$HERE/orch-pack.tgz" "$HERE/orch-src.tar"
fi

if [ "$SHELL_MODE" -eq 1 ]; then
    echo "[run-tests] interactive shell mode"
    docker run --rm -it --entrypoint /bin/bash "$IMAGE_TAG"
    exit $?
fi

DOCKER_RUN_FLAGS=(--rm)
if [ "$USE_SHIM" -eq 1 ] || [ "${MOCK_USE_SHIM:-0}" = "1" ]; then
    DOCKER_RUN_FLAGS+=(-e MOCK_USE_SHIM=1)
    echo "[run-tests] MOCK_USE_SHIM=1 — running adapter contract tests (T9/T10/T11)"
fi

echo "[run-tests] docker run"
docker run "${DOCKER_RUN_FLAGS[@]}" "$IMAGE_TAG"

if [ "$WITH_BENCH" -eq 1 ]; then
    echo "[run-tests] running latency benchmark (--with-bench)"
    BENCH_ARGS=()
    [ "$BUILD" -eq 0 ] && BENCH_ARGS+=(--no-build)
    bash "$ROOT/test/bench/measure.sh" "${BENCH_ARGS[@]}"
fi
