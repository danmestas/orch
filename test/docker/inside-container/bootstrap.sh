#!/usr/bin/env bash
# bootstrap.sh — runs inside the container as ENTRYPOINT.
#
# Sets up the environment (NATS server, orch hooks symlinked, tmux server
# running) and then invokes tests.sh inside a tmux session. Tests run
# synchronously; the container exits with the test script's exit code so
# `docker run` returns pass/fail directly.
set -uo pipefail

log() { printf '[bootstrap %s] %s\n' "$(date -u +%T)" "$*"; }

ORCH_PKG_DIR=/usr/lib/node_modules/@agent-ops/orch

# 1. Start nats-server with JetStream (so we have parity with sesh's
#    JetStream-backed hub; the shim uses core NATS for the Synadia bus).
log "starting nats-server"
mkdir -p /tmp/jetstream
nats-server --jetstream --store_dir=/tmp/jetstream --port 4222 \
    >/tmp/nats.log 2>&1 &
NATS_PID=$!
sleep 1
kill -0 $NATS_PID 2>/dev/null || { log "nats-server failed to start"; tail /tmp/nats.log; exit 1; }
log "nats-server alive (PID $NATS_PID)"

# 2. Wire CC hooks: orch's postinstall normally does this, but inside a
#    fresh container the postinstall may not have run (npm install -g of a
#    tgz can skip it depending on flags). Idempotent re-link here.
log "wiring CC hooks"
mkdir -p "$HOME/.claude/hooks" "$HOME/.cache"
for f in "$ORCH_PKG_DIR"/hooks/*; do
    [ -f "$f" ] || continue
    ln -sf "$f" "$HOME/.claude/hooks/$(basename "$f")"
done

# 3. Settings: expand $HOME and strip _INSTRUCTIONS from snippet, write
#    as the active settings.json. Fresh container has none, so a direct
#    copy is fine (no merge needed).
log "writing $HOME/.claude/settings.json"
sed "s|\$HOME|$HOME|g" "$ORCH_PKG_DIR/settings-snippet.json" \
    | jq 'del(._INSTRUCTIONS)' \
    > "$HOME/.claude/settings.json"

# 4. Clone the public wardrobe into suit's content path. The default
#    `suit init` pulls the minimal suit-template; we want the full
#    wardrobe so suit list / suit prepare exercise real outfits.
log "cloning wardrobe → suit content"
SUIT_CONTENT="$HOME/.local/share/suit/content"
mkdir -p "$(dirname "$SUIT_CONTENT")"
if [ -d "$SUIT_CONTENT" ]; then
    rm -rf "$SUIT_CONTENT"
fi
if ! git clone --depth 1 https://github.com/danmestas/wardrobe.git "$SUIT_CONTENT" >/tmp/wardrobe-clone.log 2>&1; then
    log "wardrobe clone failed:"; tail /tmp/wardrobe-clone.log
    # Non-fatal: tests T2/T8 will note the gap.
fi

# 5. tmux session — orch-spawn requires a running tmux server. We launch
#    one detached and run tests.sh inside its first pane. The pane runs to
#    completion and writes its exit code to /tmp/test-rc; we poll up to
#    120s and exit with that code.
#
#    When MOCK_USE_SHIM=1, pass SHIM_HB_INTERVAL=2s so T11 can verify
#    heartbeat cadence within a 6s window.
log "launching tests in tmux"
rm -f /tmp/test-rc /tmp/test-out.log
EXTRA_ENV=""
if [ "${MOCK_USE_SHIM:-0}" = "1" ]; then
    export MOCK_USE_SHIM=1
    export SHIM_HB_INTERVAL=2s
    EXTRA_ENV="MOCK_USE_SHIM=1 SHIM_HB_INTERVAL=2s"
    log "MOCK_USE_SHIM=1 — shim-mode enabled; heartbeat interval = 2s"
fi
tmux new-session -d -s orch-tests \
    "env NATS_URL=nats://localhost:4222 ${EXTRA_ENV} bash /usr/local/bin/tests.sh > /tmp/test-out.log 2>&1; echo \$? > /tmp/test-rc"

deadline=$(( $(date +%s) + 180 ))
while [ ! -f /tmp/test-rc ]; do
    [ "$(date +%s)" -ge "$deadline" ] && { log "TIMEOUT — tests did not finish in 180s"; break; }
    sleep 1
done

log "tests done — output follows"
echo "------------------------------------------------------------"
cat /tmp/test-out.log
echo "------------------------------------------------------------"

RC=$(cat /tmp/test-rc 2>/dev/null || echo 99)
log "exit code = $RC"
exit "$RC"
