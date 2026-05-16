---
status: accepted
date: 2026-05-09
---

# Operator pane is recorded separately from the worker registry

The operator (the session you type into) is not a worker. It is started directly without `orch-spawn`, does not export `ORCH_PANE_ID`, does not write Stop markers, and does not auto-register. To let other harness tools (peek, spy, future audit skills) find the operator's pane and transcript JSONL without prompting, we added a single optional cache file at `~/.cache/orch-operator.json` written by `orch-claim-operator` and read by tools that need it. Operator stays out of `~/.cache/orch-registry/`; the claim record is the asymmetric bridge.

## Why a separate file rather than adding the operator to the registry

The env-var distinction (`ORCH_PANE_ID` set ⇒ worker) *is* the role assignment, deliberately. Adding an `--orchestrator` flag and unifying the registry would (a) make every binary that scans the registry filter by role, and (b) require a manual flag at operator-session startup that the env-var-presence check handles for free. Asymmetry-by-env-var is load-bearing; the claim record is the small additional file needed when tools want positive identification of the operator.

## Considered options

- **Add operator to worker registry with `role: "operator"`** (rejected): conflates two distinct lifecycle shapes and forces every registry consumer to filter; adds noise to peek's worker-survey by default.
- **Infer operator at query time via `tmux display -p '#{pane_id}'`** (rejected): only works when called from the operator's own pane; tools called from worker shells cannot identify the operator.
- **Single optional cache file** (accepted): one-time write, read by anyone, optional (tools degrade gracefully without it).

## Surface area

`orch-claim-operator` writes the record. `orch-peek` reads it to prepend an operator row to its survey. `orch-spy` reads it to resolve `target=operator` to a pane id and transcript path. The file format is small: `{pane_id, claimed_at_ts_ns, transcript_jsonl, cwd}`.

## Migration note (2026-05-16 — issue #60)

`orch-claim-operator` has been retired to a no-op stub. The operator-pane identity is now established by setting `ORCH_ROLE=operator` in the operator's shell before starting a session; `orch-agent-shim` then registers the pane with `metadata.role=="operator"` on `$SRV.INFO.agents`. Discovery consumers filter by `metadata.role == "operator"` — no separate claim file is needed. `~/.cache/orch-operator.json` is no longer written. The "separate file" option in "Considered options" above is superseded by this; the single-source-of-truth property of `$SRV.INFO.agents` removes the need for the asymmetric bridge.

**Affected consumers, all migrated to discovery:**

- `orch-spy` — `target=operator` finds the agent with `metadata.role=="operator"`; `target=%pane` matches `metadata.pane_id`. Transcript JSONL inferred from `metadata.cwd` via the existing `~/.claude/projects/<encoded-cwd>/*.jsonl` lookup. Error messages now point users at `ORCH_ROLE=operator` instead of `orch-claim-operator`.
- `orch-peek` — worker list and operator-row come from the same `$SRV.INFO.agents` snapshot. Required dep: `nats` CLI on PATH (the registry-file fallback is gone).
- `orch-tell` — worker→observer guard reads `metadata.role` via discovery; falls back to "worker" (unrestricted) when NATS/jq are absent or the target is not registered.
- ~~`orch-nats-bridge-in` `list_active_workers`~~ — retired entirely in #94 along with the rest of the legacy bridge daemon.
- ~~`hooks/orch-stop-marker.sh`~~ — retired in #94 along with the other marker emitters. Shim handles registration; the marker-watch loops in the adapters survive for the test suite, but no production writer exists.
