# Docker test bench for orch

A clean-install smoke suite that boots a fresh Ubuntu container, installs
orch via the `npm pack`'d working tree (so it exercises the package as
real users get it), and verifies the core stack:

- `orch-*` binaries on PATH after install
- `suit` lists outfits (after cloning the public [`wardrobe`](https://github.com/danmestas/wardrobe) repo into suit's content path)
- `orch-spawn` creates a tmux pane
- `suit prepare` produces a bundle dir
- Synadia §12 adapter contract (T9/T10/T11, opt-in via `--with-shim`)

Legacy bridge / fs-marker coverage (T4–T7) was retired in orch#94 along
with the bridge daemon and per-harness publish hooks.

The tests use a mock `claude` shell script (`inside-container/mock-agents/claude`)
that simulates the real harness — prints the expected banner, reads stdin,
fires the orch hooks on each "turn". This validates the orch plumbing
without needing real AI provider credentials in CI.

## Run it

```sh
# From repo root:
./test/docker/run-tests.sh
```

The script:

1. `npm pack`s the current working tree to `/tmp/<tarball>.tgz`
2. Copies the tarball into `test/docker/` for the Docker build
3. Builds `orch-docker-tests:local`
4. Runs the container; output is the test bench's output, exit code is 0
   on all-pass / 1 on any failure / 99 on infrastructure timeout

## Interactive debug

If a test fails and you want to poke around inside the container:

```sh
./test/docker/run-tests.sh --shell
```

Drops into `/bin/bash` after image build. From there you can run
`/usr/local/bin/bootstrap.sh` or any of its steps individually.

To re-run the suite without rebuilding (after editing test scripts on the
host and rebuilding the image with `docker build`):

```sh
./test/docker/run-tests.sh --no-build
```

## What's NOT tested here

- **Real AI harnesses** (claude/codex/pi/gemini binaries): would require
  API keys and produce non-deterministic latency. Mock claude proves the
  plumbing. A future opt-in "live" mode behind `ANTHROPIC_API_KEY` could
  add reality checks.
- **Multi-host NATS topology**: the container runs a local
  `nats-server`; the bridge talks to that. sesh hub's leaf-of-hub
  arrangement isn't exercised here. A multi-container compose setup is
  the obvious follow-up.
- **Pi extension auto-discovery**: pi-extensions/ ships TypeScript that
  pi loads at runtime. Without the real pi binary, that path is unused.
- **Gemini-specific hooks**: gemini's `AfterAgent` wiring is verified
  manually for now; adding a mock-gemini that fires `AfterAgent`-named
  hooks is straightforward follow-up.

## Synadia adapter contract

Three additional test groups (T9/T10/T11) verify that the shipped
`orch-agent-shim` satisfies the Synadia Agent Protocol v0.3 §12
conformance checklist.  They run under an opt-in flag because they
require the shim binary and a 2 s heartbeat cadence (adds ~10 s):

```sh
./test/docker/run-tests.sh --with-shim
# or equivalently:
MOCK_USE_SHIM=1 ./test/docker/run-tests.sh
```

| Group | What it tests |
|-------|---------------|
| **T9** | `$SRV.INFO.agents` discovery — response contains `metadata.protocol_version: "0.3"` and `metadata.agent: "claude-code"` |
| **T10** | Typed chunk sequence — leading `{type:"status",data:"ack"}`, ≥1 `{type:"response",...}`, zero-body terminator |
| **T11** | Heartbeat cadence — `agents.hb.cc.<owner>.<pane>` receives ≥2 beats in 6 s with valid §8.3 payload fields (`agent`, `owner`, `instance_id`, `ts`, `interval_s`) |

CI runs T9/T10/T11 automatically on PRs touching
`cmd/orch-agent-shim/`, `executors/`, or `internal/adapter/`
(see `.github/workflows/ci.yml` job `adapter-contract`).

## Adding tests

`inside-container/tests.sh` uses a simple `assert desc expected got`
helper. Add cases inline; each one increments PASS or FAIL and the script
exits non-zero on any failure.

For new mock harnesses, drop the binary into
`inside-container/mock-agents/` and add a `COPY ... && install -m 0755`
line in the Dockerfile alongside the existing `claude` line.
