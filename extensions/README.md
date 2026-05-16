# Extensions

Extensions are **host-side companion daemons** that sit alongside an orch
install. They subscribe to the Synadia Agent Protocol bus and project that
state into something the host environment renders — a desktop app's panel,
a tmux status line, an editor's sidebar, etc.

Extensions are the third plane in orch:

| Plane                                                   | Lives on the SAP wire? | Knows about agent CLIs? |
| ------------------------------------------------------- | ---------------------- | ----------------------- |
| `executors/<type>/` — how a pane is launched            | No                     | No                      |
| `cmd/orch-agent-shim/internal/adapters/<harness>/` —    |                        |                         |
| per-harness turn-end and chunk emission                 | Yes (as producer)      | Yes (one per harness)   |
| `extensions/<host-ui>/` — host-side companions          | Yes (as consumer)      | No (harness-agnostic)   |

An extension is one directory under `extensions/`. The outer directory is named
for the **destination UI** being extended (`claudecode-subagent-panel`), never
for the source harness — because a single extension typically surfaces *all*
orch panes regardless of which harness is inside each one.

## Contract

Every extension MUST ship a `manifest.json` at the root of its directory:

```json
{
  "name": "claudecode-subagent-panel",
  "version": "0.1.0",
  "description": "Surface orch-spawned panes in Claude Code's subagent panel.",
  "lifecycle": "daemon",
  "binary": "cmd/orch-cc-subagent-bridge",
  "trigger": "orch-up",
  "env": ["NATS_URL", "ORCH_BRIDGE_CC_PROJECTS_DIR"]
}
```

Fields:

- **`name`** — extension id; matches the directory name. Used for pid/log paths
  under `~/.orch/extensions/<name>/`.
- **`version`** — SemVer; advisory only, no compatibility enforcement yet.
- **`description`** — single line, surfaced in `orch up` logs.
- **`lifecycle`** — currently only `"daemon"` is supported. Future values:
  `"one-shot"` for fire-and-forget triggers; `"hook"` for hooks installed into
  another tool's config.
- **`binary`** — path (relative to the extension dir) to the Go entry point or
  shell script that `orch up` should launch.
- **`trigger`** — when to start. Currently only `"orch-up"` is supported.
- **`env`** — environment variables the binary reads. Advisory: lets `orch up`
  show the operator what they could tune without reading the source.

## `orch up` behaviour

`orch up` enumerates `extensions/*/manifest.json` and, for every manifest with
`lifecycle: "daemon"` and `trigger: "orch-up"`, launches the binary as a
detached background process. PID goes to
`~/.orch/extensions/<name>/daemon.pid`; stdout/stderr go to
`~/.orch/extensions/<name>/daemon.log`.

`orch up` **does not** know about specific extensions. It reads only manifest
fields. New extensions land by adding a directory.

## `orch down` behaviour

`orch down` reads each pid file, sends SIGTERM, waits up to 5s, SIGKILL on
holdout, removes the pid file. Extension-specific cleanup (e.g. sweeping
synthetic files) is the extension binary's responsibility — receive SIGTERM,
clean up, exit 0.

## Writing a new extension

1. Copy an existing extension directory.
2. Edit `manifest.json`: pick a new `name`, point `binary` somewhere real.
3. Write your daemon. Subscribe to NATS using `$NATS_URL`; subscribe to
   `$SRV.INFO.agents` for discovery and `agents.>` for chunks. Render into your
   host UI however you like.
4. Add unit tests under `internal/<pkg>/` and (if your daemon has side effects
   on real systems) an integration test under `test/test-<your-ext>.sh` wired
   into `.github/workflows/ci.yml`.
5. Open a PR. Reviewers check the manifest, the harness-agnostic property
   (your extension MUST NOT special-case any specific agent CLI), and the
   cleanup behaviour on SIGTERM.

## Disabling

Delete the extension's directory. `orch up` will stop spawning the daemon;
`orch down` no longer has a pid file to kill but its synthetic-file sweep (if
the extension installs one) still runs.
