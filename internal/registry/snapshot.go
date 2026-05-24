package registry

import (
	"context"
	"sync"
	"time"
)

// Readers bundles the four data fetchers the registry consults.
//
// Per ADR-0003 only Agents is the source of truth; Heartbeats, Aliases,
// and Operator are overlays applied during the join. All fields are
// optional — a nil reader contributes its identity element (empty
// slice / empty map / empty string) so Snapshot degrades gracefully
// when a reader is unavailable.
//
// In production code, callers build this with NewNATSReader (which
// satisfies both AgentReader and HeartbeatReader), NewAliasReader, and
// NewOperatorReader. Tests inject fakes for deterministic data.
type Readers struct {
	Agents     AgentReader
	Heartbeats HeartbeatReader
	Aliases    AliasReader
	Operator   OperatorReader
}

// AgentReader / HeartbeatReader / AliasReader / OperatorReader are the
// fetcher contracts Snapshot consumes. They exist so tests can mock the
// readers without dialing NATS or writing files.
type AgentReader interface {
	Agents(ctx context.Context) ([]AgentInfo, error)
}

type HeartbeatReader interface {
	Heartbeats(ctx context.Context) (map[string]time.Time, error)
}

type AliasReader interface {
	Aliases(ctx context.Context) (map[string]string, error)
}

type OperatorReader interface {
	OperatorPane(ctx context.Context) (string, error)
}

// Snapshot reads all configured sources concurrently, joins them, and
// returns the canonical Worker list. Source errors are surfaced via the
// returned Errors map (keyed by source name) but never abort the join —
// callers can choose to log, ignore, or fail depending on which sources
// matter for their use case.
//
// Use this for one-shot reads (CLI snapshot, JSON dump, target lookup).
// For continuous live tracking, wrap with Live (see live.go).
func Snapshot(ctx context.Context, r Readers, hbWindow time.Duration) ([]Worker, Errors) {
	type result struct {
		agents       []AgentInfo
		heartbeats   map[string]time.Time
		aliases      map[string]string
		operatorPane string
		errs         Errors
	}
	res := result{errs: Errors{}}
	var mu sync.Mutex
	var wg sync.WaitGroup

	record := func(src string, err error) {
		if err == nil {
			return
		}
		mu.Lock()
		res.errs[src] = err
		mu.Unlock()
	}

	wg.Add(4)
	go func() {
		defer wg.Done()
		if r.Agents == nil {
			return
		}
		a, err := r.Agents.Agents(ctx)
		record("agents", err)
		mu.Lock()
		res.agents = a
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		if r.Heartbeats == nil {
			return
		}
		hb, err := r.Heartbeats.Heartbeats(ctx)
		record("heartbeats", err)
		mu.Lock()
		res.heartbeats = hb
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		if r.Aliases == nil {
			return
		}
		al, err := r.Aliases.Aliases(ctx)
		record("aliases", err)
		mu.Lock()
		res.aliases = al
		mu.Unlock()
	}()
	go func() {
		defer wg.Done()
		if r.Operator == nil {
			return
		}
		op, err := r.Operator.OperatorPane(ctx)
		record("operator", err)
		mu.Lock()
		res.operatorPane = op
		mu.Unlock()
	}()
	wg.Wait()

	workers := Join(JoinInputs{
		Agents:       res.agents,
		Aliases:      res.aliases,
		OperatorPane: res.operatorPane,
		Heartbeats:   res.heartbeats,
		HBWindow:     hbWindow,
	})
	return workers, res.errs
}

// Errors is keyed by source name ("agents", "heartbeats", "aliases",
// "operator"). Empty when every source succeeded.
type Errors map[string]error

// HasFatal returns true when the agents source — the only source required
// for any registry entries to appear — failed. The other three sources
// only enrich entries; their failures don't make the snapshot invalid.
func (e Errors) HasFatal() bool {
	_, fatal := e["agents"]
	return fatal
}
