# `test/bench/` — performance + stability harnesses

Out-of-band measurement tools for the Synadia substrate. Different from the
docker benches under `test/docker/` and `test/docker-sesh/` (which are
pass/fail smoke suites with hard CI gates):

- **`measure.sh`** — pre-migration latency + cost baseline (orch#83).
- **`soak.sh`** — long-running stability harness (orch#90).
- **`baselines/`** — checked-in baseline measurements for diffing future
  runs against. Updated when methodology changes; not edited per-run.

## `soak.sh` — long-running stability harness

Spawns N workers (one per harness in `SOAK_HARNESSES`), drives M prompts
each over a wall-clock window, samples per-worker resource + protocol
metrics on a fixed cadence, emits a markdown report with anomaly findings.

**Designed for nightly / weekly cron, not per-PR CI.** A 60-minute soak
would dominate CI runtime; this is opt-in.

### Run it

```sh
# default 60min × 4 harnesses × 100 prompts each
./test/bench/soak.sh

# fast local validation (2 min, smaller fleet)
SOAK_DURATION=2m SOAK_PROMPTS_PER_WORKER=20 ./test/bench/soak.sh

# claude-only smoke
SOAK_HARNESSES=claude SOAK_DURATION=5m ./test/bench/soak.sh

# reuse existing image (skip rebuild — only changed soak-runner.sh)
./test/bench/soak.sh --no-build
```

### Env knobs

| Var | Default | Meaning |
|---|---|---|
| `SOAK_HARNESSES` | `claude,codex,pi,gemini` | CSV of harnesses to spawn workers for |
| `SOAK_PROMPTS_PER_WORKER` | `100` | Each worker fields this many prompts before stopping early |
| `SOAK_DURATION` | `60m` | Wall-clock max; harness exits at min(duration, prompts-completed) |
| `SOAK_BROADCAST_RATIO` | `0.5` | Fraction of prompts sent as broadcast (vs. targeted) |
| `SOAK_SAMPLE_INTERVAL` | `60` | Seconds between metric snapshots |
| `SOAK_OUTPUT` | `test/bench/soak-<ts>.md` | Report destination |

### What's measured

Per worker, at every sample interval + final tick:

- **Prompt success / error count** — protocol-level: did `nats req` get a reply within timeout?
- **RSS memory** — `ps -o rss=` on the shim process
- **Open file handles** — `/proc/PID/fd` count (Linux-only inside the container)
- **Heartbeat count** — counted via background `nats sub agents.hb.>` collector, grouped by `agent` field

### Anomaly flagging (auto-surfaced in report's "Findings" section)

The runner flags:

- Error rate **> 1%** per harness
- RSS growth **> 20%** start-to-end
- File handle growth **≥ 5** net over the run
- Heartbeat coverage **< 95%** of expected (expected = `floor(elapsed_s / 30) − 1`; shim heartbeat default is 30s)

No hard pass/fail gate — the harness exits 0 regardless of findings. The
operator inspects the report.

### Why no CI integration

A 60-min soak would dominate per-PR CI cost. The soak's value is **slow-burn
regression detection** (memory leaks, FD leaks, heartbeat gaps under load)
that the short smoke benches don't catch. Right home is nightly cron or
manual pre-release runs, not per-PR.

If adopted as a recurring job, the workflow shape is:

```yaml
on:
  schedule:
    - cron: '0 6 * * *'   # daily 06:00 UTC
jobs:
  soak:
    runs-on: ubuntu-latest
    timeout-minutes: 75
    steps:
      - uses: actions/checkout@v5
      - run: ./test/bench/soak.sh
      - uses: actions/upload-artifact@v4
        with:
          name: soak-${{ github.run_id }}
          path: test/bench/soak-*.md
```

### Baselines

`test/bench/baselines/` is for checked-in known-good snapshots. Update on
methodology changes (e.g., new metric added) or when substrate semantics
shift (new shim version, new spec version). Comparing a fresh report
against a baseline is currently manual; an automated diff would be a
useful follow-up.
