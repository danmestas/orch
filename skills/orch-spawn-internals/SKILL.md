---
name: orch-spawn-internals
description: Use when debugging orch-spawn behavior — workers spawning but not registering, shims surviving pane kills, env vars not propagating, or --verify failing for codex/pi. Triggers on "orch-spawn returned X but Y didn't happen", "shim isn't registering after spawn", "kill-pane didn't kill the shim", "NATS_URL not set in worker", "orch-spawn --verify fails for codex/pi", "spawn multiple workers", "what does orch-spawn actually do", "shim lifecycle vs pane lifecycle", "why are there leftover orch-agent-shim processes", "disowned shim hanging around". Documents the launch model, env propagation, adapter detection, and pane/shim decoupling — the parts of orch-spawn that surprise people debugging it from the outside. Pairs with `bench-docker-sesh-author` and `bench-debug-playbook`.
---

# orch-spawn-internals

`orch-spawn` is the gateway between operator intent ("give me a claude worker") and the running shim. Its internals have subtle behavior that's hard to debug without knowing the model. This skill explains the launch sequence, the pane/shim decoupling, env propagation, adapter detection — the things you need to know when something silently doesn't work.

This is reference material: read when something surprises you, not before.

## What orch-spawn does, top-down

```
orch-spawn <agent> [--cwd p] [--outfit O] [--cut C] [--accessory A]... [--headless] [--no-shim] [--no-fleet] [--project N] [--verify]
```

The sequence:

1. **Parse args.** Resolve agent name, project, outfit, cut, accessory list.
2. **`suit prepare`** if `--outfit` is set. Builds an isolated config bundle for that outfit (claude-only today).
3. **`tmux split-window`** (or `new-session -d -s orch-headless` if `--headless`). Creates the pane the agent will run in. With `--verify`, polls the pane's captured output for the agent's startup banner before proceeding.
4. **Run the agent CLI in the pane.** The pane's shell exec's `claude` / `codex` / `pi` / `gemini`.
5. **Launch the shim in the background.** `orch-agent-shim ... & disown`. The shim attaches to the pane by id, registers on the bus, and bridges I/O.
6. **Print the pane id on stdout.** Operator captures it as a worker handle.

## Shim launch — the key detail

From `bin/orch-spawn`:

```bash
ORCH_OWNER="$SHIM_OWNER" \
ORCH_OUTFIT="$OUTFIT" \
ORCH_ROLE="$ROLE" \
SESH_SESSION="$SHIM_SESSION" \
NATS_URL="$SHIM_NATS" \
orch-agent-shim \
    --agent "$SHIM_AGENT" \
    --pane "$PANE" \
    --cwd "$CWD" \
    >"$SHIM_LOG" 2>&1 &
disown
```

What matters:

