// Package spawnspec defines the typed contract between orch-spawn (the
// dispatcher) and per-executor backends (tmux, cf-worker, cf-durable-object,
// future devcontainer / k8s pods). Two types travel the wire:
//
//   - SpawnSpec — dispatcher → backend. Declarative: "spawn this agent
//     with this identity in this executor."
//   - WorkerHandle — backend → dispatcher. Records what was actually
//     spawned (executor-specific id, bus subjects, abort verb, lifecycle
//     status).
//
// Both wire formats are YAML. Go structs in this file are canonical; the
// JSON Schema at dist/schema/spawn-spec.v1.json is generated from them so
// non-Go consumers (TS UI, Python validators) can validate without
// hand-maintaining a parallel schema. This mirrors the
// Kubernetes / Argo / Tekton pattern: Go structs are source of truth,
// JSON Schema is published artifact.
//
// Shape borrowed from Archon: flat top-level (no apiVersion/kind
// ceremony), one-of executor discriminator at root, optional
// cross-cutting metadata (labels, owner, env). See
// docs/proposals/0002-typed-executor-contract.md for rationale.
package spawnspec

import "time"

// SpecVersion is the contract version emitted and accepted by this
// package. Specs without an explicit spec_version default to this;
// specs with a different value fail validation with a clear message.
// Bumping this is a breaking change in the wire protocol.
const SpecVersion = "v1"

// Agent enumerates accepted values for SpawnSpec.Agent. The enum is
// closed-by-design (Ousterhout: define errors out of existence) — an
// unknown agent fails at parse, not at runtime inside a backend. To add
// an agent, extend the enum AND register the adapter in the shim.
type Agent string

const (
	AgentClaudeCode Agent = "claude-code"
	AgentCodex      Agent = "codex"
	AgentPi         Agent = "pi"
	AgentGemini     Agent = "gemini"
	AgentEcho       Agent = "echo"
)

// KnownAgents returns the closed enum used by the validator. Callers
// MUST NOT mutate the returned slice.
func KnownAgents() []Agent {
	return []Agent{AgentClaudeCode, AgentCodex, AgentPi, AgentGemini, AgentEcho}
}

