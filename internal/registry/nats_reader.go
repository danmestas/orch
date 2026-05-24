package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
)

// NATSReader is the live bus-side data fetcher. It satisfies AgentReader by
// querying $SRV.INFO.agents and HeartbeatReader by tracking agents.hb.>
// when started.
//
// ADR-0003 makes $SRV.INFO.agents the single source of truth; this reader
// is the implementation of that. Aliases and operator-role overlays are
// not "sources" in the same sense — they're operator-side enrichments
// applied during Snapshot, surfaced via the AliasReader / OperatorReader
// interfaces for test injection.
//
// One-shot (no Run): call Agents() to query $SRV.INFO once.
// Live (Run): also subscribes to agents.hb.> and maintains a heartbeat
// table queryable via Heartbeats().
type NATSReader struct {
	nc               *nats.Conn
	discoveryTimeout time.Duration
	maxDiscoveryWait time.Duration

	mu sync.RWMutex
	hb map[string]time.Time // pane_id → last-seen
}

// NATSOptions configures the NATS reader. Zero values pick sensible
// defaults (2s per-reply, 3s overall ceiling, no idle cap).
type NATSOptions struct {
	// DiscoveryTimeout is the per-reply timeout for $SRV.INFO.agents.
	// We collect replies until DiscoveryTimeout has elapsed since the
	// last reply (or MaxDiscoveryWait has elapsed in total).
	DiscoveryTimeout time.Duration
	// MaxDiscoveryWait caps total discovery time even when replies are
	// streaming in. Prevents a chatty bus from blocking forever.
	MaxDiscoveryWait time.Duration
}

const (
	defaultDiscoveryTimeout = 2 * time.Second
	defaultMaxDiscoveryWait = 3 * time.Second

	subjAgentInfo  = "$SRV.INFO.agents"
	subjHeartbeats = "agents.hb.>"
)

// NewNATSReader constructs a NATSReader on the given connection. The
// connection's lifecycle is the caller's responsibility.
func NewNATSReader(nc *nats.Conn, opts NATSOptions) *NATSReader {
	if opts.DiscoveryTimeout <= 0 {
		opts.DiscoveryTimeout = defaultDiscoveryTimeout
	}
	if opts.MaxDiscoveryWait <= 0 {
		opts.MaxDiscoveryWait = defaultMaxDiscoveryWait
	}
	return &NATSReader{
		nc:               nc,
		discoveryTimeout: opts.DiscoveryTimeout,
		maxDiscoveryWait: opts.MaxDiscoveryWait,
		hb:               map[string]time.Time{},
	}
}

// Agents queries $SRV.INFO.agents and returns the joined replies as
// AgentInfo. Each shim instance replies once with its own metadata + the
// micro service endpoint list.
//
// Returns a non-nil error only on transport problems. An empty bus (no
// agents replying within the discovery window) returns an empty slice
// and a nil error — callers treat "no agents" as a normal operational
// state.
func (s *NATSReader) Agents(ctx context.Context) ([]AgentInfo, error) {
	if s.nc == nil {
		return nil, errors.New("nats reader: no connection")
	}

	inbox := nats.NewInbox()
	sub, err := s.nc.SubscribeSync(inbox)
	if err != nil {
		return nil, fmt.Errorf("subscribe inbox: %w", err)
	}
	defer sub.Unsubscribe() //nolint:errcheck // best-effort cleanup

	if err := s.nc.PublishRequest(subjAgentInfo, inbox, nil); err != nil {
		return nil, fmt.Errorf("request %s: %w", subjAgentInfo, err)
	}

	deadline := time.Now().Add(s.maxDiscoveryWait)
	var out []AgentInfo
	for {
		// Cap per-iteration wait at the smaller of DiscoveryTimeout and
		// time-left-in-MaxDiscoveryWait so the overall ceiling is honoured.
		left := time.Until(deadline)
		if left <= 0 {
			break
		}
		wait := min(s.discoveryTimeout, left)
		msg, err := sub.NextMsg(wait)
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) {
				// Quiet for a discovery-timeout window → assume no more
				// replies coming. This matches the nats CLI's
				// --replies=0 --reply-timeout semantics.
				break
			}
			if errors.Is(err, nats.ErrNoResponders) {
				// Empty bus — $SRV.INFO.agents has no subscribers, which
				// means zero agents registered. Not an error condition;
				// orch-peek on a quiet machine is the canonical case.
				break
			}
			return out, fmt.Errorf("recv: %w", err)
		}
		info, perr := parseAgentInfo(msg.Data)
		if perr != nil {
			// Skip malformed replies but keep collecting — a single bad
			// shim shouldn't poison the whole snapshot.
			continue
		}
		out = append(out, info)
	}
	return out, nil
}

