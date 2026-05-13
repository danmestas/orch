#!/usr/bin/env bash
# orch-goal-stop-account.sh — Stop hook for orch's goal-harness.
#
# Fires on every Stop event (assistant turn end). When a goal pursuit
# is active (SESH_GOAL_ID set), reports an estimated token cost for
# the turn to sesh-ops, which CAS-increments the goal's used_tokens
# counter and auto-transitions to budget_limited if over budget.
#
# Estimation is rough — Claude Code doesn't expose per-turn token
# counts to hooks directly. The reference impl uses a fixed per-turn
# estimate; real harnesses should read from the model SDK after each
# completion. The substrate is correct regardless of the estimate;
# this is purely the runtime-side measurement that the spec calls
# out as harness responsibility.
#
# Env required:
#   SESH_GOAL_ID         — active goal record id (set by orch-goal-pursue)
# Env optional:
#   SESH_GOAL_SCOPE      — defaults to project
#   SESH_GOAL_SCOPE_ID   — defaults to current cwd basename
#   ORCH_GOAL_TOKEN_ESTIMATE  — per-turn estimate (default 5000)
#   SESH_OPS_BIN         — sesh-ops binary (default "sesh-ops")
#
# Failure policy: never block the user's session. If sesh-ops isn't
# reachable or the goal record is missing, log to stderr and exit 0.
# Goal pursuit is best-effort observability; a broken hook must not
# break the user's workflow.

set -u

# No active goal → no-op.
if [[ -z "${SESH_GOAL_ID:-}" ]]; then
  exit 0
fi

SESH_OPS_BIN="${SESH_OPS_BIN:-sesh-ops}"
SCOPE="${SESH_GOAL_SCOPE:-project}"
SCOPE_ID="${SESH_GOAL_SCOPE_ID:-$(basename "$PWD" | tr .- _)}"
ESTIMATE="${ORCH_GOAL_TOKEN_ESTIMATE:-5000}"

# Verify sesh-ops is on PATH.
if ! command -v "$SESH_OPS_BIN" >/dev/null 2>&1; then
  echo "orch-goal: sesh-ops not on PATH; skipping account" >&2
  exit 0
fi

# Account the turn. Capture stderr for diagnostics; never block on failure.
if ! "$SESH_OPS_BIN" --scope "$SCOPE" --scope-id "$SCOPE_ID" \
       goal account "$SESH_GOAL_ID" "$ESTIMATE" >/dev/null 2>"/tmp/orch-goal-stop-${SESH_GOAL_ID}.err"; then
  echo "orch-goal: goal account failed for $SESH_GOAL_ID (see /tmp/orch-goal-stop-${SESH_GOAL_ID}.err)" >&2
fi

exit 0
