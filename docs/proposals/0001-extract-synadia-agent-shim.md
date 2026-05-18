# Proposal 0001 — Extract `synadia-agent-shim` to a sister repo

**Status:** draft (not yet committed; spec only, design phase to follow)
**Depends on:** none
**Blocks:** Proposal 0003 (executor extraction depends on shim consumed as binary)

## Why

The shim is the largest deep module in orch:

- ~3000 LoC Go behind a small public interface (CLI flags + Adapter contract)
- 5 adapters already prove the Adapter seam is real (claudecode / codex / pi / gemini / echo)
- Implements a public spec (Synadia Agent Protocol v0.3 + the sesh envelope extension) — has reuse potential beyond orch (dagnats-agents, alternative orchs, the reference `sesh-ref-agent` could converge on the same implementation)

Today the shim:
- Couples orch's npm package to the Go toolchain (orch can't be pure-bash)
- Couples orch's bench to the shim's Go build path
- Locks the Adapter contract behind orch's release cadence (any non-orch consumer can't pick up shim improvements independently)

Extracting unlocks:
- Independent release cadence for the protocol implementation
- A public Adapter contract that other Synadia consumers can target
- orch becomes operator UX + executor abstraction + bench (substantially smaller)

## Goals

1. New repo `github.com/danmestas/synadia-agent-shim` (also distributable via Go modules + prebuilt binaries)
2. orch consumes the shim as a binary dependency, not source
3. Adapter interface is the public Go API; new adapters can be built by importing the package without forking the repo
4. Backwards-compatible: existing orch-spawn invocations still work after the cutover

## Non-goals

- Migrating the shim's CLI surface (preserve current flag shapes)
- Renaming the binary's behavior (still produces Synadia-spec chunks; envelope headers; trace propagation)
- Changing the Adapter interface (existing 5 adapters survive untouched in the new repo)

## Public interface

### CLI (unchanged)

```
synadia-agent-shim --agent <claude-code|codex|pi|gemini|echo> \
                   --pane <%N> \
                   --cwd <path> \
                   [--session <label>] \
                   [--owner <name>] \
                   [--task-id <id>] \
                   [--role <name>] \
                   [--outfit <name>] \
                   [--nats <url>] \
                   [--interval <duration>]
```

Existing flag names preserved. Resolution order (env, flags, fallback) unchanged.

### Go package layout

```
synadia-agent-shim/
├── cmd/
│   └── synadia-agent-shim/        # main package — the binary
│       └── main.go
├── shim/                           # PUBLIC — the protocol implementation
│   ├── shim.go                    # Config, Run, RunWithConn
│   ├── chunk.go                   # Chunk types (Response, Status, Query, Terminator, Error)
│   ├── envelope.go                # Sesh envelope headers (W3C traceparent + Sesh-*)
│   ├── adapter.go                 # Adapter interface
│   └── shim_test.go               # conformance tests
├── adapter/                        # PUBLIC — built-in adapters
│   ├── claudecode/
│   ├── codex/
│   ├── pi/
│   ├── gemini/
│   └── echo/
├── docs/
│   ├── adapter-sdk.md             # how to write a new adapter
│   ├── synadia-comparison.md      # spec compliance notes
│   └── orch-signals.md            # orch.signal.> handler reference
├── README.md
├── LICENSE
├── go.mod
└── go.sum
```

### Public Go API (frozen at v1)

```go
package shim

type Adapter interface {
    Start(ctx context.Context) error
    OnPrompt(ctx context.Context, prompt string) error
    Events() <-chan Chunk
    Close() error
    // Abort is OPTIONAL. Implementations that need imperative
    // cancellation (TUI harnesses that don't honour ctx.Done())
    // implement this; the shim type-asserts and calls it on
    // orch.signal.interrupt arrival.
}

type AbortAdapter interface {
    Adapter
    Abort(ctx context.Context) error
}

type Config struct {
    Agent       string
    AgentToken  string
    Pane        string
    Owner       string
    Session     string
    NATSURL     string
    Outfit      string
    Role        string
    CWD         string
    Harness     string
    TaskID      string
    Interval    time.Duration
    Adapter     Adapter
}

func Run(ctx context.Context, cfg Config) error
func RunWithConn(ctx context.Context, nc *nats.Conn, cfg Config) error

type Chunk struct { /* fields */ }
type ChunkType string

const (
    ChunkStatus    ChunkType = "status"
    ChunkResponse  ChunkType = "response"
    ChunkQuery     ChunkType = "query"
)

// Constructor helpers, public.
func NewResponseChunk(data any) Chunk
func NewStatusChunk(value string) Chunk
func NewQueryChunk(id, replySubject, prompt string) Chunk
func NewTerminatorChunk() Chunk
func NewErrorChunk(code int, msg string, body map[string]any) Chunk

// Resolves NATS URL using documented precedence: override → $NATS_URL → ~/.sesh/hub.url → default.
func ReadNATSURL(override string) string
```

The `Adapter` and `Config` shapes are frozen at v1. Adding fields requires adapter-API minor version bump.

### Wire surface (unchanged)

