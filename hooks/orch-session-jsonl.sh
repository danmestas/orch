#!/usr/bin/env bash
# SessionStart hook — records the mapping between this claude session's
# tmux pane id and the JSONL transcript path it's writing.
#
# Claude Code passes hook context via stdin as JSON; SessionStart payloads
# include `session_id` and `transcript_path`. The transcript filename is
# session-uuid based, and Claude Code does not expose its path as an env
# var inside the running session, so external tooling (spies, audits,
# transcript monitors) otherwise has to guess the right .jsonl by
# "most-recently-modified in the cwd's project dir" — racy whenever
# multiple sessions share a cwd. This hook closes that gap by writing a
# deterministic per-pane mapping that `orch-current-jsonl` reads back.
#
# Mapping file: $ORCH_SESSIONS_DIR/<pane-id>.json (default ~/.orch/sessions/).
# Key precedence: $ORCH_PANE_ID (set by orch-spawn) > $TMUX_PANE.
# Atomic write: tmp + rename so a concurrent reader never sees a half-written
# file.
#
# No-op when jq is missing, when stdin has no transcript_path, or when no
# pane id can be derived — silently, since this hook is safe to install
# globally and runs in every claude session.
set -u

command -v jq >/dev/null 2>&1 || exit 0

PAYLOAD=$(cat)
TRANSCRIPT_PATH=$(printf '%s' "$PAYLOAD" | jq -r '.transcript_path // empty' 2>/dev/null || true)
SESSION_ID=$(printf '%s' "$PAYLOAD" | jq -r '.session_id // empty' 2>/dev/null || true)
[ -n "$TRANSCRIPT_PATH" ] || exit 0

PANE_ID="${ORCH_PANE_ID:-${TMUX_PANE:-}}"
[ -n "$PANE_ID" ] || exit 0

SESSIONS_DIR="${ORCH_SESSIONS_DIR:-$HOME/.orch/sessions}"
mkdir -p "$SESSIONS_DIR" 2>/dev/null || exit 0

TARGET="$SESSIONS_DIR/$PANE_ID.json"
TMP="$TARGET.$$.tmp"
jq -nc \
    --arg pane_id "$PANE_ID" \
    --arg transcript_path "$TRANSCRIPT_PATH" \
    --arg session_id "$SESSION_ID" \
    --argjson started_at "$(date +%s)" \
    '{pane_id:$pane_id, transcript_path:$transcript_path, session_id:$session_id, started_at:$started_at}' \
    > "$TMP" 2>/dev/null && mv -f "$TMP" "$TARGET"

exit 0
