package main

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/registry"
	"github.com/danmestas/orch/internal/registry/sources"
	"github.com/danmestas/synadia-agent-shim/shim"
)

// snapshotTimeout bounds how long we wait for $SRV.INFO.agents replies
// when building the registry snapshot. The shim usually answers in <50ms;
// 5s gives slack for high-latency NATS connections without hanging tools
// indefinitely.
const snapshotTimeout = 5 * time.Second

// connectNATS dials the configured NATS URL using the shim's standard
// resolution order ($NATS_URL → ~/.sesh/hub.nats.url → ~/.sesh/hub.url →
// nats://127.0.0.1:4222). overrideURL wins when non-empty (used by
// --nats flags).
func connectNATS(overrideURL string, clientName string) (*nats.Conn, error) {
	url := shim.ReadNATSURL(overrideURL)
	nc, err := nats.Connect(url, nats.Name(clientName))
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", url, err)
	}
	return nc, nil
}

// snapshotOnce performs one registry snapshot read against the supplied
// NATS connection. The alias / operator readers default to the standard
// filesystem paths. Returns the joined Worker slice or a fatal error from
// the $SRV.INFO.agents read; non-fatal source errors are silently swallowed
// (the registry already tolerates them).
func snapshotOnce(ctx context.Context, nc *nats.Conn) ([]registry.Worker, error) {
	src := sources.New(nc, sources.NATSOptions{})
	readers := registry.Readers{
		Agents:     src,
		Heartbeats: src,
		Aliases:    sources.NewAliasFile(""),
		Operator:   sources.NewOperatorFile(""),
	}
	ctx, cancel := context.WithTimeout(ctx, snapshotTimeout)
	defer cancel()
	workers, errs := registry.Snapshot(ctx, readers, registry.DefaultHeartbeatWindow)
	if errs.HasFatal() {
		return nil, errs["agents"]
	}
	return workers, nil
}

// lookupTarget resolves a target string to a Worker. Accepts:
//   - "%nn"      → exact pane-id match
//   - "operator" → first worker with metadata.role == "operator"
//   - "op"       → alias for "operator"
//   - any other  → match by Name (alias > slug > session > pct fallback)
//                  or InstanceID (raw slug) — registry.Lookup semantics.
//
// Returns (Worker{}, false) on miss; the caller surfaces a useful error.
func lookupTarget(workers []registry.Worker, target string) (registry.Worker, bool) {
	if target == "operator" || target == "op" {
		for _, w := range workers {
			if w.Role == "operator" {
				return w, true
			}
		}
		return registry.Worker{}, false
	}
	if len(target) > 0 && target[0] == '%' {
		for _, w := range workers {
			if w.PaneID == target {
				return w, true
			}
		}
		return registry.Worker{}, false
	}
	// Name (alias > slug > session > pct) match first, then InstanceID.
	for _, w := range workers {
		if w.Name == target {
			return w, true
		}
	}
	for _, w := range workers {
		if w.InstanceID == target {
			return w, true
		}
	}
	// Some callers still know the worker by its session label even when
	// an alias shadows Name (mirrors orch-registry's lookup behaviour).
	for _, w := range workers {
		if w.Session != "" && w.Session == target {
			return w, true
		}
	}
	return registry.Worker{}, false
}
