package spawnspec

import "time"

// SpawnSpec v2 — the cmux + zmx + layout=none addition.
//
// Per docs/spawn-spec-versioning.md, opening the Executor enum is a
// wire-format change → version bump. v2 is purely additive:
//
//   - Executor enum gains `cmux` and `zmx`.
//   - Two new optional discriminator blocks: CmuxBlock, ZmxBlock.
//   - TmuxBlock's layout enum gains `none` (composition table valid
//     pair {executor=zmx, layout=none}).
//
// v1 (types.go) stays IMMUTABLE — the v1 reflector input is unchanged
// so dist/schema/spawn-spec.v1.json remains byte-identical. v2 ships
// its own struct family + its own published schemas
// (dist/schema/spawn-spec.v2.json, dist/schema/worker-handle.v2.json).
//
// Parser dispatch: UnmarshalSpec peeks `spec_version:` and routes to
// the v1 or v2 struct family. Both versions are accepted indefinitely
// per the versioning doc's "no silent breakage" policy.

// SpecVersionV1 is the v1 contract version literal. Kept as a named
// constant for the version-aware parser; SpecVersion (types.go) is
// the canonical v1 reference and remains the default emitted by
// MarshalSpec for backward compatibility.
const SpecVersionV1 = "v1"

// SpecVersionV2 is the v2 contract version literal. Set
// `spec_version: v2` on a YAML document to opt in to cmux / zmx /
// layout=none.
const SpecVersionV2 = "v2"

// SpawnSpecV2 is the v2 dispatcher → backend message. Mirrors
// SpawnSpec (v1) field-for-field with two added discriminator blocks
// (Cmux, Zmx). The TmuxBlock here is the v2 variant (TmuxBlockV2)
// whose layout enum accepts `none`.
//
// Exactly one of Tmux / CFWorker / CFDurableObject / Cmux / Zmx MUST
// be set. The XOR rule grows from 3-way to 5-way; same semantics.
type SpawnSpecV2 struct {
	SpecVersion string `yaml:"spec_version,omitempty" json:"spec_version,omitempty" jsonschema:"enum=v2,description=Wire contract version. Must be v2 for cmux/zmx/layout=none."`

	Name string `yaml:"name" json:"name" validate:"required,dns_label"`

	Description string `yaml:"description,omitempty" json:"description,omitempty" jsonschema:"description=Free-form operator notes. Not consumed by dispatcher or backends."`

	Agent Agent `yaml:"agent" json:"agent" validate:"required,agent"`

	Session string `yaml:"session,omitempty" json:"session,omitempty" jsonschema:"description=Sesh session label and 5th subject token. Defaults to Name when omitted."`

	Cwd string `yaml:"cwd,omitempty" json:"cwd,omitempty" jsonschema:"description=Worker working directory. Empty defers to dispatcher resolution (current dir / --project / sesh worker-cwd)."`

	Owner string `yaml:"owner,omitempty" json:"owner,omitempty" jsonschema:"description=Operator handle used in subject tokens and registry filters. Defaults to $USER."`

	Labels map[string]string `yaml:"labels,omitempty" json:"labels,omitempty" jsonschema:"description=Arbitrary key/value metadata surfaced in $SRV.INFO. Empty by default; otherwise inert."`

	Outfit *OutfitBlock `yaml:"outfit,omitempty" json:"outfit,omitempty" jsonschema:"description=Suit-prepare bundle. Omit to skip outfit application."`

	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty" jsonschema:"description=Env vars passed to the worker. Keys must match [A-Z_][A-Z0-9_]*. Empty by default."`

	// ─── Executor discriminator: exactly one block ──────────────────
	// Validator enforces XOR via the "executor_xor" struct-level rule.
	// v2 grows the closed set from {tmux, cf-worker, cf-durable-object}
	// to {tmux, cf-worker, cf-durable-object, cmux, zmx}.

	Tmux            *TmuxBlockV2     `yaml:"tmux,omitempty" json:"tmux,omitempty" jsonschema:"description=Local tmux executor block. Set exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx."`
	CFWorker        *CFWorkerBlock   `yaml:"cf-worker,omitempty" json:"cf-worker,omitempty" jsonschema:"description=Cloudflare Worker executor block. Set exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx."`
	CFDurableObject *CFDurableBlock  `yaml:"cf-durable-object,omitempty" json:"cf-durable-object,omitempty" jsonschema:"description=Cloudflare Durable Object executor block. Set exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx."`
	Cmux            *CmuxBlock       `yaml:"cmux,omitempty" json:"cmux,omitempty" jsonschema:"description=cmux executor block (surface-based multiplexer). Set exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx."`
	Zmx             *ZmxBlock        `yaml:"zmx,omitempty" json:"zmx,omitempty" jsonschema:"description=zmx executor block (sessions-only, no in-session layout). Set exactly one of tmux / cf-worker / cf-durable-object / cmux / zmx."`
}

