# Extensions — host-side companions

Extensions are a third plane of orch, alongside executors and adapters:

| Plane                                                       | Subject of the abstraction         | Wire role                |
| ----------------------------------------------------------- | ---------------------------------- | ------------------------ |
| `executors/<type>/`                                         | How a pane is launched              | Off-wire                 |
| `cmd/orch-agent-shim/` + `internal/adapter/<harness>/`      | How a harness emits SAP chunks     | SAP producer             |
| `extensions/<host-ui>/`                                     | How orch state shows up in a host UI | SAP consumer (read-only) |

An extension is a daemon (or hook, in future) launched by `orch up` and
stopped by `orch down`. It subscribes to the Synadia Agent Protocol bus and
projects the resulting state into a host environment's UI — Claude Code's
subagent panel, a tmux status line, an editor sidebar.

Extensions MUST be **harness-agnostic**: they read only the SAP wire and treat
every orch pane the same regardless of which CLI is running inside it.

See [`extensions/README.md`](../extensions/README.md) for the extension
contract (manifest schema, `orch up` / `orch down` behaviour, how to write a
new one).

## Current extensions

| Directory                                 | What it does                                                                 |
| ----------------------------------------- | ---------------------------------------------------------------------------- |
| `extensions/claudecode-subagent-panel/`   | Mirrors every orch-spawned pane into Claude Code's subagent panel by writing synthetic JSONLs under `~/.claude/projects/<cwd-enc>/<session-uuid>/subagents/`. |

## Why a third plane?

Executors and adapters are about getting an agent onto the wire. Extensions
are about getting the operator's attention back. They live in a different
direction (host-side, off-wire from orch's core protocols) and have different
constraints (must tolerate host-app being absent; must not pretend to be a
first-class part of the host's data model).

Bundling extensions into `orch up` rather than shipping them as separate
packages keeps the install footprint to a single npm package, which is the
right default for solo operators. When an operator wants to disable one, they
delete the directory — no settings flag, no env var, just absence.