// Heartbeats returns a snapshot of the most-recent heartbeat per pane.
// Returns an empty map (not error) when Run has not been called yet.
func (s *NATSReader) Heartbeats(ctx context.Context) (map[string]time.Time, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make(map[string]time.Time, len(s.hb))
	maps.Copy(out, s.hb)
	return out, nil
}

// Run subscribes to agents.hb.> and updates the heartbeat table until ctx
// is cancelled. Blocking — call in a goroutine.
func (s *NATSReader) Run(ctx context.Context) error {
	if s.nc == nil {
		return errors.New("nats reader: no connection")
	}
	sub, err := s.nc.Subscribe(subjHeartbeats, s.onHeartbeat)
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", subjHeartbeats, err)
	}
	<-ctx.Done()
	return sub.Unsubscribe()
}

// onHeartbeat updates the per-pane LastHB. The heartbeat body carries
// the full agent metadata (per §8.3); we read pane_id from there.
func (s *NATSReader) onHeartbeat(msg *nats.Msg) {
	pane := paneFromHeartbeat(msg)
	if pane == "" {
		return
	}
	s.mu.Lock()
	s.hb[pane] = time.Now()
	s.mu.Unlock()
}

// paneFromHeartbeat extracts the worker's pane id from the heartbeat
// message. Preference order:
//  1. JSON body's "pane_id" field (the shim's heartbeat payload).
//  2. Subject's 5th token decoded from pct-form back to "%N", as a
//     defensive fallback for shims that publish empty heartbeat bodies.
func paneFromHeartbeat(msg *nats.Msg) string {
	if len(msg.Data) > 0 {
		var body struct {
			PaneID string `json:"pane_id"`
		}
		if err := json.Unmarshal(msg.Data, &body); err == nil && body.PaneID != "" {
			return body.PaneID
		}
	}
	parts := strings.Split(msg.Subject, ".")
	if len(parts) >= 5 {
		if rest, ok := strings.CutPrefix(parts[4], "pct"); ok {
			return "%" + rest
		}
	}
	return ""
}

// parseAgentInfo decodes one $SRV.INFO.agents reply body into an AgentInfo.
//
// The Synadia micro framework's standard $SRV.INFO body shape is documented
// at https://docs.nats.io/nats-concepts/micro — relevant fields:
//
//	{
//	  "type":"io.nats.micro.v1.info_response",
//	  "id":"<instance-id>",
//	  "name":"agents",
//	  "version":"...",
//	  "metadata": { "pane_id":"%64", "role":"worker", ... },
//	  "endpoints": [ { "name":"prompt", "subject":"agents.prompt..." }, ... ]
//	}
func parseAgentInfo(data []byte) (AgentInfo, error) {
	var raw struct {
		ID        string            `json:"id"`
		Metadata  map[string]string `json:"metadata"`
		Endpoints []struct {
			Name    string `json:"name"`
			Subject string `json:"subject"`
		} `json:"endpoints"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return AgentInfo{}, err
	}
	eps := make([]EndpointInfo, 0, len(raw.Endpoints))
	for _, e := range raw.Endpoints {
		eps = append(eps, EndpointInfo{Name: e.Name, Subject: e.Subject})
	}
	if raw.Metadata == nil {
		raw.Metadata = map[string]string{}
	}
	return AgentInfo{
		InstanceID: raw.ID,
		Metadata:   raw.Metadata,
		Endpoints:  eps,
	}, nil
}
