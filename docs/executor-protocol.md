# Executor protocol — dispatcher ↔ backend contract

The wire contract between `orch-spawn` (the dispatcher) and per-executor
backends (`tmux`, `cf-worker`, `cf-durable-object`, future
`devcontainer` / `k8s-pod` / `browser-tab`). See
[docs/spawn-spec.md](./spawn-spec.md) for the SpawnSpec / WorkerHandle
YAML field reference, and
[docs/proposals/0002-typed-executor-contract.md](./proposals/0002-typed-executor-contract.md)
for the design rationale.

This document is the protocol — the shape of the conversation, not the
shape of the documents.

## Wire shape

```
$ <executor-backend>
  stdin:  SpawnSpec YAML  (one document)
  stdout: WorkerHandle YAML  (one document) on success
  stderr: human-readable diagnostics (always permitted)
  exit:   0 on success; non-zero on failure
```

One backend invocation produces at most one WorkerHandle. The backend
MUST NOT emit anything to stdout on failure — stdout is reserved for
the handle. Diagnostics, progress, warnings all go to stderr.

Backends are discovered by name from `$ORCH_EXECUTORS_DIR` (default
`~/.local/share/orch/executors/<name>`) or via `PATH` lookup for an
`orch-executor-<name>` binary. The dispatcher picks the first match.

## Exit code semantics

| Exit | Meaning | stdout | stderr |
|---|---|---|---|
| `0` | Success | WorkerHandle YAML (one document) | optional warnings |
| non-zero | Failure | empty | human-readable cause; last line SHOULD summarise |

The dispatcher treats non-zero exit + empty stdout as authoritative
failure. A backend that writes partial handle YAML and then errors out
MUST exit non-zero AND clear/omit the partial handle — silent-partial
output corrupts the dispatcher's parse.

For async-provisioning backends (CF Worker takes ~30s to deploy), the
backend exits `0` immediately and writes a WorkerHandle with
`status: pending`; the operator polls until `ready` or `failed`. See
the `Status` field reference in [docs/spawn-spec.md](./spawn-spec.md).

## Validation contract — hybrid

Validation is **hybrid**: dispatcher always validates, backends MAY
re-validate.

### Dispatcher (always)

`orch-spawn` MUST validate the SpawnSpec via
[`spawnspec.ValidateSpec`](../internal/spawnspec/validate.go) before
invoking any backend. This catches typos, missing fields, executor-XOR
violations, bad agent enums, and outfit-shape errors at the point
closest to the operator — failures surface in the operator's CLI,
not deep inside a remote backend.

```go
import "github.com/danmestas/orch/internal/spawnspec"

spec, err := spawnspec.UnmarshalSpec(yamlBytes)
if err != nil {
    return fmt.Errorf("spawn-spec parse: %w", err)
}
if err := spawnspec.ValidateSpec(spec); err != nil {
    return fmt.Errorf("spawn-spec invalid: %w", err)
}
// Only now: pipe yamlBytes to the backend's stdin.
```

The workflow compiler (`orch-workflow validate`, `orch-workflow
compile`) inlines the same call so `spawn:` nodes inside workflow YAML
get the identical contract enforced at compile time — typos in a
workflow file surface as Phase A diagnostics with the offending
node-id and line number, not as runtime confusion inside a backend.
See `internal/workflow/validate.go` (`checkSpawnBodies`) and
`CodeInvalidSpawn`.

### Backend (recommended)

Backends MAY re-validate the SpawnSpec on their own input. This is
recommended for two reasons:

1. **Remote / async backends.** A `cf-worker` backend running in a
   Cloudflare datacenter is far from the dispatcher; the network /
   queue between them could (in theory) corrupt the spec or route a
   spec to the wrong backend version. Local re-validation rejects the
   request before any side effects.
2. **Independent invocation.** A backend SHOULD be runnable standalone
   for diagnostics (e.g. `cat spec.yaml | orch-executor-tmux`) without
   going through the dispatcher. Self-validation makes that safe.

Backends MUST NOT skip validation that the dispatcher already performs
on the assumption "the dispatcher checked." That assumption fails the
moment the dispatcher is bypassed for testing, the contract version
shifts, or a future operator pipes a hand-crafted spec.

Validation duplication is the protocol's intended shape — same as
TCP/IP, where every hop validates the checksum.

### Version gate

Both sides MUST reject a SpawnSpec whose `spec_version` doesn't match
the value the binary speaks (`spawnspec.SpecVersion`, currently `v1`).
Specs without an explicit `spec_version` default to the current value.
A mismatch is a fatal parse error, not a warning — the wire format is
versioned for a reason.

## Examples

### Minimal SpawnSpec (stdin to backend)

```yaml
spec_version: v1
name: verifier
agent: claude-code
tmux: {}
```

### Minimal WorkerHandle (stdout from backend)

```yaml
spec_version: v1
name: verifier
agent: claude-code
created_at: 2026-05-22T10:30:00Z
executor: tmux
pane_id: "%64"
status: ready
abort:
  kind: tmux-send-keys
  target: "%64"
  keys: "C-c"
```

## Schema references (non-Go consumers)

Canonical Go structs live in `internal/spawnspec/`. The published JSON
Schema artifacts are the contract for non-Go consumers (TS UI, Python
validators, GitOps tooling):

- `dist/schema/spawn-spec.v1.json`
- `dist/schema/worker-handle.v1.json`

Regenerate via `go run ./cmd/spawnspec-schema -out dist/schema`.

## Where this contract is enforced

| Caller | File | What it does |
|---|---|---|
| `orch-spawn` dispatcher | `cmd/orch-spawn/*` | Builds SpawnSpec from operator flags, validates, pipes to backend stdin. |
| `orch-workflow` compiler | `internal/workflow/validate.go` (`checkSpawnBodies`) | Validates every `spawn:` node body in a workflow YAML before compile. |
| Backend binaries | `executors/<name>/spawn.sh` (or Go equivalent) | Optionally re-validate on input; produce WorkerHandle on stdout. |

A new caller adopting this contract: import `internal/spawnspec`,
call `UnmarshalSpec` then `ValidateSpec`, treat the returned error as
fatal. Don't reinvent the validation — duplicating the rules drifts
from the canonical Go structs.
