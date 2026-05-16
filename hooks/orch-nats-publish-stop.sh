#!/usr/bin/env bash
# Stop hook companion — publishes a NATS message announcing the Stop, alongside
# (not instead of) the marker file written by orch-stop-marker.sh.
#
# Subject: orch.stop.<pane_num>     (pane id "%37" becomes "37")
# Body:    JSON  {pane_id, session_id, cwd, ts_ns, ts_iso}
#
# Best-effort. If the `nats` CLI is missing, or no NATS server is reachable,
# this hook silently no-ops. The marker-file path stays authoritative; NATS
# is an additional fan-out for sesh-aware listeners.
#
# Only fires when ORCH_PANE_ID is set, matching the marker hook's
# "this CC instance is a harness child" gate. Safe to install globally.
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0

# NATS subject tokens can't contain `%`; strip the prefix to get the numeric id.
PANE_NUM="${ORCH_PANE_ID#%}"
SUBJECT_PREFIX="${ORCH_NATS_SUBJECT_PREFIX:-harness}"
SUBJECT="${SUBJECT_PREFIX}.stop.${PANE_NUM}"

# Read the Stop payload — same shape the marker hook consumes.
PAYLOAD=$(cat)
SESSION_ID=""
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
fi

NS=$(date +%s%N)
ISO=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)

# Build the body. Plain bash printf fallback if jq is absent.
if command -v jq >/dev/null 2>&1; then
    BODY=$(jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$PWD" \
        --argjson ts_ns "$NS" \
        --arg ts_iso "$ISO" \
        '{event:"stop", pane_id:$pane_id, session_id:$session_id, cwd:$cwd, ts_ns:$ts_ns, ts_iso:$ts_iso}')
else
    BODY=$(printf '{"event":"stop","pane_id":"%s","session_id":"%s","cwd":"%s","ts_ns":%s,"ts_iso":"%s"}' \
        "$ORCH_PANE_ID" "$SESSION_ID" "$PWD" "$NS" "$ISO")
fi

# Publish best-effort. Short --timeout so a missing server doesn't stall the
# hook past its 5s budget. Errors to /dev/null so the hook exits 0 regardless.
nats pub --timeout=1s "$SUBJECT" "$BODY" >/dev/null 2>&1 || true

exit 0
