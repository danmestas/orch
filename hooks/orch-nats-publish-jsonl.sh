#!/usr/bin/env bash
# SessionStart hook companion — tails the current Claude session's JSONL
# transcript and publishes each line to NATS. Gives an orchestrator live
# visibility into a harness child's tool calls + messages, not just terminal
# Stop/Notification events.
#
# Subject: orch.events.<pane_num>     (pane id "%37" becomes "37")
# Body:    one JSONL line verbatim       (each Claude transcript entry)
#
# Best-effort; silently no-ops if NATS unreachable, jq missing, or the
# transcript file doesn't materialize within a short window.
#
# Self-managing: the tailer is backgrounded + disowned. It exits when its
# parent process group dies (pane closed) OR when the JSONL file is gone
# for >30s (session moved/renamed). One tailer per session — re-running the
# hook with the same session_id is a no-op via a PID file gate.
[ -n "${ORCH_PANE_ID:-}" ] || exit 0
command -v nats >/dev/null 2>&1 || exit 0
command -v jq   >/dev/null 2>&1 || exit 0

PAYLOAD=$(cat)
SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // ""' 2>/dev/null || true)
CWD=$(printf '%s' "$PAYLOAD" | jq -r '.cwd // ""' 2>/dev/null || true)
[ -n "$SESSION_ID" ] || exit 0
[ -n "$CWD" ] || CWD="$PWD"

# Claude Code's JSONL layout: $HOME/.claude/projects/<cwd-encoded>/<session_id>.jsonl
# The cwd encoding replaces both `/` AND `.` with `-`, so `/Users/dan/.claude` →
# `-Users-dan--claude` (the `.` in `.claude` produces a literal `--` between
# segments). Earlier versions of this hook only handled `/` and missed `.claude`
# subpaths; that's why tailer spawn appeared to succeed but the file was never
# found.
ENCODED="${CWD//\//-}"
ENCODED="${ENCODED//./-}"
JSONL="$HOME/.claude/projects/${ENCODED}/${SESSION_ID}.jsonl"

PANE_NUM="${ORCH_PANE_ID#%}"
SUBJECT_PREFIX="${ORCH_NATS_SUBJECT_PREFIX:-orch}"
SUBJECT="${SUBJECT_PREFIX}.events.${PANE_NUM}"

# Pid gate: one tailer per (pane, session). Prevents double-spawn if SessionStart
# fires twice (e.g., user runs /resume mid-session).
GATE_DIR="${ORCH_NATS_GATE_DIR:-$HOME/.cache/orch-nats-tailers}"
mkdir -p "$GATE_DIR"
GATE="$GATE_DIR/${PANE_NUM}-${SESSION_ID}.pid"
if [ -f "$GATE" ]; then
    existing=$(cat "$GATE" 2>/dev/null || true)
    if [ -n "$existing" ] && kill -0 "$existing" 2>/dev/null; then
        exit 0  # already tailing
    fi
fi

# Background tailer. Wait up to 10s for the JSONL to appear (CC writes it
# shortly after SessionStart fires), then tail -F and publish each line.
# Exit when the file is gone for 30s consecutive checks (session ended) OR
# when this script's parent process group dies.
(
    deadline=$(( $(date +%s) + 10 ))
    while [ ! -f "$JSONL" ]; do
        [ "$(date +%s)" -gt "$deadline" ] && exit 0
        sleep 0.5
    done

    # Publish a session-start marker so subscribers know we're alive.
    start_body=$(jq -nc \
        --arg pane_id "$ORCH_PANE_ID" \
        --arg session_id "$SESSION_ID" \
        --arg cwd "$CWD" \
        --arg jsonl "$JSONL" \
        --argjson ts_ns "$(date +%s%N)" \
        '{event:"jsonl_tailer_start", pane_id:$pane_id, session_id:$session_id, cwd:$cwd, jsonl:$jsonl, ts_ns:$ts_ns}')
    nats pub --timeout=1s "${SUBJECT_PREFIX}.events.${PANE_NUM}" "$start_body" >/dev/null 2>&1 || true

    # tail -F handles renames + truncations; --line-buffered keeps output flowing.
    tail -F -n0 "$JSONL" 2>/dev/null | while IFS= read -r line; do
        [ -n "$line" ] || continue
        nats pub --timeout=1s "$SUBJECT" "$line" >/dev/null 2>&1 || true
    done
) </dev/null >/dev/null 2>&1 &

TAIL_PID=$!
disown "$TAIL_PID" 2>/dev/null || true
echo "$TAIL_PID" > "$GATE"

exit 0
