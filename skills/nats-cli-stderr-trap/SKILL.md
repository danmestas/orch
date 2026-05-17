---
name: nats-cli-stderr-trap
description: Use when scripting against the nats CLI — counting replies, fanning out requests, capturing headers, driving JetStream/KV from scripts, or running ws:// clients. Triggers on "count nats replies", "nats CLI in a script", "nats req --replies", "nats sub returns nothing", "why is my counter 0", "nats CLI WebSocket", "ws:// with nats CLI", "capture nats headers", "extract traceparent from reply", "nats req from bash background", "nats stream / consumer / kv from CLI", "nats CLI returns empty but server has data". Documents the stderr/stdout split (the most common trap), correct counting idioms, `--replies` semantics, WebSocket support, and header capture patterns. Pairs with `bench-docker-sesh-author` and `bench-debug-playbook`.
---

# nats-cli-stderr-trap

The `nats` CLI is the workhorse for any script that touches a NATS bus. It has defaults that surprise first-time scripters and silently produce wrong results. This skill is the field guide for using it correctly in shells and benches.

## The stderr/stdout split — the most common trap

`nats req` and `nats sub` emit two distinct streams:

- **STDOUT**: reply bodies, one per reply.
- **STDERR**: human-facing log lines like `Sending request on "..."` and `Received with rtt 495µs`.

This bites everyone the first time. A counter like:

```bash
n=$(nats req SUBJ '' --replies=0 --timeout=5s 2>/dev/null | grep -c "Received")
```

returns **0 even when replies arrive fine**. The "Received" lines are in stderr, and we redirected stderr to `/dev/null`.

### Right way to count replies

Pick a distinctive substring of the reply body and count its occurrences:

```bash
n=$(nats req '$SRV.INFO.agents' '' --replies=0 --timeout=5s 2>/dev/null \
    | grep -c '"name":"agents"')
```

If the body has no convenient marker, merge stderr and count there:

```bash
n=$(nats req SUBJ '' --replies=0 --timeout=5s 2>&1 | grep -c "Received with rtt")
```

Either works. The first is preferable in production scripts because it's robust to nats CLI log-format changes.

## `--replies` semantics

| value | meaning |
|---|---|
| `--replies=1` (default) | wait for ONE reply, then exit |
| `--replies=N` | wait for N replies or timeout |
| `--replies=0` | wait until `--timeout` elapses, collect ALL replies received in that window |

Use `--replies=0` for fan-out probes ($SRV.INFO.agents across multiple service instances, broadcast subjects, etc). Always pair with `--timeout` so the command actually exits.

## `--reply-timeout` vs `--timeout`

- `--timeout=Xs` — total command time bound.
- `--reply-timeout=Xs` — inter-reply timeout; meaningful when the server streams chunks. After the LAST reply lands, wait this long for one more before closing.

For Synadia prompt round-trips through the orch shim:

```bash
nats --server=URL req agents.prompt.cc.user.pct5 'hi' \
    --replies=0 \
    --reply-timeout=35s \
    --timeout=45s
```

The 35s `--reply-timeout` covers the shim's 30s terminator watchdog (mocks never close the stream voluntarily; the watchdog force-emits the terminator). The 45s `--timeout` is the outer bound.

## WebSocket support

`nats CLI` v0.4.0+ supports `--server=ws://` natively. No Node, no `@nats-io/transport-websockets`, no miniflare needed for bench-level WebSocket testing.

```bash
# TCP publish, WS subscribe — same hub, different transports
nats --server="$SESH_NATS_URL" pub broadcast.test "hello"
nats --server="$SESH_NATS_WS_URL" sub broadcast.test --count=1
```

The bench's Group 8 uses exactly this to validate sesh's TCP↔WS bridge — see `bench-docker-sesh-author` for the test pattern.

## Header capture

| flag | use |
|---|---|
| `-H 'key: value'` | inject an inbound header on a request |
| `--headers` (sub default) | display reply headers in output |
| `--headers-only` | only show headers, suppress body |
| `--raw` | suppress nats CLI log lines on stdout (cleaner parsing) |

Capture `traceparent` from a reply stream:

```bash
nats --server=URL req SUBJ 'body' \
    -H 'traceparent: 00-aabbcc..-112233..-01' \
    --replies=0 --reply-timeout=35s 2>&1 \
    | grep -oE 'traceparent: 00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}'
```

