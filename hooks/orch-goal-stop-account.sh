#!/usr/bin/env bash
# orch-goal-stop-account.sh — DEPRECATED Stop hook for orch's goal-harness.
#
# DEPRECATED as of orch#64 (2026-05). Superseded by
# orch-goal-stop-account-daemon, a long-running Go binary that subscribes
# to Synadia §6.5 terminator events on agents.prompt.*.*.*.> and fires
# sesh-ops goal account on every turn-end across ALL harnesses
# (claude-code, codex, pi, gemini) — not just Claude Code.
#
# Migration:
#   1. Remove the orch-goal-stop-account.sh entry from your settings.json
#      Stop hooks (see settings-snippet.json for the current reference).
#   2. Re-run `orch-goal-pursue` — it now launches the daemon automatically.
#   3. Verify with `orch-goal-status` that the daemon is running.
#
# This file is kept for one deprecation cycle to avoid breaking existing
# installs that reference it from settings.json. It will be removed in the
# next minor release.
#
# Original purpose:
# Fired on every Claude Code Stop event (assistant turn end). When a goal
# pursuit is active (SESH_GOAL_ID set), reports an estimated token cost
# for the turn to sesh-ops, which CAS-increments the goal's used_tokens
# counter and auto-transitions to budget_limited if over budget.
#
# Limitation: fires only on Claude Code's Stop event; does not work with
# codex, pi, or gemini harnesses. Use the daemon instead.
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
