#!/usr/bin/env bash
# Stop hook — fires every time the host claude-code instance finishes a turn.
# Writes a marker file at ~/.cache/orch-stop/<ORCH_PANE_ID>.event
# *only* when the spawning side set ORCH_PANE_ID for this CC instance.
# Other CC instances no-op so this hook is safe to install globally.
[ -n "${ORCH_PANE_ID:-}" ] || exit 0

DIR="${ORCH_STOP_DIR:-$HOME/.cache/orch-stop}"
mkdir -p "$DIR"

# Capture the JSON payload (session_id, transcript_path, cwd, etc.).
PAYLOAD=$(cat)
SESSION_ID=""
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
fi

NS=$(date +%s%N)

# Atomic write: tmp file then mv. A consumer using fswatch will see the rename.
TARGET="$DIR/$ORCH_PANE_ID.event"
TMP="$TARGET.$$.tmp"
{
    printf 'ts_ns=%s\n' "$NS"
    printf 'ts_iso=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)"
    printf 'pane_id=%s\n' "$ORCH_PANE_ID"
    printf 'session_id=%s\n' "$SESSION_ID"
    printf 'cwd=%s\n' "$PWD"
} > "$TMP"
mv -f "$TMP" "$TARGET"

# Append-only event log so a parent that was offline can replay history.
LOG="${ORCH_EVENT_LOG:-$DIR/events.log}"
if command -v jq >/dev/null 2>&1; then
    jq -nc \
        --argjson ts_ns "$NS" \
        --arg event "Stop" \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$PWD" \
        '{ts_ns:$ts_ns, event:$event, pane_id:$pane_id, session_id:$session_id, cwd:$cwd}' >> "$LOG"
fi

# Lazy registry update — pane meta refreshed on every Stop.
REG_DIR="${ORCH_REGISTRY_DIR:-$HOME/.cache/orch-registry}"
mkdir -p "$REG_DIR"
REG_FILE="$REG_DIR/$ORCH_PANE_ID.json"
if command -v jq >/dev/null 2>&1; then
    jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg cwd "$PWD" \
        --arg session_id "$SESSION_ID" \
        --argjson last_stop_ts_ns "$NS" \
        '{pane_id:$pane_id, cwd:$cwd, session_id:$session_id, last_stop_ts_ns:$last_stop_ts_ns}' \
        > "$REG_FILE.$$.tmp" && mv -f "$REG_FILE.$$.tmp" "$REG_FILE"
fi

exit 0
