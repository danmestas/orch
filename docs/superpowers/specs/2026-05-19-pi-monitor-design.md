# Pi Monitor Design

**Date:** 2026-05-19
**Status:** Proposed

## Goal

Add a `Monitor` tool to Pi that matches Claude Code's in-session monitoring model closely enough that prompts, recipes, and operator expectations transfer without retraining.

## Scope

This spec covers the Pi-side `Monitor` programming model, lifecycle, event semantics, and operator-facing behavior.

Out of scope:
- durable cross-restart monitors
- desktop/audible paging behavior
- replacing one-shot background waits

## User-facing contract

`Monitor` exposes the same four-field contract:

- `command: string` — shell command to run
- `description: string` — specific label shown on every fired notification
- `persistent?: boolean` — default `false`
- `timeout_ms?: number` — default `300000`, max `3600000`

Constraint:
- when `persistent: true`, `timeout_ms` is ignored

`Monitor` returns a handle. Pi must also expose a `TaskStop`-equivalent that stops a monitor by handle. Callers must not need PID tracking.

## Lifecycle

### Non-persistent monitors

Default behavior:
- spawn the command as a child shell process in the same general environment model as Bash
- keep the watch active until the child exits or `timeout_ms` elapses
- if timeout elapses first, kill the child and emit a final timeout/stopped notification

### Persistent monitors

When `persistent: true`:
- no timeout applies
- the monitor survives turn boundaries within the same Pi session
- the monitor ends only when:
  - the child exits,
  - the session ends, or
  - the caller stops it through the returned handle

### Session durability

Pi should mirror Claude Code here:
- monitors are in-session only
- monitors do **not** survive Pi restart / parent session restart
- any future durable monitor support must be introduced as a Pi-specific extension, not hidden under the Claude-compatible contract

## Event model

Pi must treat monitor output as a transport-level stream, not a semantic event source.

### What counts as an event

- only the child process's `stdout` is event-bearing
- the event unit is a complete newline-delimited line
- line content is surfaced verbatim
- Pi does not interpret line meaning; an "event" is just emitted stdout

Common usage patterns to support:
1. tail + filter (`tail -f ... | grep --line-buffered ...`)
2. filesystem watch (`inotifywait -m ...`)
3. poll loop (`while true; do ...; sleep N; done`)

### Coalescing

Pi must implement Claude-compatible stdout batching:
- collect complete stdout lines into a 200ms coalescing window
- lines arriving within that window become one monitor notification / transcript interrupt

This is required for compatibility. Without it, multiline logical events produce too many notifications and feel materially different from Claude Code.

### Delivery semantics

Monitor notifications are:
- transcript interrupts, not tool replies
- allowed to arrive mid-turn or while waiting for user input
- annotated with the monitor `description` on every fire

`description` is mandatory-in-practice context. It should be documented as requiring concrete, source-specific wording (for example, `errors in deploy.log`, not `watching logs`).

## stdout vs stderr

Pi should mirror Claude Code's split deliberately:

- only `stdout` produces monitor events
- `stderr` never produces monitor events
- `stderr` is written to an output artifact/file readable later

This is surprising enough that the docs must call it out explicitly and show the standard fix when failures should participate in the watch:

```bash
python train.py 2>&1 | grep --line-buffered -E "loss=|Traceback|Error"
```

Also document the non-example clearly: `tail -f` only sees what was actually redirected into the tailed file.

## Final states

A monitor must end with a final status notification when any of these occur:
- child exited normally or non-zero
- timeout killed the child
- explicit stop by handle
- circuit breaker stopped the monitor for excessive output

The final notification must surface the termination reason and exit code when applicable.

## Output-volume circuit breaker

Pi must implement a monitor circuit breaker that auto-stops overly chatty monitors.

Rationale:
- a bad filter can flood the transcript
- recovering manually after a flood is costly
- Claude Code already protects itself this way

Requirements:
- detect excessive event volume after coalescing
- stop the monitor automatically
- emit a final notification that explains it was stopped for excess output and suggests restarting with a tighter filter

The threshold can remain an internal tuning constant rather than part of the public contract.

## Monitor vs one-shot waits

Pi should preserve the same product boundary as Claude Code.

Use `Monitor` for:
- one-per-occurrence indefinitely
- one-per-occurrence until a known end, if the command emits transitions and then exits

Do **not** use `Monitor` for:
- one-shot `tell me once when X is ready`

