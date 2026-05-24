package spawnspec

import (
	"strings"
	"testing"
	"time"
)

func TestSpawnSpec_RoundTrip(t *testing.T) {
	in := &SpawnSpec{
		Name:        "lead-engineer",
		Description: "Backend engineer for shim PRs",
		Agent:       AgentClaudeCode,
		Session:     "lead-engineer",
		Cwd:         "/Users/dmestas/projects/orch",
		Owner:       "dmestas",
		Labels:      map[string]string{"role": "engineer", "tier": "lead"},
		Outfit:      &OutfitBlock{Bundle: "backend/executing+pr-policy"},
		Env: map[string]string{
			"NATS_URL":   "nats://127.0.0.1:58413",
			"ORCH_OWNER": "dmestas",
		},
		Tmux: &TmuxBlock{Headless: true, Verify: false, Layout: "default"},
	}

	data, err := MarshalSpec(in)
	if err != nil {
		t.Fatalf("MarshalSpec: %v", err)
	}
	out, err := UnmarshalSpec(data)
	if err != nil {
		t.Fatalf("UnmarshalSpec: %v\nyaml was:\n%s", err, data)
	}

	if out.SpecVersion != SpecVersion {
		t.Errorf("SpecVersion: want %q, got %q", SpecVersion, out.SpecVersion)
	}
	if out.Name != in.Name {
		t.Errorf("Name: want %q, got %q", in.Name, out.Name)
	}
	if out.Agent != in.Agent {
		t.Errorf("Agent: want %q, got %q", in.Agent, out.Agent)
	}
	if out.Tmux == nil || !out.Tmux.Headless {
		t.Errorf("Tmux block lost in round-trip: %+v", out.Tmux)
	}
	if out.Outfit == nil || out.Outfit.Bundle != "backend/executing+pr-policy" {
		t.Errorf("Outfit block lost in round-trip: %+v", out.Outfit)
	}
	if got := out.Env["NATS_URL"]; got != "nats://127.0.0.1:58413" {
		t.Errorf("Env NATS_URL: got %q", got)
	}
}

