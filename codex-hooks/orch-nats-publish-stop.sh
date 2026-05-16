#!/usr/bin/env bash
# codex Stop hook companion — publishes a NATS message when codex finishes a
# turn, mirroring the claude-code-side hook at hooks/orch-nats-publish-stop.sh.
#
# Subject: orch.stop.<pane_num>     (pane id "%37" becomes "37")
# Body:    JSON  {event, pane_id, session_id, cwd, ts_ns, ts_iso}
#
# Codex's hook system delivers the event payload as JSON on stdin (same shape
# convention as claude-code), so the body construction is identical. Wire this
# into ~/.codex/hooks.json under the Stop event entry.
#
# Best-effort. Silently no-ops if:
#   - ORCH_PANE_ID isn't set (codex session not spawned by orch)
#   - nats CLI is missing
#   - NATS server is unreachable (--timeout=1s caps the publish wait)
#
# Safe to install globally — the ORCH_PANE_ID gate keeps it dormant in
# non-orch codex sessions.
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0

# NATS subject tokens can't contain `%`; strip the prefix to get the numeric id.
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
        --arg harness "codex" \
        '{event:"stop", harness:$harness, pane_id:$pane_id, session_id:$session_id, cwd:$cwd, ts_ns:$ts_ns, ts_iso:$ts_iso}')
else
    BODY=$(printf '{"event":"stop","harness":"codex","pane_id":"%s","session_id":"%s","cwd":"%s","ts_ns":%s,"ts_iso":"%s"}' \
        "$ORCH_PANE_ID" "$SESSION_ID" "$PWD" "$NS" "$ISO")
fi

nats pub --timeout=1s "$SUBJECT" "$BODY" >/dev/null 2>&1 || true

exit 0