- Subjects: `agents.{prompt,status,hb}.{token}.{owner}.{session-or-pane-enc}`
- Envelope headers: traceparent (W3C), Sesh-Envelope: 1, Sesh-Role, Sesh-Task-Id (opt), Sesh-Attempt
- Signal subscription: `orch.signal.{interrupt,redirect}.{token}.{owner}.{session}` (from orch#133)
- Conformance: Synadia §12 checklist preserved

## Migration plan

### Step 1: Create the new repo, move code (no orch changes)

1. New repo `github.com/danmestas/synadia-agent-shim`, initialised from current orch HEAD's shim subtree.
2. File moves:
   - `cmd/orch-agent-shim/main.go` → `cmd/synadia-agent-shim/main.go`
   - `internal/shim/` → `shim/` (note: was internal, now public)
   - `internal/adapter/*` → `adapter/*` (same — internal → public)
   - `docs/orch-agent-shim.md` → README + `docs/adapter-sdk.md`
   - `docs/orch-signals.md` → `docs/orch-signals.md`
3. Module path: `github.com/danmestas/synadia-agent-shim`
4. All existing tests survive (rename import paths)
5. CI: GitHub Actions builds + tests on push; releases tagged binaries via goreleaser (matching orch's existing release machinery)

### Step 2: orch consumes the binary

1. `orch-spawn` keeps shelling out to `orch-agent-shim` on PATH (existing behavior). Compatibility shim: provide `orch-agent-shim` as an alias name on the new release for one major version.
2. orch's `install.sh` adds a step: "fetch latest synadia-agent-shim release binary; symlink as `orch-agent-shim` for backwards-compat".
3. orch's npm package's `postinstall` includes a download step OR vendors a known-good shim binary tag per OS/arch.

### Step 3: orch removes the Go code

1. Delete `cmd/orch-agent-shim/`, `internal/shim/`, `internal/adapter/*` from orch.
2. orch repo no longer needs go.mod (modulo `executors/wasm/cf-worker/` which is TS) — orch's Makefile / Justfile drops the Go build step.
3. orch's docker-sesh bench Dockerfile builds the shim from the sister repo's release tag instead of orch source.
4. orch's adapter tests move to the shim repo; orch retains end-to-end bench tests that exercise the shim binary's CLI.

### Step 4: Release the shim independently

- Semver tracking: shim v1.0.0 = current behavior at extraction time
- Each Synadia spec version bump = shim major version bump (v0.3 today; if Synadia ships v0.4, shim's major increments)
- Adapter API additions = minor version bump
- Bug fixes = patch

## Backwards compatibility

- Binary name: rename + alias period — ship as both `synadia-agent-shim` (canonical) and `orch-agent-shim` (alias) for one major orch release, then drop the alias.
- Adapter API: orch's existing adapters survive byte-for-byte (just new import paths).
- Wire surface: zero change. Subjects, headers, chunk shapes all preserved. Bench's behavior should be byte-equivalent.

## Acceptance criteria

- [ ] New repo exists with full shim source + adapters + tests + docs
- [ ] `go test ./...` in the shim repo passes (matches orch's pre-extraction 63/63)
- [ ] Released binary on GitHub Releases for at least macOS arm64 + linux amd64
- [ ] orch's `install.sh` and npm `postinstall` fetch the binary cleanly
- [ ] orch's docker-sesh bench passes (64/0/0) using the released shim binary, not in-repo source
- [ ] orch's repo size drops by ≥50% (Go source removed)
- [ ] orch's CI no longer includes a Go build step
- [ ] At least one non-orch consumer can import `shim` package and write a new adapter (verify via a sample repo)

## Open questions (resolved during design phase)

1. **Adapter discovery**: should adapters be in-tree (`adapter/{claudecode,...}/`) or pluggable (Go plugin or out-of-tree imports)? Lean: in-tree for v1 (5 known adapters); reassess if community contributions appear.
2. **Adapter naming**: `claude-code` vs `claude` vs `cc`? Current shim uses all three at different layers. Worth standardizing during extraction.
3. ~~**Binary distribution**~~ → **npm** (Dan: 2026-05-18). Same distribution channel as orch today (`@agent-ops/synadia-agent-shim`). npm postinstall fetches/builds the binary for the host's platform. Avoids Homebrew/apt/Nix fragmentation; one install path for the ecosystem.
4. **Version skew tolerance**: how does the shim handle an orch CLI that requests adapter features the shim doesn't support? Define explicit handshake (e.g., shim advertises supported adapter list in `--help-json`).
5. **Where does orch-signal.> spec live?** Currently orch-defined extension. After extraction the shim implements it; orch documents it. Consider publishing the orch.signal.> verb catalog in shim repo OR orch repo. Lean: shim implements, orch defines (subject-namespace ownership = orch's).

## Risks

- **Cross-repo coordination overhead**: PR for shim feature → PR in orch to consume → cross-team test. Mitigation: shim's adapter API stays stable; only NEW capabilities require orch updates.
- **Install UX regression** if not planned: don't merge the extraction until the install path is rock-solid.
- **Bench fragmentation**: bench can stay in orch (the integration story) OR move to shim (the unit story). Lean: orch bench stays as integration; shim repo gets focused unit tests + a contract bench.

## Effort estimate

~1 week for one focused engineer:
- Day 1: new repo setup, file moves, import-path fixes, tests pass
- Day 2: release machinery (goreleaser), binary builds, README
- Day 3: orch consumes binary; install.sh + postinstall updated
- Day 4: orch removes Go source; docker-sesh bench updated
- Day 5: smoke test full flow; alias compatibility; release notes
