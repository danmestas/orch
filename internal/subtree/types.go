package subtree

import (
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// SpecVersion is the topology yaml's contract version. Empty defaults
// here; mismatches are a parse-time error.
const SpecVersion = "v1"

// Topology is the parsed `.orch/subtrees/<name>.yaml` document — the
// declarative description of a subtree.
//
// Each subtree composes:
//
//   - one Sesh context (existing hub OR a spawned hub),
//   - N Worker entries (each a full SpawnSpec from Proposal 0002),
//   - optional State seeds (pass-through to sesh-ops; Ousterhout
//     adjustment 2026-05-18 — orch does NOT redefine sesh's schemas).
//
// Apply order is fixed by Proposal 0006's "apply semantics" section:
// Parse → Resolve sesh → Spawn workers → Seed state → Persist. The
// ordering is part of the public interface, not an implementation
// detail.
type Topology struct {
	SpecVersion string `yaml:"spec_version,omitempty"`

	// Name is the subtree identifier. Used as the cache filename
	// (~/.cache/orch-subtrees/<name>.applied.yaml), the sesh session
	// label when sesh.spawn is set, and the human-facing handle for
	// status/destroy/diff/list. DNS-label-shaped (same rule as
	// SpawnSpec.Name) so it round-trips through filesystems and NATS
	// subjects.
	Name string `yaml:"name"`

	// Description is free-form operator notes. Not interpreted.
	Description string `yaml:"description,omitempty"`

	// Sesh is the subtree's bus context. Required; the XOR
	// discriminator (Existing vs Spawn) is enforced by the validator.
	Sesh SeshSection `yaml:"sesh"`

	// Workers is the desired fleet. Empty is legal (a "state-only"
	// subtree that seeds tasks/goals without spawning processes), but
	// surfaces a warning at apply time so operators notice.
	Workers []WorkerEntry `yaml:"workers,omitempty"`

	// State is the optional seed-state section. Thin pass-through to
	// sesh-ops; orch does NOT redefine the task/goal schemas. See
	// state.go for the per-entry shape.
	State StateSection `yaml:"state,omitempty"`

	// Labels are cross-cutting metadata. Surfaced in `list` /
	// `status`; otherwise inert.
	Labels map[string]string `yaml:"labels,omitempty"`

	// SourceLine records the YAML node line of the document root for
	// anchoring "missing required field" diagnostics that have no
	// narrower position.
	SourceLine int `yaml:"-"`
}

// SeshSection is the sesh-context discriminator: exactly one of
// Existing (join a hub by URL) or Spawn (bring up a fresh hub) is set.
//
// The XOR is enforced by Validate (see validate.go) so callers
// downstream of the validator can safely dispatch on which field is
// non-zero.
type SeshSection struct {
	// Existing is a NATS URL to an already-running sesh hub. May
	// contain $ENV-VAR references (literal `$NAME`) which the parser
	// resolves against the operator's environment.
	Existing string `yaml:"existing,omitempty"`

	// Spawn, when set, instructs apply to bring up a fresh sesh hub
	// dedicated to this subtree.
	Spawn *SeshSpawn `yaml:"spawn,omitempty"`
}

// SeshSpawn describes a fresh sesh hub for a subtree. The shape mirrors
// `sesh up --session=<session> --scope=<scope> [--cwd=<cwd>]`.
type SeshSpawn struct {
	// Session is the sesh session label. If empty, defaults to the
	// parent topology's Name at validate time.
	Session string `yaml:"session,omitempty"`

	// Scope is one of "session" or "project". Empty defaults to
	// "session".
	Scope string `yaml:"scope,omitempty"`

	// Cwd is the working directory for the hub. Empty inherits the
	// operator's cwd at apply time.
	Cwd string `yaml:"cwd,omitempty"`
}

// WorkerEntry inlines a SpawnSpec (Proposal 0002). The embedded type
// uses yaml inline so a topology's `workers:` entries look identical
// to standalone spawnspec yaml — operators don't need to learn a new
// shape.
//
// SpecVersion on the embedded spec is auto-filled to spawnspec's
// SpecVersion during parse; operators don't repeat it per worker.
type WorkerEntry struct {
	spawnspec.SpawnSpec `yaml:",inline"`

	// SourceLine is the YAML source line of the worker mapping. Used
	// only by diagnostics.
	SourceLine int `yaml:"-"`
}

// StateSection is the seed-state pass-through to sesh-ops. Each
// entry's shape matches the sesh-ops command's accepted input — orch
// does NOT redefine the schema (Ousterhout-review adjustment
// 2026-05-18). If sesh-ops's input changes, this section follows
// automatically because the validator only enforces orch-side
// invariants (scope-id presence) and forwards the rest opaquely.
type StateSection struct {
	// Tasks is a list of sesh-ops-task-add payloads.
	Tasks []TaskSeed `yaml:"tasks,omitempty"`

	// Goals is a list of sesh-ops-goal-create payloads.
	Goals []GoalSeed `yaml:"goals,omitempty"`
}

// TaskSeed is the orch-side view of a `sesh-ops task add` payload.
// Fields are intentionally a thin pass-through; sesh-ops owns the full
// schema. Only the orch-required wiring (scope, scope-id, title) is
// validated here.
type TaskSeed struct {
	Scope       string                 `yaml:"scope"`
	ScopeID     string                 `yaml:"scope-id"`
	Title       string                 `yaml:"title"`
	DependsOn   []string               `yaml:"depends_on,omitempty"`
	MaxAttempts int                    `yaml:"max_attempts,omitempty"`
	Metadata    map[string]any `yaml:"metadata,omitempty"`

	// Extra preserves any sesh-ops-known fields orch doesn't promote
	// to first-class. Forwarded verbatim to sesh-ops.
	Extra map[string]any `yaml:",inline"`
}

// GoalSeed is the orch-side view of a `sesh-ops goal create` payload.
// Same pass-through rationale as TaskSeed.
type GoalSeed struct {
	Scope        string                 `yaml:"scope"`
	ScopeID      string                 `yaml:"scope-id"`
	Objective    string                 `yaml:"objective"`
	BudgetTokens int            `yaml:"budget_tokens,omitempty"`
	Metadata     map[string]any `yaml:"metadata,omitempty"`
	Extra        map[string]any `yaml:",inline"`
}

// AppliedSubtree is the persisted record of a successful apply. Stored
// at ~/.cache/orch-subtrees/<name>.applied.yaml so `diff` / `destroy`
// know what to compare against / tear down later.
//
// The applied.yaml is intentionally local: the topology yaml is the
// canonical source (committed to a repo). applied.yaml is a cache of
// last-apply state — losing it triggers `orch subtree adopt` to
// reconstruct from live bus state.
type AppliedSubtree struct {
	SpecVersion string `yaml:"spec_version"`

	// Name matches Topology.Name.
	Name string `yaml:"name"`

	// AppliedAt is when phase 5 (Persist) ran successfully.
	AppliedAt time.Time `yaml:"applied_at"`

	// Topology is the resolved topology (env-vars substituted, defaults
	// applied). Embedded so destroy/diff/status don't need to re-read
	// the source yaml.
	Topology Topology `yaml:"topology"`

	// ResolvedNATS is the actual NATS URL the workers attached to
	// (resolved $ENV in Sesh.Existing, or the URL the spawned hub
	// reported).
	ResolvedNATS string `yaml:"resolved_nats"`

	// Workers maps WorkerEntry.Name to the WorkerHandle the
	// dispatcher returned. Used by destroy to find pane ids and abort
	// verbs. Populated only when spawnspec returns a WorkerHandle
	// (legacy spawn paths leave it empty and destroy falls back to
	// live bus discovery).
	Workers map[string]*spawnspec.WorkerHandle `yaml:"workers,omitempty"`
}
