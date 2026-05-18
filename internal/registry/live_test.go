package registry

import (
	"context"
	"maps"
	"sync"
	"testing"
	"time"
)

// --- mock readers ------------------------------------------------------

type mockAgents struct {
	mu       sync.Mutex
	agents   []AgentInfo
	err      error
	callsObs int
}

func (m *mockAgents) set(a []AgentInfo) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.agents = a
}
func (m *mockAgents) Agents(ctx context.Context) ([]AgentInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callsObs++
	if m.err != nil {
		return nil, m.err
	}
	out := make([]AgentInfo, len(m.agents))
	copy(out, m.agents)
	return out, nil
}

type mockHB struct {
	mu sync.Mutex
	hb map[string]time.Time
}

func (m *mockHB) Heartbeats(ctx context.Context) (map[string]time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]time.Time, len(m.hb))
	maps.Copy(out, m.hb)
	return out, nil
}

type mockAliases struct {
	aliases map[string]string
}

func (m *mockAliases) Aliases(ctx context.Context) (map[string]string, error) {
	return m.aliases, nil
}

type mockOperator struct{ pane string }

func (m *mockOperator) OperatorPane(ctx context.Context) (string, error) {
	return m.pane, nil
}

// --- Snapshot ----------------------------------------------------------

func TestSnapshot_AllSourcesContribute(t *testing.T) {
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
		{InstanceID: "i2", Metadata: map[string]string{
			"pane_id": "%17", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	}}
	hb := &mockHB{hb: map[string]time.Time{
		"%64": time.Now().Add(-10 * time.Second),
	}}
	al := &mockAliases{aliases: map[string]string{"engineer": "%64"}}
	op := &mockOperator{pane: "%17"}

	ws, errs := Snapshot(context.Background(), Readers{
		Agents: ag, Heartbeats: hb, Aliases: al, Operator: op,
	}, 90*time.Second)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(ws) != 2 {
		t.Fatalf("want 2 workers, got %d", len(ws))
	}
	// Sort is by numeric pane: %17 first.
	if ws[0].PaneID != "%17" || ws[0].Role != "operator" {
		t.Errorf("expected %%17 as operator first, got %+v", ws[0])
	}
	if ws[1].PaneID != "%64" || ws[1].Name != "engineer" {
		t.Errorf("expected %%64 named 'engineer' second, got %+v", ws[1])
	}
}

func TestSnapshot_NilReadersDegrade(t *testing.T) {
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	}}
	// Heartbeats / Aliases / Operator all nil — should not panic, should
	// still produce a Worker.
	ws, errs := Snapshot(context.Background(), Readers{Agents: ag}, 90*time.Second)
	if len(errs) != 0 {
		t.Fatalf("nil readers should not produce errors: %v", errs)
	}
	if len(ws) != 1 {
		t.Fatalf("want 1 worker, got %d", len(ws))
	}
	if !ws[0].Alive {
		t.Errorf("no HB seen → Alive should be true (registered but not yet heartbeated)")
	}
}

func TestSnapshot_AgentsErrorIsFatal(t *testing.T) {
	ag := &mockAgents{err: testErr("bus unreachable")}
	_, errs := Snapshot(context.Background(), Readers{Agents: ag}, 0)
	if !errs.HasFatal() {
		t.Errorf("agents error should be flagged fatal: %v", errs)
	}
}

type testErr string

func (e testErr) Error() string { return string(e) }

// --- Live registry ------------------------------------------------------

func TestLive_LookupByNameAndPane(t *testing.T) {
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code",
			"owner": "dmestas", "session": "lead-engineer",
		}},
	}}
	al := &mockAliases{aliases: map[string]string{"engineer": "%64"}}

	live := NewLive(Readers{Agents: ag, Aliases: al}, LiveOptions{
		RefreshInterval: 0, // no background poll
	})
	if err := live.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer live.Close() //nolint:errcheck

	if w, ok := live.Lookup("%64"); !ok || w.PaneID != "%64" {
		t.Errorf("lookup by pane failed: %+v ok=%v", w, ok)
	}
	if w, ok := live.Lookup("engineer"); !ok || w.PaneID != "%64" {
		t.Errorf("lookup by alias failed: %+v ok=%v", w, ok)
	}
	// session label is also a valid name path (since alias took priority
	// for the worker's display name).
	if _, ok := live.Lookup("nonexistent"); ok {
		t.Errorf("nonexistent lookup should fail")
	}
}

func TestLive_EmitsJoinedAndDepartedEvents(t *testing.T) {
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	}}

	live := NewLive(Readers{Agents: ag}, LiveOptions{RefreshInterval: 0})
	ctx := t.Context()
	if err := live.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer live.Close() //nolint:errcheck

	events := live.Watch(ctx)

	// First refresh post-Watch — adding a new agent should yield Joined.
	ag.set([]AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
		{InstanceID: "i2", Metadata: map[string]string{
			"pane_id": "%99", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	})
	if err := live.refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if ev := mustReceive(t, events, 200*time.Millisecond); ev.Type != Joined || ev.Worker.PaneID != "%99" {
		t.Errorf("expected Joined %%99, got %+v", ev)
	}

	// Remove %99 — departed.
	ag.set([]AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	})
	if err := live.refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if ev := mustReceive(t, events, 200*time.Millisecond); ev.Type != Departed || ev.Worker.PaneID != "%99" {
		t.Errorf("expected Departed %%99, got %+v", ev)
	}
}

func TestLive_EmitsUpdatedOnAliveTransition(t *testing.T) {
	now := time.Now()
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "i1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code", "owner": "dmestas",
		}},
	}}
	hb := &mockHB{hb: map[string]time.Time{"%64": now}}

	live := NewLive(Readers{Agents: ag, Heartbeats: hb}, LiveOptions{
		RefreshInterval: 0,
		HeartbeatWindow: 50 * time.Millisecond,
	})
	ctx := t.Context()
	if err := live.Start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer live.Close() //nolint:errcheck

	events := live.Watch(ctx)

	// Push the heartbeat back so the next refresh marks alive=false.
	hb.mu.Lock()
	hb.hb["%64"] = now.Add(-1 * time.Hour)
	hb.mu.Unlock()
	if err := live.refresh(ctx); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	ev := mustReceive(t, events, 200*time.Millisecond)
	if ev.Type != Updated || ev.Worker.Alive {
		t.Errorf("expected Updated with Alive=false, got %+v", ev)
	}
}

func mustReceive(t *testing.T, ch <-chan Event, d time.Duration) Event {
	t.Helper()
	select {
	case ev, ok := <-ch:
		if !ok {
			t.Fatalf("channel closed unexpectedly")
		}
		return ev
	case <-time.After(d):
		t.Fatalf("no event after %v", d)
		return Event{}
	}
}