- **Env vars are INLINED on the exec.** They survive `disown` in both bash and zsh because they're resolved in the parent's context before fork. Don't rely on environment inheritance through `tmux`; see "Env propagation gotcha" below.
- **Logs go to `~/.cache/orch-shim/<pct-pane>.log`.** Pane id `%5` becomes `pct5.log` (`%` isn't a legal filename char).
- **`disown` unbinds from job control.** Parent shell SIGHUP doesn't reach the shim.
- **Shim's parent PID becomes 1.** Init reparents it after disown. Easy to grep for: `ps -eo pid,ppid | awk '$2==1 && /orch-agent-shim/'`.

## Pane vs shim — separate processes

A common confusion. The pane (running the agent CLI) and the shim (bridging NATS) are TWO INDEPENDENT processes:

```
parent shell (orch-spawn)
  ├─ tmux split-window  →  pane process (mock-claude / claude / codex / pi / gemini)
  └─ orch-agent-shim    (backgrounded, disowned, reparented to init)
        watches the pane via pane id
        connects to NATS, registers `agents` micro service
```

`tmux kill-pane -t %5` kills the pane process only. The shim:

- Loses its watch target (pane is gone).
- BUT continues running — it's a separate process.
- Eventually fails when it tries to interact with the dead pane.
- Lives until: (a) the NATS connection dies permanently, (b) SIGTERM/SIGINT delivered directly, (c) the process exits on its own error path.

To kill a shim deliberately:

```bash
pkill -f "orch-agent-shim --agent X --pane %5"
```

Or capture the PID before disown (requires modifying orch-spawn).

This is why bench tests that don't `sesh_full_reset` between groups can see "leftover" shims from previous groups in `$SRV.INFO.agents`. They die when the sesh hub's NATS server dies (port released, no reconnect target).

## Env propagation gotcha

Naive assumption: `NATS_URL=foo tmux split-window 'mock-claude'` propagates `NATS_URL` into the new pane's environment.

Reality: `tmux split-window` inherits the **tmux server's** env block at server-start time, NOT the calling shell's. The caller's env is never transferred to a new pane.

`orch-spawn` handles this by inlining env vars on the **shim's** exec (above). For the **pane** itself (where the agent CLI runs), env-via-tmux doesn't work either — set vars via `tmux send-keys 'export X=...'` if needed, but that races with the agent's startup.

For bench tests: never assume env propagation through tmux. Pass via explicit flags or env-on-exec.

## Adapter detection

`orch-spawn` probes whether `orch-agent-shim` has a Go adapter for the target agent:

```bash
if orch-agent-shim --agent "$SHIM_AGENT" --help >/dev/null 2>&1; then
    ADAPTER_OK=1
else
    ADAPTER_EXIT=$?
    if [ "$ADAPTER_EXIT" -eq 2 ]; then
        ADAPTER_OK=0
    else
        ADAPTER_OK=1  # unexpected exit — try anyway
    fi
fi
```

**Caveat:** `flag.Parse()` in the Go binary intercepts `--help` and exits 0 BEFORE the agent check runs. So the probe returns 0 for any agent name — the exit-2 path is unreachable today. The probe is effectively a binary-exists check, not an adapter check.

The real adapter selection happens at shim startup. If the agent is unknown, the shim logs `no adapter for agent X` and exits 1. Look in the shim log to find this.

If you write similar probes elsewhere, remember: `flag.PrintDefaults()` always exits 0, regardless of other flag values.

## BANNER table + --verify

`orch-spawn --verify` waits for the agent's startup banner to appear in the pane's captured output before returning the pane id. This avoids races where the operator immediately sends a prompt and the agent hasn't started yet.

| harness | banner string | --verify works |
|---|---|---|
| claude | "Claude Code" | yes |
| gemini | "Gemini CLI" | yes |
| codex | (empty) | no |
| pi | (empty) | no |

Codex and pi rely on tmux title rename for liveness, which mock binaries can't easily produce without becoming Go binaries. The docker-sesh bench drops `--verify` for cross-harness uniformity and uses `sleep` + probe instead.

If you need verify-equivalent behavior for codex / pi, write a custom poll loop on `tmux capture-pane -p` looking for the agent's first prompt-ready marker.

## Multi-worker spawn timing

Spawning N workers sequentially works (`for h in claude codex pi gemini; do orch-spawn ...; done`), but each shim takes a few seconds to register. Sleep tuning, empirically derived from the docker-sesh bench:

| count | sleep before $SRV.INFO probe |
|---|---|
| 1 | 5s |
| 2 | 8s |
| 4 | 12s |

Probing too early returns fewer replies than expected. Bump the sleep, don't retry.

## Debugging spawn failures

Symptom → likely cause map:

| symptom | likely cause | check |
|---|---|---|
| orch-spawn output doesn't start with `%` | warning printed before pane id, or pane creation failed | `orch-spawn ... 2>&1 \| grep -E '^%[0-9]+'` |
| shim log shows "starting" but service never registers | wrong NATS_URL, or leaf vs hub confusion | `cat ~/.sesh/hub.nats.url` (the NATS client URL) vs the URL the shim was given — NOT `hub.url`, which is the leaf-node URL |
| shim log is empty / missing | shim crashed before logging, or adapter probe rejected | `ps -eo pid,command \| grep orch-agent-shim` |
| pane exists but no shim | `--no-shim` was passed, or adapter probe failed (exit 2 path) | `ls -la ~/.cache/orch-shim/<pct-pane>.log` |
| `$SRV.INFO.agents` shows stale workers | disowned shims from prior tests still alive | `pkill -f orch-agent-shim` or wait for sesh hub to die |

## Worked recipes

### Spawn a worker and verify it actually registered

```bash
PANE=$(NATS_URL="$NATS_URL" orch-spawn claude --cwd "$PWD" --headless 2>&1 | grep -E '^%[0-9]+' | tail -1)
sleep 5
N=$(nats --server="$NATS_URL" req '$SRV.INFO.agents' '' --replies=0 --timeout=3s 2>/dev/null \
    | grep -c '"name":"agents"')
echo "pane=$PANE registered_services=$N"
```

### Cleanly tear down all bench shims

```bash
pkill -f "^orch-agent-shim" || true
# Wait for the NATS server to release them too if needed:
pkill -f "^sesh up" || true
sleep 0.5
rm -rf "$HOME/.sesh"
```

### Set ORCH_OWNER and verify metadata picked it up

```bash
ORCH_OWNER=alice NATS_URL=... orch-spawn claude --cwd /tmp/x --headless
sleep 5
nats --server=$NATS_URL req '$SRV.INFO.agents' '' --replies=0 --timeout=3s 2>/dev/null \
    | jq -r 'select(.metadata.owner=="alice")'
```

## Cross-references

- `bench-docker-sesh-author` — for how the bench wraps orch-spawn into `spawn_worker_on_hub`.
- `bench-debug-playbook` — for diagnostic primitives when spawn-related failures don't surface cleanly.
- `orch-driver` — for the operator-facing skill around already-spawned panes.
