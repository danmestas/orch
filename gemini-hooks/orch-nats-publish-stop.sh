#!/usr/bin/env bash
# gemini Stop hook companion — publishes a NATS message when gemini finishes a
# turn, mirroring the claude-side and codex-side hooks.
#
# Subject: orch.stop.<pane_num>     (pane id "%37" becomes "37")
# Body:    JSON  {event, harness, pane_id, session_id, cwd, ts_ns, ts_iso}
#
# Wire this into ~/.gemini/settings.json under hooks.AfterAgent[].hooks[] —
# gemini-cli's turn-end event is "AfterAgent", NOT "Stop" (claude's name).
# Using "Stop" in gemini settings.json is silently rejected with a console
# warning: `⚠ Invalid hook event name: "Stop" from project config. Skipping.`
# Verified against gemini-cli v0.42.0 HookEventName enum + `gemini hooks
# migrate` mapping (Stop → AfterAgent).
#
# Companion Notification publisher (orch-nats-publish-notify.sh) wires under
# the same-named "Notification" event. SessionStart-JSONL is deferred —
# gemini's transcript path encoding varies by project context.
#
# Payload shape on stdin is inferred to match claude-code's convention (JSON
# with at least `session_id`). If gemini ships a divergent shape, this hook
# silently falls back to publishing without session_id rather than erroring.
#
# Best-effort. Silently no-ops if:
#   - ORCH_PANE_ID isn't set (gemini session not spawned by orch)
#   - nats CLI is missing
#   - NATS server is unreachable (--timeout=1s caps the publish wait)
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0

PANE_NUM="${ORCH_PANE_ID#%}"
SUBJECT_PREFIX="${ORCH_NATS_SUBJECT_PREFIX:-orch}"
SUBJECT="${SUBJECT_PREFIX}.stop.${PANE_NUM}"

PAYLOAD=$(cat)
SESSION_ID=""
if command -v jq >/dev/null 2>&1; then
    SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
fi

NS=$(date +%s%N)
ISO=$(date -u +%Y-%m-%dT%H:%M:%S.%6NZ)

if command -v jq >/dev/null 2>&1; then
    BODY=$(jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$PWD" \
        --argjson ts_ns "$NS" \
        --arg ts_iso "$ISO" \
        --arg harness "gemini" \
        '{event:"stop", harness:$harness, pane_id:$pane_id, session_id:$session_id, cwd:$cwd, ts_ns:$ts_ns, ts_iso:$ts_iso}')
else
    BODY=$(printf '{"event":"stop","harness":"gemini","pane_id":"%s","session_id":"%s","cwd":"%s","ts_ns":%s,"ts_iso":"%s"}' \
        "$ORCH_PANE_ID" "$SESSION_ID" "$PWD" "$NS" "$ISO")
fi

nats pub --timeout=1s "$SUBJECT" "$BODY" >/dev/null 2>&1 || true

exit 0
