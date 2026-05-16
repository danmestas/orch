#!/usr/bin/env bash
# codex SessionStart hook companion — tails the current codex session's JSONL
# rollout transcript and publishes each line to NATS. Mirrors the claude-side
# hook at hooks/orch-nats-publish-jsonl.sh.
#
# Subject: orch.events.<pane_num>     (pane id "%37" becomes "37")
# Body:    one JSONL line verbatim    (each codex transcript entry)
#
# Codex writes rollouts at:
#   ~/.codex/sessions/<YYYY>/<MM>/<DD>/rollout-<timestamp>-<session_uuid>.jsonl
#
# The session_id arrives via the hook's stdin payload, but the full rollout
# path is not exposed. We resolve it by globbing for the session_id suffix
# under ~/.codex/sessions/ (cheap — codex paths are date-bucketed).
#
# Best-effort; no-ops if:
#   - ORCH_PANE_ID unset
#   - nats CLI or jq missing
#   - rollout file doesn't appear within a short window
#
# Self-managing: tailer is backgrounded + disowned. PID-gated to prevent
# double-spawn when SessionStart fires twice (e.g. /resume).
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0
command -v jq   >/dev/null 2>&1 || exit 0

PAYLOAD=$(cat)
SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
CWD=$(printf '%s' "$PAYLOAD" | jq -r '.cwd // ""' 2>/dev/null || true)
[ -n "$SESSION_ID" ] || exit 0
[ -n "$CWD" ] || CWD="$PWD"

PANE_NUM="${ORCH_PANE_ID#%}"
SUBJECT_PREFIX="${ORCH_NATS_SUBJECT_PREFIX:-orch}"
SUBJECT="${SUBJECT_PREFIX}.events.${PANE_NUM}"

CODEX_SESSIONS_DIR="${CODEX_SESSIONS_DIR:-$HOME/.codex/sessions}"

# Pid gate: one tailer per (pane, session).
GATE_DIR="${ORCH_NATS_GATE_DIR:-$HOME/.cache/orch-nats-tailers}"
mkdir -p "$GATE_DIR"
GATE="$GATE_DIR/${PANE_NUM}-${SESSION_ID}.pid"
if [ -f "$GATE" ]; then
    existing=$(cat "$GATE" 2>/dev/null || true)
    if [ -n "$existing" ] && kill -0 "$existing" 2>/dev/null; then
        exit 0
    fi
fi

(
    # Codex writes the rollout file shortly after SessionStart fires.
    # Wait up to 10s for it to appear; glob by session_id suffix.
    deadline=$(( $(date +%s) + 10 ))
    JSONL=""
    while [ -z "$JSONL" ]; do
        # shellcheck disable=SC2086  # glob expansion intentional
        for candidate in "$CODEX_SESSIONS_DIR"/*/*/*/rollout-*-"${SESSION_ID}.jsonl"; do
            [ -f "$candidate" ] && { JSONL="$candidate"; break; }
        done
        [ -n "$JSONL" ] && break
        [ "$(date +%s)" -gt "$deadline" ] && exit 0
        sleep 0.5
    done

    start_body=$(jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$CWD" \
        --arg jsonl "$JSONL" \
        --arg harness "codex" \
        --argjson ts_ns "$(date +%s%N)" \
        '{event:"jsonl_tailer_start", harness:$harness, pane_id:$pane_id, session_id:$session_id, cwd:$cwd, jsonl:$jsonl, ts_ns:$ts_ns}')
    nats pub --timeout=1s "$SUBJECT" "$start_body" >/dev/null 2>&1 || true

    tail -F -n0 "$JSONL" 2>/dev/null | while IFS= read -r line; do
        [ -n "$line" ] || continue
        nats pub --timeout=1s "$SUBJECT" "$line" >/dev/null 2>&1 || true
    done
) </dev/null >/dev/null 2>&1 &

TAIL_PID=$!
disown "$TAIL_PID" 2>/dev/null || true
echo "$TAIL_PID" > "$GATE"

exit 0