// TmuxBlockV2 is the v2 variant of TmuxBlock. Identical to v1 except
// the layout enum tag accepts `none` as a fifth value. v1's
// TmuxBlock stays unchanged so dist/schema/spawn-spec.v1.json keeps
// the original four-value enum.
type TmuxBlockV2 struct {
	Headless bool   `yaml:"headless,omitempty" json:"headless,omitempty" jsonschema:"description=Detach into the orch-headless session instead of splitting the current pane. Defaults to false."`
	Verify   bool   `yaml:"verify,omitempty" json:"verify,omitempty" jsonschema:"description=Poll the new pane for an agent banner before declaring success. Defaults to false."`
	Layout   string `yaml:"layout,omitempty" json:"layout,omitempty" validate:"omitempty,oneof=default grid full none" jsonschema:"description=Layout-orchestrator preset (default|grid|full|none). Empty defers to orch's default split behavior. 'none' is only valid paired with executor=zmx (Proposal 0008 composition table)."`
	Position string `yaml:"position,omitempty" json:"position,omitempty" validate:"omitempty,oneof=right left above below" jsonschema:"description=Split direction off the current pane (right|left|above|below). Defaults to right."`
	Role     string `yaml:"role,omitempty" json:"role,omitempty" validate:"omitempty,oneof=worker observer" jsonschema:"description=Pane role tag (worker|observer). Empty defers to orch-spawn auto-detect."`
	NoShim   bool   `yaml:"no_shim,omitempty" json:"no_shim,omitempty" jsonschema:"description=Disable the orch-agent-shim sidecar. Defaults to false (shim attached)."`
}

// CmuxBlock is the executor block for cmux surface spawns. Mirrors
// TmuxBlockV2 — cmux's spawn surface is shaped close enough to tmux's
// (position/headless/verify all have natural cmux mappings; layout is
// reserved for future multi-pane cmux work) that the block shape is
// parallel.
//
// cmux engine constraint: --headless is currently rejected by the
// engine (cmux has no headless-session concept). The validator does
// not pre-emptively flag headless+cmux — that would duplicate the
// engine-side error path; we let the engine surface the precise
// guidance when the operator actually spawns.
type CmuxBlock struct {
	Headless bool   `yaml:"headless,omitempty" json:"headless,omitempty" jsonschema:"description=Engine-rejected for cmux today (cmux has no headless session). Reserved for future use."`
	Verify   bool   `yaml:"verify,omitempty" json:"verify,omitempty" jsonschema:"description=Poll the new surface for an agent banner before declaring success. Defaults to false."`
	Position string `yaml:"position,omitempty" json:"position,omitempty" validate:"omitempty,oneof=right left above below" jsonschema:"description=Split direction off the current surface (right|left|above|below). Defaults to right; mapped onto cmux's --direction internally."`
	Role     string `yaml:"role,omitempty" json:"role,omitempty" validate:"omitempty,oneof=worker observer" jsonschema:"description=Surface role tag (worker|observer). Empty defers to orch-spawn auto-detect."`
	NoShim   bool   `yaml:"no_shim,omitempty" json:"no_shim,omitempty" jsonschema:"description=Disable the orch-agent-shim sidecar. Defaults to false (shim attached)."`
}