This is the pattern the bench's Group 11 uses to validate orch#117's envelope-headers work end-to-end.

## JetStream / KV from CLI

Quick recipes:

```bash
# Streams
nats --server=URL stream add NAME --subjects 'x.>' --storage memory --max-msgs=100
nats --server=URL stream info NAME
nats --server=URL stream rm NAME -f

# KV
nats --server=URL kv add BUCKET --history=10
nats --server=URL kv put BUCKET key value
nats --server=URL kv get BUCKET key --raw
nats --server=URL kv watch BUCKET           # newline-delimited change events

# Pull consumers
nats --server=URL consumer create STREAM C1 --pull --filter='x.>' --ack=explicit
nats --server=URL consumer next STREAM C1 --no-ack --count=N
```

## Background + wait hazards

Bash-backgrounded `nats req --replies=0` calls under `wait` can hang indefinitely in some bench configurations. Saw this reliably with multiple orch workers attached to the same hub — the bench's Group 13 originally tried parallel pulls and never returned.

Workaround:

```bash
# Don't do this when orch workers are on the hub:
for i in 1 2 3; do
    nats req SUBJ "$i" --replies=0 --timeout=5s &
done
wait   # may hang

# Do this instead — serial with per-call timeout:
for i in 1 2 3; do
    timeout 15 nats req SUBJ "$i" --replies=0 --timeout=5s
done
```

If you genuinely need parallelism, use `wait -n` with PID polling rather than bare `wait`, and bound each child with `timeout`.

## Common gotchas summary

| gotcha | symptom | fix |
|---|---|---|
| stderr vs stdout | counter is 0 | count body markers on stdout, or merge `2>&1` |
| `--replies=0` misread | hangs forever | always pair with `--timeout=Xs` |
| `--reply-timeout` not set on req | premature close on streaming reply | set both `--timeout` and `--reply-timeout` |
| `2>/dev/null` hides validation errors | empty results, no clue why | drop the redirect when debugging |
| wrong server URL (leaf vs hub) | `$SRV` requests return nothing | verify with `nats --server=X server check` |
| bash bg `nats req` + `wait` | hangs | serial + per-call `timeout` |

## Worked examples

### Counting service replies the right way

The bench's Group 13 antipattern (got 0 from grep "Received") and fix:

```bash
# Wrong (silent zero)
n_replies=$(nats --server=URL req '$SRV.PING.agents' '' \
    --replies=0 --timeout=5s 2>/dev/null | grep -c "Received")

# Right (count JSON bodies)
n_replies=$(nats --server=URL req '$SRV.INFO.agents' '' \
    --replies=0 --timeout=5s 2>/dev/null | grep -c '"name":"agents"')
```

### Traceparent propagation roundtrip

```bash
PARENT="0af7651916cd43dd8448eb211c80319c"
nats --server=URL req agents.prompt.cc.user.pct5 'hi' \
    -H "traceparent: 00-${PARENT}-00f067aa0ba902b7-01" \
    --replies=0 --reply-timeout=35s --timeout=45s > /tmp/cap 2>&1

# Every reply chunk's traceparent must share the PARENT trace_id:
grep -oE 'traceparent: 00-[0-9a-f]{32}-[0-9a-f]{16}-[0-9a-f]{2}' /tmp/cap \
    | awk -F- -v want="$PARENT" 'NF>=4 && $2 != want {print "MISMATCH: " $0}'
```

### WS↔TCP bridge probe

```bash
# Subscriber on WS, publisher on TCP — proves sesh hub bridges transports
timeout 10 nats --server="$SESH_NATS_WS_URL" sub broadcast.test --count=1 > /tmp/ws.cap 2>&1 &
sleep 1
nats --server="$SESH_NATS_URL" pub broadcast.test "hello"
wait
grep -q "hello" /tmp/ws.cap && echo "bridge works"
```

### JetStream durable replay

```bash
nats --server=URL stream add T --subjects 't.>' --storage memory --max-msgs=100
nats --server=URL pub t.evt "m1"
nats --server=URL pub t.evt "m2"
nats --server=URL consumer create T c1 --pull --filter='t.>' --ack=explicit
nats --server=URL consumer next T c1 --no-ack --count=2  # replays both
nats --server=URL stream rm T -f
```

## Cross-references

- `bench-docker-sesh-author` — for the bench's existing patterns that use these idioms.
- `bench-debug-playbook` — when nats CLI behavior surprises you, the debug recipes there extend these.
