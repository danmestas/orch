# claudecode-subagent-panel

Surfaces every orch-spawned pane (claude / codex / pi / gemini / echo) in
Claude Code's **subagent panel**, so you can browse what each pane is doing
from the same UI you use to drive Claude.

## How it works

`orch-cc-subagent-bridge` is a sidecar daemon launched by `orch up`. It:

1. Connects to the orch NATS bus (`$NATS_URL`).
2. Subscribes to `$SRV.INFO.agents` (discovery — every 5s).
3. Subscribes to `agents.>` (chunk traffic).
4. Detects the operator's active Claude Code session under
   `~/.claude/projects/<cwd-enc>/<session-uuid>/` by scanning for the
   most-recently-modified `.jsonl` (re-scans every 30s so opening a new CC
   window just works).
5. For each discovered orch agent, writes a synthetic
   `subagents/agent-<pane-enc>.jsonl` next to the operator's real CC
   transcript. CC's renderer picks the file up and lists the agent in the
   panel.
6. As Synadia §6 chunks arrive on the bus, the bridge appends `assistant`-type
   JSONL lines so the panel reflects each pane's output stream.

The bridge is **harness-agnostic** — it reads only the SAP wire. New harness
types surface automatically as long as their `orch-agent-shim` registers under
`metadata.harness`.

## Limits

- **No click-to-switch into the source pane.** CC's panel click handler isn't
  pluggable; the entries are read-only browseable.
- **Synthetic JSONLs.** These files are not real CC transcripts. They're
  scratch surfaces the bridge owns; do not edit by hand.
- **§7 query chunks are surfaced as `[query: ...]` text only** — the bridge
  doesn't act on them.
- **No history backfill.** On bridge restart, only chunks arriving from that
  point forward are mirrored. Past chunks stay in whatever JSONL was there.
- **CC session detection is best-effort.** If you have multiple CC windows
  open the bridge picks whichever wrote a `.jsonl` most recently and re-binds
  on every 30s tick.

## Disabling

Delete this directory. `orch up` stops launching the bridge; `orch down`
sweeps any leftover synthetic files matching
`~/.claude/projects/*/*/subagents/agent-pct*.jsonl`.

You can also override at runtime:

```sh
ORCH_BRIDGE_KEEP_FILES=1 orch up   # daemon won't sweep on SIGTERM
```

## Debugging

```sh
tail -f ~/.orch/extensions/claudecode-subagent-panel/daemon.log
```

The log surfaces: NATS connection state, every CC-session rebind, agent
discovery / seeding, and translation errors.

To force a re-detect without restarting CC: touch any `.jsonl` under your
target CC session dir; the bridge picks it up on the next 30s tick.

## Environment

| Variable                          | Default                                  | Purpose                                                              |
| --------------------------------- | ---------------------------------------- | -------------------------------------------------------------------- |
| `NATS_URL`                        | `nats://127.0.0.1:4222`                  | Orch hub URL.                                                        |
| `ORCH_BRIDGE_CC_PROJECTS_DIR`     | `~/.claude/projects`                     | Where to scan for active CC sessions.                                |
| `ORCH_BRIDGE_KEEP_FILES`          | unset (sweep on exit)                    | Set to `1` to preserve synthetic JSONLs on SIGTERM.                  |

## Smoke test

Three panes, mixed harnesses:

```sh
orch up
orch-spawn claude --pane new
orch-spawn codex  --pane new
orch-spawn pi     --pane new
```

Open Claude Code in the same workspace, look at the subagent panel — three
entries should appear, each labelled with its harness type. Send each one a
prompt via `orch-tell <alias> "<text>"`; the corresponding JSONL grows as
chunks arrive.

## Architecture

```
extensions/claudecode-subagent-panel/
├── manifest.json
├── README.md
├── cmd/orch-cc-subagent-bridge/main.go   # daemon entry
└── internal/
    ├── ccsession/      # find active CC session dir, decode cwd-enc
    ├── translator/     # SAP chunks → CC JSONL line bytes (deep module)
    └── writer/         # O_APPEND + fsync; per-agent file cache; sweep
```