// SpawnSpec is the dispatcher → backend message: "spawn this worker."
//
// Exactly one executor block (Tmux, CFWorker, CFDurableObject) MUST be
// set. Zero or two-or-more fails validation.
type SpawnSpec struct {
	// SpecVersion is the wire version. Empty defaults to SpecVersion
	// (v1). Anything else fails validation.
	SpecVersion string `yaml:"spec_version,omitempty" json:"spec_version,omitempty" jsonschema:"enum=v1"`

	// Name is the operator-facing identifier for this worker. Used as
	// the 5th subject token, the pane title, the handle filename. Must
	// be DNS-label-shaped (lowercase, hyphens, no dots) so it round-
	// trips through NATS subjects, tmux titles, and filesystems.
	Name string `yaml:"name" json:"name" validate:"required,dns_label"`

	// Description is free-form operator notes. Not consumed by the
	// dispatcher or backends.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// Agent is the harness to spawn. Closed enum — see KnownAgents.
	Agent Agent `yaml:"agent" json:"agent" validate:"required,agent"`

	// Session is the sesh session label (also the 5th subject token
	// when set; falls back to Name otherwise). Maps to SESH_SESSION.
	Session string `yaml:"session,omitempty" json:"session,omitempty"`

	// Cwd is the working directory the worker lands in. If empty, the
	// dispatcher resolves a default (current dir, --project lookup, or
	// sesh worker-cwd <session>).
	Cwd string `yaml:"cwd,omitempty" json:"cwd,omitempty"`

	// Owner is the operator handle (typically $USER). Used in subject
	// tokens (agents.prompt.cc.<owner>.<session>) and registry filters.
	Owner string `yaml:"owner,omitempty" json:"owner,omitempty"`

	// Labels are arbitrary key/value metadata. Surfaced in $SRV.INFO
	// for filtering; otherwise inert.
	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`

	// Outfit, when set, applies a suit-prepare bundle. Either shorthand
	// (Bundle: "backend/executing+pr-policy") or explicit
	// (Name/Cut/Accessories) form.
	Outfit *OutfitBlock `yaml:"outfit,omitempty" json:"outfit,omitempty"`

	// Env are environment variables passed to the worker process. Keys
	// MUST match [A-Z_][A-Z0-9_]*; values are passed verbatim.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// ─── Executor discriminator: exactly one block ──────────────────
	// Validator enforces XOR via the "executor_xor" struct-level rule.

	Tmux             *TmuxBlock        `yaml:"tmux,omitempty" json:"tmux,omitempty"`
	CFWorker         *CFWorkerBlock    `yaml:"cf-worker,omitempty" json:"cf-worker,omitempty"`
	CFDurableObject  *CFDurableBlock   `yaml:"cf-durable-object,omitempty" json:"cf-durable-object,omitempty"`
}

// OutfitBlock describes how to dress the worker. Operators may pass
// the Bundle shorthand ("name/cut+accessory1+accessory2") OR the
// explicit Name/Cut/Accessories trio — not both.
type OutfitBlock struct {
	Bundle      string   `yaml:"bundle,omitempty" json:"bundle,omitempty"`
	Name        string   `yaml:"name,omitempty" json:"name,omitempty"`
	Cut         string   `yaml:"cut,omitempty" json:"cut,omitempty"`
	Accessories []string `yaml:"accessories,omitempty" json:"accessories,omitempty"`
}

// TmuxBlock is the executor block for local tmux spawns. Today's
// default and only fully-implemented backend.
type TmuxBlock struct {
	// Headless detaches the spawn into the `orch-headless` session
	// instead of splitting the current pane. The worker runs
	// identically; it's just not visible until promoted with orch-show.
	Headless bool `yaml:"headless,omitempty" json:"headless,omitempty"`

	// Verify polls the new pane for either a known agent banner or a
	// title-rename signal before declaring success. See orch-spawn
	// --verify docs.
	Verify bool `yaml:"verify,omitempty" json:"verify,omitempty"`

	// Layout selects a layout-orchestrator preset. Empty defers to
	// orch's default split behavior.
	Layout string `yaml:"layout,omitempty" json:"layout,omitempty" validate:"omitempty,oneof=default grid full"`

	// Position is where to split off the current pane (headed mode).
	// One of right/left/above/below. Empty defaults to right.
	Position string `yaml:"position,omitempty" json:"position,omitempty" validate:"omitempty,oneof=right left above below"`

	// Role tags the pane as worker or observer. Empty defers to the
	// orch-spawn auto-detect (observer-class outfits/cuts → observer).
	Role string `yaml:"role,omitempty" json:"role,omitempty" validate:"omitempty,oneof=worker observer"`

	// NoShim disables the orch-agent-shim sidecar. Useful for
	// diagnostics or agents without a loaded adapter.
	NoShim bool `yaml:"no_shim,omitempty" json:"no_shim,omitempty"`
}

// CFWorkerBlock is the executor block for Cloudflare Worker spawns.
// Worker provisioning is async; the dispatcher returns a WorkerHandle
// with Status=pending and the operator polls until ready or failed.
type CFWorkerBlock struct {
	// Script is the path to the worker entrypoint (TS or JS) relative
	// to the executor's wrangler root.
	Script string `yaml:"script" json:"script" validate:"required"`

	// WranglerEnv selects a wrangler environment (e.g. "production",
	// "staging"). Empty uses the default environment.
	WranglerEnv string `yaml:"wrangler_env,omitempty" json:"wrangler_env,omitempty"`

	// AbortEndpoint is the worker route to POST to for graceful
	// shutdown (analogous to tmux send-keys C-c). Empty disables
	// imperative abort.
	AbortEndpoint string `yaml:"abort_endpoint,omitempty" json:"abort_endpoint,omitempty"`
}

// CFDurableBlock is the executor block for Cloudflare Durable Object
// spawns. Persistent open-agent bridge — the DO instance is the worker.
type CFDurableBlock struct {
	// DONamespace is the wrangler binding name for the DO namespace
	// (e.g. ORCH_WORKERS).
	DONamespace string `yaml:"do_namespace" json:"do_namespace" validate:"required"`

	// DOID is the durable-object id-from-name. Stable per worker.
	DOID string `yaml:"do_id" json:"do_id" validate:"required"`
}

// WorkerHandle is the backend → dispatcher response: "this is what I
// spawned." Persisted at ~/.cache/orch-spawn/<name>.handle.yaml (in a
// later proposal); for now produced on stdout for the dispatcher to
// consume.
type WorkerHandle struct {
	// SpecVersion matches SpawnSpec.SpecVersion. Same validation rule.
	SpecVersion string `yaml:"spec_version,omitempty" json:"spec_version,omitempty" jsonschema:"enum=v1"`

	// Name echoes SpawnSpec.Name.
	Name string `yaml:"name" json:"name" validate:"required,dns_label"`

	// Agent echoes SpawnSpec.Agent.
	Agent Agent `yaml:"agent" json:"agent" validate:"required,agent"`

	// Session echoes SpawnSpec.Session (or the resolved fallback).
	Session string `yaml:"session,omitempty" json:"session,omitempty"`

	// CreatedAt is when the backend produced this handle. Used to
	// detect stale handles in the registry.
	CreatedAt time.Time `yaml:"created_at" json:"created_at" validate:"required"`

	// Executor records which discriminator block from the spec was
	// chosen. One of: tmux | cf-worker | cf-durable-object.
	Executor string `yaml:"executor" json:"executor" validate:"required,oneof=tmux cf-worker cf-durable-object"`

	// PaneID is the tmux pane id for executor=tmux (e.g. "%64"). Empty
	// for non-tmux backends — they use ID instead.
	PaneID string `yaml:"pane_id,omitempty" json:"pane_id,omitempty"`

	// ID is the executor-generic worker id (DO id, Worker route, etc.).
	// Backends that have a meaningful generic id populate this; tmux
	// populates PaneID instead.
	ID string `yaml:"id,omitempty" json:"id,omitempty"`

	// Bus is the set of NATS subjects this worker reads/writes.
	Bus *BusBlock `yaml:"bus,omitempty" json:"bus,omitempty"`

	// Abort is the imperative cancellation verb. Same role as Archon's
	// per-node abort semantics.
	Abort *AbortBlock `yaml:"abort,omitempty" json:"abort,omitempty"`

	// LogFile is the path to the shim's startup log. Diagnostic only.
	LogFile string `yaml:"log_file,omitempty" json:"log_file,omitempty"`

	// PID is the worker process id when knowable. Best-effort.
	PID int `yaml:"pid,omitempty" json:"pid,omitempty"`

	// Status is the lifecycle phase at handle-emit time. Async-
	// provisioning backends (CF Worker) emit pending; the operator
	// polls until ready or failed.
	Status string `yaml:"status" json:"status" validate:"required,oneof=pending ready failed"`

	// Message is populated when Status=failed (human-readable cause)
	// or Status=pending (what we're waiting on).
	Message string `yaml:"message,omitempty" json:"message,omitempty"`
}

// BusBlock is the per-worker NATS subject map. Subjects follow the
// shim's 5-token convention: <prefix>.<adapter-tag>.<owner>.<session>.
type BusBlock struct {
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	Status string `yaml:"status,omitempty" json:"status,omitempty"`
	HB     string `yaml:"hb,omitempty" json:"hb,omitempty"`
	Signal string `yaml:"signal,omitempty" json:"signal,omitempty"`
}

// AbortBlock describes how to cancel this worker. Kind is the verb;
// Target/Keys are kind-specific payload.
type AbortBlock struct {
	// Kind is the abort mechanism. tmux-send-keys for tmux panes,
	// http-post for CF Workers, do-call for DOs.
	Kind string `yaml:"kind" json:"kind" validate:"required,oneof=tmux-send-keys http-post do-call"`

	// Target identifies the recipient (pane id, URL, DO id). Required
	// for all kinds.
	Target string `yaml:"target" json:"target" validate:"required"`

	// Keys are the keystrokes for kind=tmux-send-keys (e.g. "C-c").
	Keys string `yaml:"keys,omitempty" json:"keys,omitempty"`
}
