package registry

import (
	"context"
	"testing"
	"time"
)

// Tests for Proposal 0009 / issue #181: stable slug as worker identity.
//
// The shim publishes a stable per-worker slug as metadata.instance_id;
// orch-spawn supplies it via --instance-id (or derives it from
// $SESH_SESSION). The registry surfaces it on Worker.InstanceID,
// promotes it into Worker.Name (above the session label but below an
// operator-set alias), and indexes it so Lookup resolves the slug back
// to the worker.

func TestJoin_InstanceIDFromMetadata(t *testing.T) {
	// metadata.instance_id (slug) overrides the per-process micro id.
	a := AgentInfo{
		InstanceID: "micro-uuid-12345",
		Metadata: map[string]string{
			"pane_id":     "%64",
			"role":        "worker",
			"agent":       "claude-code",
			"owner":       "dmestas",
			"instance_id": "lead-engineer",
		},
	}
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	w := mustOneWorker(t, ws)
	if w.InstanceID != "lead-engineer" {
		t.Errorf("metadata.instance_id should win over micro id: got %q want lead-engineer",
			w.InstanceID)
	}
}

func TestJoin_InstanceIDFallsBackToMicroID(t *testing.T) {
	// When metadata.instance_id is absent, Worker.InstanceID falls back to
	// the micro-service info_response id (preserves legacy behaviour).
	a := AgentInfo{
		InstanceID: "micro-uuid-12345",
		Metadata: map[string]string{
			"pane_id": "%64",
			"role":    "worker",
			"agent":   "claude-code",
			"owner":   "dmestas",
		},
	}
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	w := mustOneWorker(t, ws)
	if w.InstanceID != "micro-uuid-12345" {
		t.Errorf("InstanceID should fall back to micro id when slug absent: got %q",
			w.InstanceID)
	}
}

func TestJoin_NamePrecedence_SlugBeatsSession(t *testing.T) {
	// Worker.Name precedence: alias > metadata.instance_id (slug) > session > pct.
	// Here no alias and no session-only — slug wins over session for display.
	a := AgentInfo{
		Metadata: map[string]string{
			"pane_id":     "%64",
			"role":        "worker",
			"agent":       "claude-code",
			"owner":       "dmestas",
			"session":     "headless-1234",
			"instance_id": "lead-engineer",
		},
	}
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	w := mustOneWorker(t, ws)
	if w.Name != "lead-engineer" {
		t.Errorf("slug should win over session in Name: got %q want lead-engineer",
			w.Name)
	}
}

func TestJoin_NamePrecedence_AliasStillBeatsSlug(t *testing.T) {
	// Operator's explicit alias overrides the slug — the alias file is
	// the operator's intentional override of bus-side identity.
	a := AgentInfo{
		Metadata: map[string]string{
			"pane_id":     "%64",
			"role":        "worker",
			"agent":       "claude-code",
			"owner":       "dmestas",
			"instance_id": "lead-engineer",
		},
	}
	ws := Join(JoinInputs{
		Agents:  []AgentInfo{a},
		Aliases: map[string]string{"the-boss": "%64"},
		Now:     time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Name != "the-boss" {
		t.Errorf("alias should still beat slug: got %q want the-boss", w.Name)
	}
	// InstanceID is unchanged — alias only affects display Name.
	if w.InstanceID != "lead-engineer" {
		t.Errorf("alias should not change InstanceID: got %q", w.InstanceID)
	}
}

func TestLive_LookupBySlug(t *testing.T) {
	// Lookup resolves the slug back to the worker via the byInstance index,
	// even when an alias overrides the display Name.
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "micro-1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code",
			"owner": "dmestas", "instance_id": "lead-engineer",
		}},
	}}
	// Alias takes the display Name; the slug should still be a lookup key.
	al := &mockAliases{aliases: map[string]string{"the-boss": "%64"}}

	live := NewLive(Readers{Agents: ag, Aliases: al}, LiveOptions{RefreshInterval: 0})
	if err := live.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer live.Close() //nolint:errcheck

	// Pane lookup still works.
	if w, ok := live.Lookup("%64"); !ok || w.PaneID != "%64" {
		t.Errorf("lookup by pane failed: %+v ok=%v", w, ok)
	}
	// Alias lookup still works (alias beats slug in display Name).
	if w, ok := live.Lookup("the-boss"); !ok || w.PaneID != "%64" {
		t.Errorf("lookup by alias failed: %+v ok=%v", w, ok)
	}
	// Slug lookup resolves via the byInstance fallback even though
	// the worker's display Name is "the-boss".
	if w, ok := live.Lookup("lead-engineer"); !ok || w.PaneID != "%64" {
		t.Errorf("lookup by slug failed: %+v ok=%v", w, ok)
	}
	// Negative — unrelated string still misses.
	if _, ok := live.Lookup("not-a-worker"); ok {
		t.Errorf("lookup by unknown key should miss")
	}
}

func TestLive_LookupBySlug_NoAlias(t *testing.T) {
	// Without an alias, the slug surfaces as Name AND is in byInstance —
	// both lookup paths resolve to the same worker.
	ag := &mockAgents{agents: []AgentInfo{
		{InstanceID: "micro-1", Metadata: map[string]string{
			"pane_id": "%64", "role": "worker", "agent": "claude-code",
			"owner": "dmestas", "instance_id": "lead-engineer",
		}},
	}}

	live := NewLive(Readers{Agents: ag}, LiveOptions{RefreshInterval: 0})
	if err := live.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer live.Close() //nolint:errcheck

	w, ok := live.Lookup("lead-engineer")
	if !ok || w.PaneID != "%64" {
		t.Errorf("lookup by slug (no alias case): %+v ok=%v", w, ok)
	}
	if w.Name != "lead-engineer" {
		t.Errorf("Name should be the slug when no alias: got %q", w.Name)
	}
}

func TestParseAgentInfo_SurfacesInstanceIDInMetadata(t *testing.T) {
	// metadata.instance_id is just a metadata key; it should pass through
	// parseAgentInfo unchanged. This is the contract orch's join code
	// relies on for promoting slug → Worker.InstanceID.
	//
	// (parseAgentInfo lives in nats_reader.go in this package. We test
	// the projection here at the Join boundary because the metadata map
	// is the public surface — nats_reader_test.go covers the parse itself.)
	a := AgentInfo{
		Metadata: map[string]string{
			"pane_id":     "%64",
			"instance_id": "engineer-7",
			"agent":       "claude-code",
			"owner":       "dmestas",
		},
	}
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	w := mustOneWorker(t, ws)
	if w.Metadata["instance_id"] != "engineer-7" {
		t.Errorf("raw metadata.instance_id should be preserved: got %q",
			w.Metadata["instance_id"])
	}
	if w.InstanceID != "engineer-7" {
		t.Errorf("Worker.InstanceID should promote metadata.instance_id: got %q",
			w.InstanceID)
	}
}
