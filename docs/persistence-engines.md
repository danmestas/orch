# Persistence engines — choosing tmux vs cmux vs zmx

orch's `--persistence` flag picks which session-persistence backend
keeps a worker's PTY alive across operator disconnects. Three engines
ship today; this doc helps operators decide which to invoke.

The full design rationale lives in
[Proposal 0008](proposals/0008-pluggable-persistence-layout.md); this
doc is the practical "which one?" cheatsheet.

## At-a-glance

| Engine | Composition pair | Layout shape | Headless | Verify | Multi-pane | Native wait |
|---|---|---|---|---|---|---|
| tmux | `--persistence tmux --layout tmux` (default) | splits + windows | yes | yes (tmuxctl) | yes | poll-based |
| cmux | `--persistence cmux --layout cmux` | cmux surfaces | no | not yet | yes | not implemented |
| zmx | `--persistence zmx --layout none` | none (1 session = 1 PTY) | yes (no-op) | yes (history poll) | no | poll-based |

(Defaults are `--persistence tmux --layout tmux`. Operators only opt
into cmux or zmx by passing both flags.)

## When to pick which

### tmux — the default, the catch-all

Pick tmux when:

- You're working in a tmux session already (the operator's terminal
  is `$TMUX`-attached).
- You want multiple workers visible in splits / panes / windows
  around the operator pane.
- You need `--verify` (readiness polling before orch declares the
  spawn successful) — tmux is the only engine with a fully-wired
  verify path today.
- You need `--headless` — workers attach to the `orch-headless`
  session (configurable via `ORCH_HEADLESS_SESSION`) and are
  promoted-on-demand via `orch show`.
- You need to send keystrokes to the worker (`orch tell`, `orch
  interrupt`, etc. — the bash drivers all key off tmux pane ids
  today). zmx workers are not yet reachable from those drivers; cmux
  workers route through cmux's own send vocabulary.

### cmux — when tmux's window model doesn't fit

Pick cmux when:

- You're running inside cmux already (operator's terminal is a cmux
  pane) and want orch to spawn siblings via `cmux new-pane` instead
  of jumping out into a tmux session.
- You want cmux's grid / split UX instead of tmux's pane vocabulary.

Caveats:

- `--headless` is rejected — cmux has no headless-session concept.
- `--verify` is deferred — orch surfaces a clear error rather than
  silently skip. Operators who want verify on cmux should hand-poll
  via `cmux capture-pane`.
- Sending keystrokes to a cmux worker requires `cmux send --surface
  surface:N -- "string"`; the bash `orch tell` driver doesn't speak
  cmux yet (Phase 3 of the broader pluggability work).

### zmx — when you want sessions-only persistence

Pick zmx when:

- You want the worker's PTY persistence without orch dictating the
  display layer at all. zmx is sessions-only (1 session = 1 PTY-
  backed process, no panes / splits / tabs by design).
- You manage your own emulator window for the operator-facing view
  (Ghostty, Alacritty, kitty, iTerm) and `zmx attach <name>` from
  inside it on demand.
- You want to wrap zmx inside your own tmux (or another multiplexer)
  for layout, while still getting orch's slug-to-session indirection
  and shim wiring.
- You want a cheaper `--verify` than tmux's: `zmx history` reads
  scrollback directly (a file read on the zmx server side) versus
  tmux's capture-pane + display-p indirection through the tmux
  server.

Caveats:

- `--layout` must be `none` (no other pair is supported — zmx has no
  in-session layout vocabulary). `--position` (right/left/above/
  below) is rejected explicitly with a category-error message.
- `--headless` is accepted but is a no-op shape difference: zmx is
  detached-by-design from orch's perspective (the spawning process
  never attaches to the session, regardless of `--headless`). The
  flag stays accepted for parity with the tmux engine, but no
  behavior changes whether you pass it or not.
- Sending keystrokes to a zmx worker requires `zmx send <name> --
  <string>`; the bash `orch tell` driver doesn't speak zmx yet
  (Phase 3 of the broader pluggability work). Operators can shell
  into zmx send directly until then.
- `zmx wait <name>` blocks on a `run`-launched task finishing, not
  on session death — orch's `Handle.Wait` polls `zmx list --short`
  for session disappearance instead.
- Session names are operator-supplied via `--slug` (or
  `--instance-id`). orch passes the slug verbatim as the zmx
  session name. Collisions are detected pre-spawn (engine errors
  with a "kill it via `zmx kill <name> --force` or pick a different
  --slug" message).

## Composition table

The (persistence, layout) registry is closed — combinations not in
the table are rejected at flag-parse time, before any pane / surface
/ session work. See
[Proposal 0008's composition table](proposals/0008-pluggable-persistence-layout.md#closed-registry-composition)
for the full matrix; the supported subset is:

| Persistence | Layout | When |
|---|---|---|
| `tmux` | `tmux` | default; today's behavior |
| `cmux` | `cmux` | Phase B (issue #207) |
| `zmx` | `none` | Phase C (zmx Phase 2) |

Cross-engine pairs (`{tmux, cmux}`, `{zmx, tmux}`, `{cmux, none}`,
etc.) are rejected with `ErrUnsupportedComposition` and a list of the
supported pairs. Mixed pairs require explicit forwarder code; no
forwarder exists yet, so the rejection is the contract.

## Installation

- tmux: ship with macOS / most Linux distros; `brew install tmux` if
  missing.
- cmux: `https://cmux.app` (see the engine's stderr message for the
  current install path).
- zmx: `brew install junegunn/zmx/zmx` or build from
  `https://github.com/junegunn/zmx`.

orch detects each engine's binary via `exec.LookPath`; missing
binaries surface as operator-facing errors at spawn time
(`zmx not on PATH — install zmx...`).

## Limitations to be aware of

Phase 3 (not yet shipped) will:

- Replace the bash `orch tell` / `orch interrupt` drivers with a Go
  binary that dispatches by engine (so zmx + cmux workers become
  fully drivable from operator commands, not just tmux ones).
- Rename the shim's `--pane <locator>` to `--watch-handle
  <type>:<id>` so the locator type is explicit on the wire.

Until then, sending keystrokes to non-tmux workers requires shelling
out to that engine's native send verb directly.
