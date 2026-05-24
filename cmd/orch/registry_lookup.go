package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
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
//
// Returns (nil, nil) when ORCH_REGISTRY_FIXTURE_FILE is set — callers
// detect that and route through fixture-loading instead of dialling.
// The env var is integration-test plumbing; production code never sets it.
func connectNATS(overrideURL string, clientName string) (*nats.Conn, error) {
	if os.Getenv("ORCH_REGISTRY_FIXTURE_FILE") != "" {
		return nil, nil
	}
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
//
// When ORCH_REGISTRY_FIXTURE_FILE is set (integration-test plumbing) the
// NATS path is bypassed and the worker list is read from a JSON array
// at that path — same shape as `orch-registry snapshot` produces. nc
// may be nil in that mode.
func snapshotOnce(ctx context.Context, nc *nats.Conn) ([]registry.Worker, error) {
	if path := os.Getenv("ORCH_REGISTRY_FIXTURE_FILE"); path != "" {
		return loadFixtureWorkers(path)
	}
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

// loadFixtureWorkers reads a JSON array of worker objects from path. The
// JSON shape matches `orch-registry snapshot` (cmd/orch-registry/main.go
// jsonWorker), with optional fields treated as empty when absent. This
// is the test seam that replaces the per-PATH stub `orch-registry`
// binary the bash integration tests used to install.
func loadFixtureWorkers(path string) ([]registry.Worker, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", path, err)
	}
	var rows []struct {
		PaneID     string            `json:"pane_id"`
		InstanceID string            `json:"instance_id"`
		Name       string            `json:"name"`
		Role       string            `json:"role"`
		Outfit     string            `json:"outfit"`
		Agent      string            `json:"agent"`
		CWD        string            `json:"cwd"`
		Owner      string            `json:"owner"`
		Session    string            `json:"session"`
		Alive      bool              `json:"alive"`
		Subjects   struct {
			Prompt string `json:"prompt"`
			Status string `json:"status"`
			HB     string `json:"hb"`
		} `json:"subjects"`
		Metadata map[string]string `json:"metadata"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return nil, fmt.Errorf("parse fixture %s: %w", path, err)
	}
	out := make([]registry.Worker, 0, len(rows))
	for _, r := range rows {
		role := r.Role
		if role == "" {
			role = "worker"
		}
		name := r.Name
		if name == "" {
			// Match the precedence the registry's join.go promotes:
			// session beats the pct-form fallback. The Name field is
			// what Lookup matches on, so we synthesise it from the
			// session label when the fixture leaves it blank.
			if r.Session != "" {
				name = r.Session
			} else if r.PaneID != "" {
				name = "pct" + r.PaneID[1:]
			}
		}
		out = append(out, registry.Worker{
			PaneID:     r.PaneID,
			InstanceID: r.InstanceID,
			Name:       name,
			Role:       role,
			Outfit:     r.Outfit,
			Agent:      r.Agent,
			CWD:        r.CWD,
			Owner:      r.Owner,
			Session:    r.Session,
			Alive:      r.Alive,
			Subjects: registry.Subjects{
				Prompt: r.Subjects.Prompt,
				Status: r.Subjects.Status,
				HB:     r.Subjects.HB,
			},
			Metadata: r.Metadata,
		})
	}
	return out, nil
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
