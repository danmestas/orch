// Package registry is the operator-side worker registry.
//
// Per ADR-0003, $SRV.INFO.agents on NATS is the single source of truth
// for which agents are alive and what they advertise. Everything else
// the registry consults — the operator's alias file, the legacy
// operator-claim file, the per-shim heartbeat stream — is an overlay
// applied during Snapshot, not a sibling source:
//
//	NATS $SRV.INFO.agents        single source of truth (per ADR-0003)
//	agents.hb.>                  liveness overlay (heartbeat freshness)
//	~/.config/orch-aliases       display-name overlay (operator override)
//	~/.cache/orch-operator.json  legacy operator-role overlay (back-compat)
//	~/.cache/orch-shim/*.log     diagnostic-only, not consumed by Snapshot
//
// Consumers ask the registry one question — Snapshot, Lookup, Watch —
// and get a joined view. The registry is the JOIN; the source of truth
// stays on the bus.
//
// The four reader interfaces (AgentReader, HeartbeatReader, AliasReader,
// OperatorReader) are seam points for testing — they let tests inject
// deterministic data without standing up a NATS server or fixturing
// files on disk. In production, NewNATSReader, NewAliasReader, and
// NewOperatorReader produce the concrete implementations.
//
// See docs/proposals/0005-operator-registry-consolidation.md.
package registry

import (
	"context"
	"time"
)

// DefaultHeartbeatWindow is how long after the last heartbeat we consider
// a worker alive. The shim defaults to 30s heartbeats; 3× gives one missed
// beat of slack before flipping Alive=false. Proposal §"Decisions deferred",
// item 4.
const DefaultHeartbeatWindow = 90 * time.Second

// Worker is the joined view of one agent instance. Identity fields are
// stable for the lifetime of the instance; Lifecycle fields update as
// heartbeats arrive.
type Worker struct {
	// Identity — immutable per instance.
	PaneID     string // raw tmux pane id, e.g. "%64"
	InstanceID string // micro service instance id from $SRV.INFO

	// Subjects — bus addresses for talking to this worker.
	Subjects Subjects

	// Display — what to call this worker in UIs / CLIs.
	Name   string // alias-file name > metadata.session > pct-encoded pane fallback
	Role   string // metadata.role ("worker", "operator", "observer", …)
	Outfit string // metadata.outfit

	// Lifecycle.
	Agent   string    // metadata.agent ("claude-code" / "codex" / "pi" / "gemini")
	CWD     string    // metadata.cwd
	Owner   string    // metadata.owner
	Session string    // metadata.session (may be empty)
	LastHB  time.Time // zero when no heartbeat has been observed yet
	Alive   bool      // last heartbeat within HeartbeatWindow, OR registered without HB yet

	// Raw inputs preserved for callers that need fields the registry
	// hasn't promoted to first-class.
	Metadata map[string]string
}

// Subjects collects the per-worker NATS subjects so consumers do not need
// to reconstruct them from token/owner/session.
type Subjects struct {
	Prompt string // §2.3 prompt endpoint subject
	Status string // §2.3 status endpoint subject
	HB     string // agents.hb.<token>.<owner>.<session-or-pane>
}

// EventType discriminates Joined / Updated / Departed.
type EventType int

const (
	Joined EventType = iota
	Updated
	Departed
)

func (e EventType) String() string {
	switch e {
	case Joined:
		return "joined"
	case Updated:
		return "updated"
	case Departed:
		return "departed"
	default:
		return "unknown"
	}
}

// Event is one delta on the worker set. Departed events carry the last-
// known Worker so consumers can clean up by pane id without an extra lookup.
type Event struct {
	Type      EventType
	Worker    Worker
	Timestamp time.Time
}

// Registry is the read interface. Implementations are responsible for
// keeping the joined view fresh.
//
// Snapshot returns all currently-known workers (alive and not-yet-departed).
// Lookup resolves by alias name OR raw pane id (case-sensitive); the
// caller does not have to know which it has.
// Watch returns a buffered channel of events. The channel closes when ctx
// is cancelled or the registry shuts down.
type Registry interface {
	Snapshot() []Worker
	Lookup(nameOrPane string) (Worker, bool)
	Watch(ctx context.Context) <-chan Event
	Close() error
}
