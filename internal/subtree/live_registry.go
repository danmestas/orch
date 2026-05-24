package subtree

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/registry"
)

// NATSLiveRegistry implements LiveRegistry by querying $SRV.INFO.agents
// on a NATS connection. Worker identity is derived from
// metadata.instance_id when set (the stable slug published by the
// shim's --instance-id flag, Proposal 0009 / issue #181) and falls
// back to metadata.session for legacy spawns that predate the slug.
//
// The set we return is exactly the keys the apply pipeline compares
// against Topology.Workers[*].Name — orch-spawn maps SpawnSpec.Name
// → --instance-id → ORCH_INSTANCE_ID → metadata.instance_id, so the
// match is direct for any worker spawned through the spawnspec path.
type NATSLiveRegistry struct {
	// NC is the NATS connection used for the snapshot. Required; the
	// connection's lifecycle (Connect / Close) is the caller's job.
	NC *nats.Conn

	// DiscoveryTimeout is the per-reply timeout for $SRV.INFO.agents.
	// Empty defaults to 2s.
	DiscoveryTimeout time.Duration

	// MaxDiscoveryWait caps total discovery time. Empty defaults to 3s.
	MaxDiscoveryWait time.Duration
}

// AliveByName runs one $SRV.INFO.agents discovery round and returns
// the set of worker names currently registered on the bus.
//
// We collect BOTH metadata.instance_id (preferred — stable slug) and
// metadata.session (fallback) into the set. The apply pipeline does
// `_, exists := alive[w.Name]` so having both keys means a worker
// spawned with `--instance-id=X --sesh-session=Y` registers under
// either name without false-negative re-spawns.
func (r *NATSLiveRegistry) AliveByName(ctx context.Context) (map[string]struct{}, error) {
	if r == nil || r.NC == nil {
		return nil, fmt.Errorf("subtree live registry: no NATS connection")
	}
	src := registry.NewNATSReader(r.NC, registry.NATSOptions{
		DiscoveryTimeout: r.DiscoveryTimeout,
		MaxDiscoveryWait: r.MaxDiscoveryWait,
	})
	agents, err := src.Agents(ctx)
	if err != nil {
		return nil, fmt.Errorf("subtree live registry: $SRV.INFO.agents: %w", err)
	}
	out := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if id := a.Metadata["instance_id"]; id != "" {
			out[id] = struct{}{}
		}
		if sess := a.Metadata["session"]; sess != "" {
			out[sess] = struct{}{}
		}
	}
	return out, nil
}

// EmptyLiveRegistry returns a LiveRegistry that always reports an
// empty alive set. Used by `apply --no-skip-live` style flows where
// the operator explicitly wants every worker re-spawned (rare); also
// the safe default for unit tests of dependent code.
func EmptyLiveRegistry() LiveRegistry { return emptyRegistry{} }

type emptyRegistry struct{}

func (emptyRegistry) AliveByName(context.Context) (map[string]struct{}, error) {
	return map[string]struct{}{}, nil
}

// AgentsResult is exposed for callers that want richer registry data
// than the bare name-set (e.g. `status` rendering the agent kind).
// Kept separate so AliveByName stays a tight contract.
type AgentsResult struct {
	Agents []registry.AgentInfo
}

// AgentsSnapshot is a convenience for callers that want the raw
// agent list (status uses this to compute drift beyond name-set).
func (r *NATSLiveRegistry) AgentsSnapshot(ctx context.Context) (*AgentsResult, error) {
	if r == nil || r.NC == nil {
		return nil, fmt.Errorf("subtree live registry: no NATS connection")
	}
	src := registry.NewNATSReader(r.NC, registry.NATSOptions{
		DiscoveryTimeout: r.DiscoveryTimeout,
		MaxDiscoveryWait: r.MaxDiscoveryWait,
	})
	agents, err := src.Agents(ctx)
	if err != nil {
		return nil, err
	}
	return &AgentsResult{Agents: agents}, nil
}