// ZmxBlock is the executor block for zmx session spawns. zmx is
// sessions-only — no panes, no splits, no in-session layout. The
// composition table pairs zmx with layout=none (the no-op layout
// surface); a tmux block's `layout: none` value is rejected by the
// validator unless executor=zmx.
//
// SessionName overrides the auto-derived zmx session name. When
// empty, the engine derives the session name from spec.Name (the
// slug — orch's slug regex is a strict subset of what zmx accepts).
type ZmxBlock struct {
	SessionName string `yaml:"session_name,omitempty" json:"session_name,omitempty" jsonschema:"description=zmx session name. Empty defers to engine: derives the name from SpawnSpec.Name (slug)."`
	Headless    bool   `yaml:"headless,omitempty" json:"headless,omitempty" jsonschema:"description=Map to 'zmx run -d' (detached-by-design). Defaults to true on zmx (the engine treats every spawn as detached)."`
	Verify      bool   `yaml:"verify,omitempty" json:"verify,omitempty" jsonschema:"description=Poll zmx history for the agent's banner before declaring success. Defaults to false."`
	Role        string `yaml:"role,omitempty" json:"role,omitempty" validate:"omitempty,oneof=worker observer" jsonschema:"description=Session role tag (worker|observer). Empty defers to orch-spawn auto-detect."`
	NoShim      bool   `yaml:"no_shim,omitempty" json:"no_shim,omitempty" jsonschema:"description=Disable the orch-agent-shim sidecar. Defaults to false (shim attached)."`
}

// WorkerHandleV2 is the v2 backend → dispatcher response. Mirrors
// WorkerHandle (v1) with one change: Executor enum grows from
// {tmux, cf-worker, cf-durable-object} to {tmux, cf-worker,
// cf-durable-object, cmux, zmx}. All other fields unchanged.
//
// PaneID continues to overload as the engine-native locator — pane
// id for tmux, surface ref for cmux, session name for zmx — matching
// the worker_killer.buildEngineHandle dispatch (internal/subtree).
type WorkerHandleV2 struct {
	SpecVersion string `yaml:"spec_version,omitempty" json:"spec_version,omitempty" jsonschema:"enum=v2,description=Wire contract version. Must be v2 for cmux/zmx executor values."`

	Name      string    `yaml:"name" json:"name" validate:"required,dns_label"`
	Agent     Agent     `yaml:"agent" json:"agent" validate:"required,agent"`
	Session   string    `yaml:"session,omitempty" json:"session,omitempty" jsonschema:"description=Echoes SpawnSpec.Session (or the resolved fallback). Empty if unset on both sides."`
	CreatedAt time.Time `yaml:"created_at" json:"created_at" validate:"required"`

	Executor string `yaml:"executor" json:"executor" validate:"required,oneof=tmux cf-worker cf-durable-object cmux zmx"`

	PaneID  string     `yaml:"pane_id,omitempty" json:"pane_id,omitempty" jsonschema:"description=Engine-native locator: tmux pane id, cmux surface ref, or zmx session name. Empty for executors that use the generic 'id' field instead."`
	ID      string     `yaml:"id,omitempty" json:"id,omitempty" jsonschema:"description=Executor-generic worker id (DO id, Worker route, etc.). Empty for executors that populate pane_id instead."`
	Bus     *BusBlock  `yaml:"bus,omitempty" json:"bus,omitempty" jsonschema:"description=Per-worker NATS subject map. Omit if the backend did not wire the bus."`
	Abort   *AbortBlock `yaml:"abort,omitempty" json:"abort,omitempty" jsonschema:"description=Imperative cancellation verb. Omit when imperative abort is not supported by the backend."`
	LogFile string     `yaml:"log_file,omitempty" json:"log_file,omitempty" jsonschema:"description=Path to the shim's startup log. Diagnostic only; empty when not produced."`
	PID     int        `yaml:"pid,omitempty" json:"pid,omitempty" jsonschema:"description=Worker process id when knowable. Best-effort; 0 when unknown."`
	Status  string     `yaml:"status" json:"status" validate:"required,oneof=pending ready failed"`
	Message string     `yaml:"message,omitempty" json:"message,omitempty" jsonschema:"description=Human-readable cause when status=failed; what we're waiting on when status=pending. Empty when status=ready."`
}
