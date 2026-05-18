package registry

import (
	"reflect"
	"testing"
	"time"
)

// --- helpers -----------------------------------------------------------

func mkAgent(pane, role, agent, owner, session, outfit, instanceID string, endpoints map[string]string) AgentInfo {
	meta := map[string]string{
		"pane_id": pane,
		"role":    role,
		"agent":   agent,
		"owner":   owner,
		"session": session,
		"outfit":  outfit,
	}
	eps := make([]EndpointInfo, 0, len(endpoints))
	for name, subj := range endpoints {
		eps = append(eps, EndpointInfo{Name: name, Subject: subj})
	}
	return AgentInfo{InstanceID: instanceID, Metadata: meta, Endpoints: eps}
}

func mustOneWorker(t *testing.T, ws []Worker) Worker {
	t.Helper()
	if len(ws) != 1 {
		t.Fatalf("want 1 worker, got %d: %+v", len(ws), ws)
	}
	return ws[0]
}

// --- Join contract -----------------------------------------------------

func TestJoin_EmptyBus(t *testing.T) {
	ws := Join(JoinInputs{Now: time.Unix(1700000000, 0)})
	if len(ws) != 0 {
		t.Fatalf("want 0 workers, got %d", len(ws))
	}
}

func TestJoin_SingleAgentMinimalMetadata(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{mkAgent("%64", "worker", "claude-code", "dmestas", "", "", "inst-1", nil)},
		Now:    time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.PaneID != "%64" || w.InstanceID != "inst-1" {
		t.Errorf("identity wrong: %+v", w)
	}
	if w.Role != "worker" || w.Agent != "claude-code" || w.Owner != "dmestas" {
		t.Errorf("metadata projection wrong: %+v", w)
	}
	// No session, no alias → name falls back to pct-form.
	if w.Name != "pct64" {
		t.Errorf("name fallback wrong: got %q want %q", w.Name, "pct64")
	}
	// No heartbeat observed → Alive=true (registered, just no HB yet).
	if !w.Alive {
		t.Errorf("registered-but-no-HB should be Alive=true; got %+v", w)
	}
}

func TestJoin_NamePrecedence_AliasBeatsSession(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{mkAgent("%64", "worker", "claude-code", "dmestas", "lead-engineer", "", "", nil)},
		Aliases: map[string]string{
			"engineer": "%64",
		},
		Now: time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Name != "engineer" {
		t.Errorf("alias should win over session: got %q want %q", w.Name, "engineer")
	}
}

func TestJoin_NamePrecedence_SessionBeatsPctForm(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{mkAgent("%64", "worker", "claude-code", "dmestas", "lead-engineer", "", "", nil)},
		Now:    time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Name != "lead-engineer" {
		t.Errorf("session should win over pct fallback: got %q want %q", w.Name, "lead-engineer")
	}
}

func TestJoin_OperatorMarkerOverridesRole(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{
			mkAgent("%17", "worker", "claude-code", "dmestas", "", "", "", nil),
		},
		OperatorPane: "%17",
		Now:          time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Role != "operator" {
		t.Errorf("operator marker should pin role; got %q", w.Role)
	}
}

func TestJoin_DefaultRoleWhenMetadataEmpty(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{mkAgent("%64", "", "claude-code", "dmestas", "", "", "", nil)},
		Now:    time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Role != "worker" {
		t.Errorf("missing role should default to worker; got %q", w.Role)
	}
}

func TestJoin_AgentWithoutPaneIDIsSkipped(t *testing.T) {
	// An agent reply without metadata.pane_id is not a worker; the
	// registry refuses to invent identity. (External services that
	// advertise on $SRV.INFO but aren't pane-bound fall through.)
	a := mkAgent("", "worker", "claude-code", "dmestas", "", "", "", nil)
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	if len(ws) != 0 {
		t.Fatalf("pane-less agent should be skipped; got %d workers", len(ws))
	}
}

func TestJoin_AliveTransitions(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		name      string
		hb        time.Time
		hbWindow  time.Duration
		wantAlive bool
		wantHB    time.Time
	}{
		{"fresh-heartbeat", now.Add(-30 * time.Second), 90 * time.Second, true, now.Add(-30 * time.Second)},
		{"edge-of-window", now.Add(-90 * time.Second), 90 * time.Second, true, now.Add(-90 * time.Second)},
		{"just-past-window", now.Add(-91 * time.Second), 90 * time.Second, false, now.Add(-91 * time.Second)},
		{"way-stale", now.Add(-1 * time.Hour), 90 * time.Second, false, now.Add(-1 * time.Hour)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws := Join(JoinInputs{
				Agents:     []AgentInfo{mkAgent("%64", "worker", "claude-code", "dmestas", "", "", "", nil)},
				Heartbeats: map[string]time.Time{"%64": tc.hb},
				HBWindow:   tc.hbWindow,
				Now:        now,
			})
			w := mustOneWorker(t, ws)
			if w.Alive != tc.wantAlive {
				t.Errorf("alive: got %v want %v", w.Alive, tc.wantAlive)
			}
			if !w.LastHB.Equal(tc.wantHB) {
				t.Errorf("LastHB: got %v want %v", w.LastHB, tc.wantHB)
			}
		})
	}
}

