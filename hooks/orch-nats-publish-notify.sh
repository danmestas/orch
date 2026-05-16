#!/usr/bin/env bash
# Notification hook companion — publishes a NATS message when CC fires the
# Notification hook (attention-needed event). Sibling to orch-notify-marker.sh.
#
# Subject: orch.notify.<pane_num>
# Body:    JSON  {event, pane_id, session_id, message, cwd, ts_ns, ts_iso}
#
# Best-effort; silently no-ops if NATS is unreachable.
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0

PANE_NUM="${ORCH_PANE_ID#%}"
SUBJECT_PREFIX="${ORCH_NATS_SUBJECT_PREFIX:-harness}"
SUBJECT="${SUBJECT_PREFIX}.notify.${PANE_NUM}"

PAYLOAD=$(cat)
SESSION_ID=""
MESSAGE=""
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
    MESSAGE=$(printf '%s' "$PAYLOAD" | jq -r '.message // ""' 2>/dev/null || true)
fi

NS=$(date +%s%N)
ISO=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)

if command -v jq >/dev/null 2>&1; then
    BODY=$(jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg message "$MESSAGE" \
        --arg cwd "$PWD" \
        --argjson ts_ns "$NS" \
        --arg ts_iso "$ISO" \
        '{event:"notify", pane_id:$pane_id, session_id:$session_id, message:$message, cwd:$cwd, ts_ns:$ts_ns, ts_iso:$ts_iso}')
else
    BODY=$(printf '{"event":"notify","pane_id":"%s","session_id":"%s","message":"%s","cwd":"%s","ts_ns":%s,"ts_iso":"%s"}' \
        "$ORCH_PANE_ID" "$SESSION_ID" "$MESSAGE" "$PWD" "$NS" "$ISO")
fi

nats pub --timeout=1s "$SUBJECT" "$BODY" >/dev/null 2>&1 || true

exit 0
