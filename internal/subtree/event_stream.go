package subtree

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/nats-io/nats.go"
)

// NATSEventStream implements EventStream by subscribing to
// `agents.events.>` (status events from shims) and `agents.hb.>`
// (heartbeats). Events are filtered to the workers declared in the
// subtree's applied.yaml — bus traffic from other subtrees / orch's
// own pane is dropped before reaching the channel.
//
// The filter set is built at Subscribe-time from the cached
// AppliedSubtree. Newly-spawned workers (post-Subscribe) that the
// operator added with a fresh `apply` won't show up on an existing
// watch — re-running `orch subtree watch` after `apply` is the
// documented way to widen the filter.
type NATSEventStream struct {
	// NC is the NATS connection used for the subscriptions. The
	// connection's lifecycle is the caller's responsibility.
	NC *nats.Conn

	// Cache reads the applied.yaml so Subscribe can build the worker
	// name filter. Required.
	Cache CacheStore

	// ChannelBuffer sizes the output channel. Empty defaults to 64.
	// Larger buffers smooth burst traffic; smaller buffers exert
	// back-pressure earlier.
	ChannelBuffer int
}

// NewNATSEventStream constructs an event stream around an existing
// NATS connection + cache. The cache is normally
// NewFileCache(DefaultCacheDir()).
func NewNATSEventStream(nc *nats.Conn, cache CacheStore) *NATSEventStream {
	return &NATSEventStream{NC: nc, Cache: cache, ChannelBuffer: 64}
}

// Subscribe implements EventStream. Returns the event channel and
// closes it (cancelling the subscriptions) when ctx is cancelled.
func (s *NATSEventStream) Subscribe(ctx context.Context, subtreeName string) (<-chan WatchEvent, error) {
	if s == nil || s.NC == nil {
		return nil, fmt.Errorf("subtree watch: nil NATS connection")
	}
	if s.Cache == nil {
		return nil, fmt.Errorf("subtree watch: nil CacheStore")
	}
	applied, err := s.Cache.Read(subtreeName)
	if err != nil {
		return nil, err
	}

	// Build the membership set. We accept any token equality match
	// against worker name (instance_id), session, or pane id —
	// agents publish different leading tokens depending on which
	// adapter is active.
	wantNames := make(map[string]struct{}, len(applied.Topology.Workers))
	for _, w := range applied.Topology.Workers {
		wantNames[w.Name] = struct{}{}
		if w.Session != "" {
			wantNames[w.Session] = struct{}{}
		}
		if h := applied.Workers[w.Name]; h != nil && h.PaneID != "" {
			// Pane id is published in pct-form on heartbeats; record
			// both raw and pct variants for matching.
			wantNames[h.PaneID] = struct{}{}
			if pct := paneToPct(h.PaneID); pct != "" {
				wantNames[pct] = struct{}{}
			}
		}
	}

	buf := s.ChannelBuffer
	if buf <= 0 {
		buf = 64
	}
	out := make(chan WatchEvent, buf)

	var mu sync.Mutex
	closed := false
	closeOnce := func() {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return
		}
		close(out)
		closed = true
	}

	push := func(ev WatchEvent) {
		mu.Lock()
		if closed {
			mu.Unlock()
			return
		}
		mu.Unlock()
		select {
		case out <- ev:
		case <-ctx.Done():
		default:
			// Drop on full buffer — operator gets a stable rate
			// rather than memory pressure. Documented in the
			// EventStream interface contract.
		}
	}

	handler := func(kind string) nats.MsgHandler {
		return func(msg *nats.Msg) {
			worker := matchSubject(msg.Subject, wantNames)
			if worker == "" {
				return
			}
			push(WatchEvent{
				SubtreeName: subtreeName,
				Worker:      worker,
				Kind:        kind,
				Payload:     truncatePayload(msg.Data),
			})
		}
	}

	subEvents, err := s.NC.Subscribe("agents.events.>", handler("status"))
	if err != nil {
		return nil, fmt.Errorf("subtree watch: subscribe agents.events.>: %w", err)
	}
	subHB, err := s.NC.Subscribe("agents.hb.>", handler("hb"))
	if err != nil {
		_ = subEvents.Unsubscribe()
		return nil, fmt.Errorf("subtree watch: subscribe agents.hb.>: %w", err)
	}

	go func() {
		<-ctx.Done()
		_ = subEvents.Unsubscribe()
		_ = subHB.Unsubscribe()
		closeOnce()
	}()
	return out, nil
}

// matchSubject returns a worker-name match found in the subject's
// tokens, or "" if no token is in the filter set. Subject shape:
// `agents.<kind>.<adapter>.<owner>.<session-or-pane>[.tail...]`.
// We walk every token so additions to the convention (e.g. a future
// 6th-token tag) keep matching naturally.
func matchSubject(subject string, want map[string]struct{}) string {
	for _, tok := range strings.Split(subject, ".") {
		if tok == "" {
			continue
		}
		if _, ok := want[tok]; ok {
			return tok
		}
		// Heartbeats encode pane id as `pctNNN`; also try the raw
		// pane form (with leading %).
		if strings.HasPrefix(tok, "pct") {
			if _, ok := want["%"+tok[3:]]; ok {
				return "%" + tok[3:]
			}
		}
	}
	return ""
}

// paneToPct mirrors the shim's pct-encoding for pane ids in
// subjects (`%N` → `pctN`).
func paneToPct(pane string) string {
	if strings.HasPrefix(pane, "%") {
		return "pct" + pane[1:]
	}
	return ""
}

// truncatePayload trims an event payload to a sensible size for the
// CLI to print. NATS messages can carry kilobytes of payload; the
// watch UX prefers compact lines.
func truncatePayload(b []byte) string {
	const max = 512
	if len(b) <= max {
		return string(b)
	}
	// Try to keep the JSON parseable for downstream tooling — chop
	// at a record boundary if we can find one.
	cut := max
	for i := max - 1; i > max-64 && i > 0; i-- {
		if b[i] == ',' || b[i] == '}' || b[i] == ']' || b[i] == '\n' {
			cut = i + 1
			break
		}
	}
	return string(b[:cut]) + "...<truncated>"
}

// MarshalEvent is a small convenience for tests that need to compare
// payload-bearing events without depending on the raw byte slice
// representation.
func MarshalEvent(ev WatchEvent) string {
	b, _ := json.Marshal(ev)
	return string(b)
}
