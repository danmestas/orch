#!/usr/bin/env bash
# run-tests.sh — host-side launcher for the sesh test bench.
#
# Builds an image that includes a freshly-compiled sesh binary (from
# github.com/danmestas/sesh @ main + github.com/danmestas/EdgeSync @ main,
# sibling-dirs per sesh's go.mod replace directive), the orch tarball
# from `npm pack` of the working tree, and a smoke suite covering the
# sesh comm patterns.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
HERE=$(cd "$(dirname "$0")" && pwd)
IMAGE_TAG="orch-docker-sesh-tests:local"

BUILD=1
SHELL_MODE=0
for arg in "$@"; do
    case $arg in
        --no-build) BUILD=0 ;;
        --shell)    SHELL_MODE=1 ;;
        --help|-h)  sed -n '1,/^set -e/p' "$0" | sed '$d' | sed 's|^# *||'; exit 0 ;;
        *) echo "unknown arg: $arg"; exit 2 ;;
    esac
done

cd "$ROOT"

if [ "$BUILD" -eq 1 ]; then
    echo "[run-tests-sesh] npm pack from $ROOT"
    out=$(npm pack --pack-destination /tmp 2>&1 | tail -1)
    cp "/tmp/$out" "$HERE/orch-pack.tgz"
    echo "[run-tests-sesh] pack: /tmp/$out"

    echo "[run-tests-sesh] docker build $IMAGE_TAG (will compile sesh from source — first build is slow)"
    docker build -t "$IMAGE_TAG" "$HERE"
    rm -f "$HERE/orch-pack.tgz"
fi

if [ "$SHELL_MODE" -eq 1 ]; then
    docker run --rm -it --entrypoint /bin/bash "$IMAGE_TAG"
    exit $?
fi

echo "[run-tests-sesh] docker run"
docker run --rm "$IMAGE_TAG"
