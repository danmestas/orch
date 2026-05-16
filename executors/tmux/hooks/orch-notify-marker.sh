#!/usr/bin/env bash
# Notification hook — fires when CC needs user attention mid-turn (e.g. permission
# prompts, "Claude is waiting for your input" idle warnings).
# Writes ~/.cache/orch-stop/<ORCH_PANE_ID>.notify with the message so the
# parent harness can react (approve, escalate, or just push the user a heads-up).
[ -n "${ORCH_PANE_ID:-}" ] || exit 0

DIR="${ORCH_STOP_DIR:-$HOME/.cache/orch-stop}"
mkdir -p "$DIR"

PAYLOAD=$(cat)
MESSAGE=""
SESSION_ID=""
if command -v jq >/dev/null 2>&1; then
    MESSAGE=$(printf '%s' "$PAYLOAD" | jq -r '.message // ""' 2>/dev/null || true)
    SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
fi

NS=$(date +%s%N)

TARGET="$DIR/$ORCH_PANE_ID.notify"
TMP="$TARGET.$$.tmp"
{
    printf 'ts_ns=%s\n' "$NS"
    printf 'event=Notification\n'
    printf 'pane_id=%s\n' "$ORCH_PANE_ID"
    printf 'session_id=%s\n' "$SESSION_ID"
    printf 'cwd=%s\n' "$PWD"
    printf 'message=%s\n' "$MESSAGE"
} > "$TMP"
mv -f "$TMP" "$TARGET"

# Append-only event log so a parent that was offline can replay history.
LOG="${ORCH_EVENT_LOG:-$DIR/events.log}"
if command -v jq >/dev/null 2>&1; then
    jq -nc \
        --argjson ts_ns "$NS" \
        --arg event "Notification" \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$PWD" \
        --arg message "$MESSAGE" \
        '{ts_ns:$ts_ns, event:$event, pane_id:$pane_id, session_id:$session_id, cwd:$cwd, message:$message}' >> "$LOG"
fi

exit 0