func TestJoin_HeartbeatWindowDefaults(t *testing.T) {
	now := time.Unix(1700000000, 0)
	// HBWindow == 0 → DefaultHeartbeatWindow (90s). A 60s-old heartbeat
	// is still alive; a 120s-old one isn't.
	ws := Join(JoinInputs{
		Agents: []AgentInfo{
			mkAgent("%64", "worker", "claude-code", "dmestas", "", "", "", nil),
			mkAgent("%99", "worker", "claude-code", "dmestas", "", "", "", nil),
		},
		Heartbeats: map[string]time.Time{
			"%64": now.Add(-60 * time.Second),
			"%99": now.Add(-120 * time.Second),
		},
		Now: now,
	})
	if len(ws) != 2 {
		t.Fatalf("want 2 workers, got %d", len(ws))
	}
	if !ws[0].Alive {
		t.Errorf("%%64 (60s old) should be alive under default window")
	}
	if ws[1].Alive {
		t.Errorf("%%99 (120s old) should be dead under default window")
	}
}

func TestJoin_PaneIDOrderingIsNumeric(t *testing.T) {
	// "%10" should sort AFTER "%9", not before — lexical order would
	// reverse them. orch-peek depends on numeric ordering.
	ws := Join(JoinInputs{
		Agents: []AgentInfo{
			mkAgent("%10", "worker", "claude-code", "dmestas", "ten", "", "", nil),
			mkAgent("%9", "worker", "claude-code", "dmestas", "nine", "", "", nil),
			mkAgent("%100", "worker", "claude-code", "dmestas", "hundred", "", "", nil),
		},
		Now: time.Unix(1700000000, 0),
	})
	got := []string{ws[0].PaneID, ws[1].PaneID, ws[2].PaneID}
	want := []string{"%9", "%10", "%100"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("numeric pane order:\n got: %v\nwant: %v", got, want)
	}
}

func TestJoin_SubjectsFromEndpoints(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{
			mkAgent("%64", "worker", "claude-code", "dmestas", "lead-engineer", "", "", map[string]string{
				"prompt": "agents.prompt.cc.dmestas.lead-engineer",
				"status": "agents.status.cc.dmestas.lead-engineer",
			}),
		},
		Now: time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	if w.Subjects.Prompt != "agents.prompt.cc.dmestas.lead-engineer" {
		t.Errorf("prompt subject not promoted: %q", w.Subjects.Prompt)
	}
	if w.Subjects.Status != "agents.status.cc.dmestas.lead-engineer" {
		t.Errorf("status subject not promoted: %q", w.Subjects.Status)
	}
	// HB subject is derived (the micro framework doesn't advertise it).
	wantHB := "agents.hb.cc.dmestas.lead-engineer"
	if w.Subjects.HB != wantHB {
		t.Errorf("hb subject derive: got %q want %q", w.Subjects.HB, wantHB)
	}
}

func TestJoin_HBSubjectFallsBackToPctWhenNoSession(t *testing.T) {
	ws := Join(JoinInputs{
		Agents: []AgentInfo{
			mkAgent("%64", "worker", "claude-code", "dmestas", "", "", "", nil),
		},
		Now: time.Unix(1700000000, 0),
	})
	w := mustOneWorker(t, ws)
	want := "agents.hb.cc.dmestas.pct64"
	if w.Subjects.HB != want {
		t.Errorf("hb subject pct fallback: got %q want %q", w.Subjects.HB, want)
	}
}

func TestJoin_AgentTokenAbbreviation(t *testing.T) {
	// claude-code → cc; everything else → identity. Mirrors
	// shim.withDefaults.
	cases := []struct {
		agent string
		want  string
	}{
		{"claude-code", "cc"},
		{"codex", "codex"},
		{"pi", "pi"},
		{"gemini", "gemini"},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			ws := Join(JoinInputs{
				Agents: []AgentInfo{mkAgent("%64", "worker", tc.agent, "dmestas", "sess", "", "", nil)},
				Now:    time.Unix(1700000000, 0),
			})
			w := mustOneWorker(t, ws)
			wantHB := "agents.hb." + tc.want + ".dmestas.sess"
			if w.Subjects.HB != wantHB {
				t.Errorf("hb subject for agent %q: got %q want %q",
					tc.agent, w.Subjects.HB, wantHB)
			}
		})
	}
}

func TestJoin_PreservesRawMetadata(t *testing.T) {
	a := mkAgent("%64", "worker", "claude-code", "dmestas", "", "", "", nil)
	a.Metadata["custom_field"] = "diagnostic"
	ws := Join(JoinInputs{Agents: []AgentInfo{a}, Now: time.Unix(1700000000, 0)})
	w := mustOneWorker(t, ws)
	if w.Metadata["custom_field"] != "diagnostic" {
		t.Errorf("raw metadata not preserved: %+v", w.Metadata)
	}
}