func TestSpawnSpec_VersionMismatch(t *testing.T) {
	// UnmarshalSpec is the v1-only entrypoint. Versions other than v1
	// must be rejected so callers that only handle v1 don't silently
	// downgrade a future-version document into a partial v1 read.
	// Once v2 exists, the rejection must still apply to v1's UnmarshalSpec
	// (the version-agnostic entrypoint is UnmarshalAnySpec).
	yaml := []byte("spec_version: v99\nname: x\nagent: echo\ntmux:\n  headless: true\n")
	_, err := UnmarshalSpec(yaml)
	if err == nil {
		t.Fatal("expected version-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "unsupported spec_version") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSpawnSpec_DefaultVersion(t *testing.T) {
	yaml := []byte("name: x\nagent: echo\ntmux:\n  headless: true\n")
	s, err := UnmarshalSpec(yaml)
	if err != nil {
		t.Fatalf("UnmarshalSpec: %v", err)
	}
	if s.SpecVersion != SpecVersion {
		t.Errorf("SpecVersion default: want %q, got %q", SpecVersion, s.SpecVersion)
	}
}

func TestSpawnSpec_UnknownField(t *testing.T) {
	yaml := []byte("name: x\nagent: echo\nbogus_field: true\ntmux: {headless: true}\n")
	_, err := UnmarshalSpec(yaml)
	if err == nil {
		t.Fatal("expected unknown-field error, got nil")
	}
	if !strings.Contains(err.Error(), "bogus_field") {
		t.Errorf("error should name the field; got: %v", err)
	}
}

func TestValidate_MissingRequired(t *testing.T) {
	s := &SpawnSpec{
		// Name omitted
		Agent: AgentEcho,
		Tmux:  &TmuxBlock{},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation error for missing Name")
	}
	if !strings.Contains(err.Error(), "Name") {
		t.Errorf("error should name the missing field; got: %v", err)
	}
}

func TestValidate_BadAgent(t *testing.T) {
	s := &SpawnSpec{
		Name:  "x",
		Agent: Agent("definitely-not-a-real-agent"),
		Tmux:  &TmuxBlock{},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation error for unknown agent")
	}
	if !strings.Contains(err.Error(), "must be one of") {
		t.Errorf("error should hint at the enum; got: %v", err)
	}
}

func TestValidate_BadDNSLabel(t *testing.T) {
	for _, bad := range []string{"Has.Dots", "UPPER", "ends-with-", "-starts", ""} {
		t.Run(bad, func(t *testing.T) {
			s := &SpawnSpec{
				Name:  bad,
				Agent: AgentEcho,
				Tmux:  &TmuxBlock{},
			}
			if err := ValidateSpec(s); err == nil {
				t.Errorf("expected validation failure for %q", bad)
			}
		})
	}
}

func TestValidate_ExecutorXOR_Zero(t *testing.T) {
	s := &SpawnSpec{
		Name:  "x",
		Agent: AgentEcho,
		// no executor block
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation error for zero executor blocks")
	}
	if !strings.Contains(err.Error(), "missing executor") {
		t.Errorf("error should explain zero-executor-block rule; got: %v", err)
	}
}

func TestValidate_ExecutorXOR_Multi(t *testing.T) {
	s := &SpawnSpec{
		Name:     "x",
		Agent:    AgentEcho,
		Tmux:     &TmuxBlock{},
		CFWorker: &CFWorkerBlock{Script: "/tmp/w.js"},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation error for multiple executor blocks")
	}
	if !strings.Contains(err.Error(), "multiple executor blocks") {
		t.Errorf("error should explain multi-executor-block rule; got: %v", err)
	}
}

func TestValidate_OutfitXOR(t *testing.T) {
	s := &SpawnSpec{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlock{},
		Outfit: &OutfitBlock{
			Bundle: "engineer/focused", // shorthand
			Name:   "engineer",         // AND explicit
			Cut:    "focused",
		},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected outfit_xor error when both shorthand and explicit are set")
	}
}

func TestValidate_EnvKey(t *testing.T) {
	s := &SpawnSpec{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlock{},
		Env:   map[string]string{"lowercase_key": "x"},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation failure for lowercase env key")
	}
	if !strings.Contains(err.Error(), "lowercase_key") {
		t.Errorf("error should name the bad key; got: %v", err)
	}
}

func TestValidate_Tmux_BadLayout(t *testing.T) {
	s := &SpawnSpec{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlock{Layout: "rainbow"},
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation failure for unknown layout")
	}
}

func TestValidate_CFWorker_RequiresScript(t *testing.T) {
	s := &SpawnSpec{
		Name:     "x",
		Agent:    AgentEcho,
		CFWorker: &CFWorkerBlock{}, // no script
	}
	err := ValidateSpec(s)
	if err == nil {
		t.Fatal("expected validation failure for missing CFWorker.Script")
	}
}

func TestWorkerHandle_RoundTrip(t *testing.T) {
	in := &WorkerHandle{
		Name:      "lead-engineer",
		Agent:     AgentClaudeCode,
		Session:   "lead-engineer",
		CreatedAt: time.Date(2026, 5, 18, 13, 0, 0, 0, time.UTC),
		Executor:  "tmux",
		PaneID:    "%64",
		Bus: &BusBlock{
			Prompt: "agents.prompt.cc.dmestas.lead-engineer",
			Status: "agents.status.cc.dmestas.lead-engineer",
			HB:     "agents.hb.cc.dmestas.lead-engineer",
			Signal: "orch.signal.>.cc.dmestas.lead-engineer",
		},
		Abort:   &AbortBlock{Kind: "tmux-send-keys", Target: "%64", Keys: "C-c"},
		LogFile: "/Users/dmestas/.cache/orch-shim/pct64.log",
		PID:     12345,
		Status:  "ready",
	}
	data, err := MarshalHandle(in)
	if err != nil {
		t.Fatalf("MarshalHandle: %v", err)
	}
	out, err := UnmarshalHandle(data)
	if err != nil {
		t.Fatalf("UnmarshalHandle: %v\nyaml was:\n%s", err, data)
	}
	if out.PaneID != in.PaneID {
		t.Errorf("PaneID round-trip lost: want %q, got %q", in.PaneID, out.PaneID)
	}
	if out.Bus == nil || out.Bus.Prompt != in.Bus.Prompt {
		t.Errorf("Bus block round-trip lost")
	}
	if !out.CreatedAt.Equal(in.CreatedAt) {
		t.Errorf("CreatedAt round-trip lost: want %v, got %v", in.CreatedAt, out.CreatedAt)
	}
}

func TestValidateHandle_TmuxRequiresPaneID(t *testing.T) {
	h := &WorkerHandle{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "tmux",
		// PaneID empty
		Status: "ready",
	}
	err := ValidateHandle(h)
	if err == nil {
		t.Fatal("expected validation failure for tmux executor with empty pane_id")
	}
}

func TestValidateHandle_FailedRequiresMessage(t *testing.T) {
	h := &WorkerHandle{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "tmux",
		PaneID:    "%5",
		Status:    "failed",
		// Message empty
	}
	err := ValidateHandle(h)
	if err == nil {
		t.Fatal("expected validation failure for status=failed with empty message")
	}
}

func TestValidateHandle_PendingOK(t *testing.T) {
	h := &WorkerHandle{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "cf-worker",
		ID:        "https://w.example.workers.dev",
		Status:    "pending",
		Message:   "wrangler deploy in progress",
	}
	if err := ValidateHandle(h); err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}