That case should remain a background Bash-style pattern where the command exits on condition, for example:

```bash
until grep -q "Ready" log; do sleep 0.5; done
```

Reason: using `Monitor` for one-shot waits holds an active slot until timeout and creates asymmetric cost/failure behavior compared with Claude Code.

## Operator guidance to document

Pi docs/examples must lean on the same edge cases Claude Code emphasizes.

### 1. Pipe buffering makes monitors look broken

Every stage in a pipeline may need line buffering:
- `grep --line-buffered`
- `awk -W interactive`
- `stdbuf -oL`
- `python -u`

Without this, output may arrive in large delayed bursts.

### 2. Silence is not success

Filters must include failure signatures, not just happy-path lines.

Example:

```bash
grep -E --line-buffered "elapsed_steps=|Traceback|Error|FAILED|Killed|OOM"
```

### 3. Don't stream raw logs

Every emitted line becomes transcript noise. Filters should be selective for lines the model would act on.

### 4. Poll loops should tolerate transient failures

For remote/rate-limited polling, recommend `|| true` around fragile calls and sane intervals:
- 30s+ for rate-limited APIs
- 0.5–1s for local checks

### 5. Known-end workflows should be bounded

Finite workflows like CI settling should use a poll loop that emits new state transitions and exits when all checks are terminal. `tail -f` is the wrong shape for this.

## Reference examples

### Each matching log line is an event

```bash
tail -f /var/log/app.log | grep --line-buffered "ERROR"
```

### Each file change is an event

```bash
inotifywait -m --format '%e %f' /watched/dir
```

### Poll GitHub for new PR comments

```bash
last=$(date -u +%Y-%m-%dT%H:%M:%SZ)
while true; do
  now=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  gh api "repos/owner/repo/issues/123/comments?since=$last" \
    --jq '.[] | "\(.user.login): \(.body)"'
  last=$now; sleep 30
done
```

### Emit CI check transitions until completion

```bash
prev=""
while true; do
  s=$(gh pr checks 123 --json name,bucket)
  cur=$(jq -r '.[] | select(.bucket!="pending") | "\(.name): \(.bucket)"' <<<"$s" | sort)
  comm -13 <(echo "$prev") <(echo "$cur")
  prev=$cur
  jq -e 'all(.bucket!="pending")' <<<"$s" >/dev/null && break
  sleep 30
done
```

## Architecture

Pi's implementation should split into four focused pieces:

1. **Tool contract layer**
   - validates the four-field input contract
   - normalizes timeout rules
   - allocates/returns a monitor handle

2. **Process runner**
   - spawns the shell child
   - captures stdout and stderr separately
   - owns kill/cleanup lifecycle

3. **Event aggregator**
   - reads stdout by complete line
   - applies 200ms coalescing
   - counts output volume for circuit breaking
   - emits transcript interrupts with description context

4. **Monitor registry**
   - tracks active handles for the session
   - supports stop-by-handle
   - tears down persistent monitors at session end

This separation keeps Claude-compatible semantics isolated from any future Pi-only durability layer.

## Error handling

- invalid `timeout_ms` above max should fail validation before spawn
- missing/empty `description` should fail validation or be strongly rejected in tool docs
- failure to create stderr artifact should still not convert stderr into events; instead report monitor setup failure
- if line decoding fails, treat it as monitor/process failure and terminate with a final error notification

## Testing strategy

### Contract tests

- defaults: non-persistent, 300000ms timeout
- max timeout enforcement
- `persistent: true` ignores timeout
- handle returned

### Event tests

- one stdout line => one event
- several lines within 200ms => one coalesced notification
- lines beyond 200ms => separate notifications
- stderr-only output => no events
- mixed stdout/stderr => only stdout events, stderr captured to artifact

### Lifecycle tests

- child exit => final status
- timeout => kill + final timeout status
- explicit stop => final stopped status
- persistent monitor survives multiple turns in one session
- monitors removed on session teardown

### Safety tests

- circuit breaker stops a noisy monitor
- final message suggests tighter filtering
- raw multiline output path does not bypass coalescing

### Documentation examples

- executable smoke tests where practical for the reference patterns, especially bounded CI-style poll-loop behavior

## Open decisions intentionally deferred

These are not needed for Claude-compatible v1 and should not block implementation:
- durable monitors across Pi restarts
- exposing circuit-breaker thresholds publicly
- adding semantic parsers on top of line events
- automatic paging integrations
