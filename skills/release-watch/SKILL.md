---
name: release-watch
description: Watch a release flow — build → publish → install → verify — using a Monitor (push notification per state change) instead of a `until ...; do sleep 5; done` bash poll. Use when the user says "release this", "publish v0.X", "wait for the release to finish", "ship it", or when you've just run `gh workflow run` / `cargo publish` / `npm publish` / `pip upload` and need to react to its completion. Triggers on any "watch this release/build/deploy until it's done" intent. Pairs with the `orch-driver` "Choosing the right wait primitive" rule.
---

# release-watch

Drive a release flow with the right Claude Code persistence primitive (Monitor / CronCreate / /loop) instead of a bash-poll loop. The poll loop is the canonical violation of `feedback_use_monitor_cron_loop`; this skill makes the right pattern enforced-by-construction.

## When to use

You just kicked off a release and need to react when it lands. Concretely, any of:

- `gh workflow run release.yml` and need to know when CI finishes
- `npm publish` / `cargo publish` / `pip upload` / `gh release create` — need to wait for the registry to reflect the new version
- A multi-stage chain: GitHub workflow → npm publish → install on target host → smoke test
- Anything where the next step depends on a future state-of-the-world change

## When NOT to use

- A single short command you can synchronously `wait` for. Just run it inline.
- A turn-bound wait that completes in <60s. `Bash(run_in_background=true)` with an `until` loop is fine, and one cache-warm wakeup is cheaper than arming a Monitor.
- An indefinite "watch forever" with no terminal state. That's an alert, not a release watch — use Monitor with explicit alert grep, not this skill.

## The decision tree

Pick a primitive based on the *shape of the wait*, not the duration.

| Wait shape | Primitive | Why |
|---|---|---|
| Stream of state changes, terminal state known | **Monitor** with poll loop that exits on success/failure | One push per state transition; loop exits on terminal state |
| Single completion within session | `Bash(run_in_background=true)` with `until` | One push when the bash exits; no Monitor overhead |
| Cross-session ("check tomorrow") | **CronCreate** | Survives session end |
| Recurring polling at fixed interval | **/loop** with interval | Operator-paced cadence |

The rest of this skill assumes the Monitor case (the most common release shape).

## Recipes

### npm publish → wait for registry visibility

```python
Monitor(
    description="npm registry visibility for @agent-ops/suit",
    persistent=False,
    timeout_ms=600000,   # 10 min; tighten if you know the typical publish-to-CDN delay
    command="""
last=""
while true; do
    cur=$(npm view @agent-ops/suit version 2>/dev/null || echo "")
    if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
        echo "registry: @agent-ops/suit@$cur"
        last=$cur
    fi
    if [ "$cur" = "$EXPECTED" ]; then
        echo "DONE: @agent-ops/suit@$cur visible"
        break
    fi
    sleep 30   # registry caches; <30s is wasteful
done
""",
)
```

Set `EXPECTED` in env before launching: `Monitor(env={"EXPECTED": "0.13.0"}, ...)` or interpolate the literal version into the command.

### cargo publish → wait for crates.io

Same shape as npm; replace the version probe with `cargo search <crate> --limit 1`. Crates.io has typical 1–2 minute index propagation; poll every 30s.

### pip publish → wait for PyPI

```bash
last=""
while true; do
    cur=$(pip index versions <pkg> 2>&1 | grep -oE 'Available versions: [0-9.]+' | head -1 | awk '{print $3}')
    if [ "$cur" != "$last" ]; then
        echo "pypi: <pkg>==$cur"; last=$cur
    fi
    [ "$cur" = "$EXPECTED" ] && echo "DONE" && break
    sleep 30
done
```

### GitHub release workflow → wait for run to complete

```python
Monitor(
    description="gh workflow release.yml — emit per-job status",
    persistent=False,
    timeout_ms=1800000,   # 30 min cap
    command="""
RUN_ID=$(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')
prev=""
while true; do
    cur=$(gh run view "$RUN_ID" --json status,conclusion,jobs \\
          -q '{status:.status, conclusion:.conclusion, jobs:[.jobs[] | {name:.name, status:.status, conclusion:.conclusion}]}' 2>/dev/null)
    if [ "$cur" != "$prev" ]; then
        # Diff: emit per-job rows that just transitioned.
        printf '%s\\n' "$cur" | jq -c '.jobs[] | select(.status=="completed")'
        prev=$cur
    fi
    status=$(printf '%s\\n' "$cur" | jq -r .status)
    [ "$status" = "completed" ] && echo "DONE: $(printf '%s\\n' "$cur" | jq -r .conclusion)" && break
    sleep 30
done
""",
)
```

