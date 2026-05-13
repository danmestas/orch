#!/usr/bin/env bash
# orch-goal-session-context.sh — SessionStart hook for orch's goal-harness.
#
# Fires on every SessionStart event (initial connection, /resume).
# When a goal pursuit is active (SESH_GOAL_ID set), injects the current
# goal record into the session's initial context via additionalContext
# (returned as JSON on stdout per Claude Code's SessionStart hook contract).
#
# This is the mechanism by which long-horizon goals survive context
# loss — the goal record persists in sesh; the harness re-reads it on
# every cold start.
#
# Env required:
#   SESH_GOAL_ID         — active goal record id
# Env optional:
#   SESH_GOAL_SCOPE      — defaults to project
#   SESH_GOAL_SCOPE_ID   — defaults to current cwd basename
#   SESH_OPS_BIN         — sesh-ops binary (default "sesh-ops")

set -u

# No active goal → emit empty additionalContext (effectively no-op).
if [[ -z "${SESH_GOAL_ID:-}" ]]; then
  exit 0
fi

SESH_OPS_BIN="${SESH_OPS_BIN:-sesh-ops}"
SCOPE="${SESH_GOAL_SCOPE:-project}"
SCOPE_ID="${SESH_GOAL_SCOPE_ID:-$(basename "$PWD" | tr .- _)}"

if ! command -v "$SESH_OPS_BIN" >/dev/null 2>&1; then
  exit 0
fi

# Read the goal record. On failure, exit silently — don't poison startup.
GOAL_JSON="$("$SESH_OPS_BIN" --scope "$SCOPE" --scope-id "$SCOPE_ID" \
              goal get "$SESH_GOAL_ID" 2>/dev/null)" || exit 0

if [[ -z "$GOAL_JSON" ]]; then
  exit 0
fi

# Compose the context message. Pull a few fields for the human-readable
# summary; include the full JSON for the model to inspect.
SUMMARY="$(echo "$GOAL_JSON" | jq -r '
  "Active goal pursuit:\n" +
  "  id:        \(.id)\n" +
  "  objective: \(.objective)\n" +
  "  status:    \(.status)\n" +
  "  budget:    \(.used_tokens)/\(.token_budget // "∞") tokens\n" +
  "  tasks:     \((.tasks // []) | length) linked\n" +
  "\nUse orch-goal-status for the full state; invoke the goal-complete\n" +
  "skill ONLY after performing the completion audit per its instructions."
' 2>/dev/null)"

if [[ -z "$SUMMARY" ]]; then
  exit 0
fi

# Emit as Claude Code SessionStart hook JSON: { "hookSpecificOutput": { "hookEventName": "SessionStart", "additionalContext": "..." } }
jq -nc \
  --arg ctx "$SUMMARY" \
  '{hookSpecificOutput: {hookEventName: "SessionStart", additionalContext: $ctx}}'