The terminal-state grep emits `DONE: success` or `DONE: failure`. Watch for `failure` in the next conversation turn — Monitor stays silent on a slow run, and silence shouldn't look the same as "succeeded."

### GitHub release create → wait for the asset to be downloadable

```bash
ASSET_URL="https://github.com/$REPO/releases/download/v$EXPECTED/$ASSET"
while ! curl -sfI "$ASSET_URL" >/dev/null 2>&1; do
    echo "waiting for $ASSET_URL"
    sleep 30
done
echo "DONE: $ASSET_URL is downloadable"
```

Wrap that in `Monitor(persistent=False, timeout_ms=600000, command="...")`.

### Multi-stage chain: workflow → publish → install → verify

Compose stages in a single Monitor, emit one line per stage transition. The Monitor exits on the verify step (or on any stage failure).

```bash
emit_stage() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$1"; }

# Stage 1: workflow
RUN_ID=$(gh run list --workflow=release.yml --limit 1 --json databaseId -q '.[0].databaseId')
while [ "$(gh run view "$RUN_ID" --json status -q .status)" != "completed" ]; do sleep 30; done
CONC=$(gh run view "$RUN_ID" --json conclusion -q .conclusion)
emit_stage "workflow: $CONC"
[ "$CONC" = success ] || { echo "FAIL: workflow $CONC"; exit 1; }

# Stage 2: registry visibility
while [ "$(npm view "$PKG" version 2>/dev/null)" != "$EXPECTED" ]; do sleep 30; done
emit_stage "npm: $EXPECTED visible"

# Stage 3: install on local
npm i -g "$PKG@$EXPECTED" >/dev/null
emit_stage "installed locally"

# Stage 4: smoke
"$PKG" --version | grep -q "$EXPECTED" || { echo "FAIL: smoke version mismatch"; exit 1; }
emit_stage "smoke: $EXPECTED matches"

echo "DONE: $PKG@$EXPECTED end-to-end"
```

## What "DONE" means

The poll loop must emit a single, distinct `DONE` line on terminal success and exit. It should also emit `FAIL` (or any line containing the word) on terminal failure. The Monitor wrapper filters its stdout into push notifications; without a definitive terminal line, you'll either get false silence (loop hung) or noise (every poll fires).

Coverage check before arming: *if the workflow crashed right now, would my filter emit anything?* If the answer is "no — it'd just go silent," widen the loop to also emit on cancelled/failed/timed-out states.

## Common gotchas

- **Sleep cadence vs cache TTL**: `npm view` hits the registry CDN with ~30s cache. Polling faster than 30s mostly gets cached responses — wastes effort, doesn't shorten the wait. Same logic for crates.io (~60s) and PyPI's `pip index` (~30s).
- **The bash-bg-poll antipattern**: `Bash(... while ...; do sleep 5; done, run_in_background=true)` works for one terminal check inside the current session and dies with the bash. It can't compose with CronCreate, can't survive restart, and conflates "I'm polling" with "the system is durably waiting." Use Monitor for any wait you'd describe as "watch this release."
- **`gh workflow run` is fire-and-forget**: it returns 0 the moment the workflow is enqueued, NOT when it completes. Always follow with `gh run list --limit 1` to capture the run id and start a Monitor on it.
- **Release tags vs published versions**: a tag pushed to GitHub is not the same as a version visible in npm/PyPI/crates.io. Watch the registry, not the tag.
- **Local install drift**: after `npm i -g`, run `which <bin>` to confirm PATH resolves to the new install, not a prior copy in `~/.local/bin/`. The harness has had this exact bug; `orch-version` catches it after the fact.

## Cross-references

- `orch-driver` skill — "Choosing the right wait primitive" section codifies the same rule at the listener layer.
- `feedback_use_monitor_cron_loop` memory — the originating doctrine.
- `orch-version` — useful as the verify step of a release-watch chain when shipping changes to the harness itself.
