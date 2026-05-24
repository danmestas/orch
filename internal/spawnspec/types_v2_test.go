package spawnspec

import (
	"strings"
	"testing"
	"time"
)

// ─── v2 SpawnSpec parse / round-trip / validate ──────────────────────

func TestV2_SpawnSpec_RoundTrip_Tmux(t *testing.T) {
	in := &SpawnSpecV2{
		Name:    "lead",
		Agent:   AgentClaudeCode,
		Session: "lead",
		Tmux:    &TmuxBlockV2{Headless: true, Layout: "default"},
	}
	data, err := MarshalSpecV2(in)
	if err != nil {
		t.Fatalf("MarshalSpecV2: %v", err)
	}
	out, err := UnmarshalSpecV2(data)
	if err != nil {
		t.Fatalf("UnmarshalSpecV2: %v\nyaml was:\n%s", err, data)
	}
	if out.SpecVersion != SpecVersionV2 {
		t.Errorf("SpecVersion default: want %q, got %q", SpecVersionV2, out.SpecVersion)
	}
	if out.Tmux == nil || !out.Tmux.Headless || out.Tmux.Layout != "default" {
		t.Errorf("Tmux block lost in round-trip: %+v", out.Tmux)
	}
}

func TestV2_SpawnSpec_RoundTrip_Cmux(t *testing.T) {
	in := &SpawnSpecV2{
		Name:  "engineer",
		Agent: AgentCodex,
		Cmux:  &CmuxBlock{Verify: true, Position: "right"},
	}
	data, err := MarshalSpecV2(in)
	if err != nil {
		t.Fatalf("MarshalSpecV2: %v", err)
	}
	out, err := UnmarshalSpecV2(data)
	if err != nil {
		t.Fatalf("UnmarshalSpecV2: %v", err)
	}
	if out.Cmux == nil || !out.Cmux.Verify || out.Cmux.Position != "right" {
		t.Errorf("Cmux block lost in round-trip: %+v", out.Cmux)
	}
	if err := ValidateSpecV2(out); err != nil {
		t.Errorf("ValidateSpecV2 on cmux spec: %v", err)
	}
}

func TestV2_SpawnSpec_RoundTrip_Zmx_LayoutNone(t *testing.T) {
	// The composition-table valid pair: executor=zmx, no tmux block at
	// all (layout=none lives on the tmux block, not the spec; the v2
	// spec models the zmx case as "tmux block absent, zmx block
	// present").
	in := &SpawnSpecV2{
		Name:  "engineer",
		Agent: AgentClaudeCode,
		Zmx:   &ZmxBlock{SessionName: "engineer-z", Verify: true},
	}
	data, err := MarshalSpecV2(in)
	if err != nil {
		t.Fatalf("MarshalSpecV2: %v", err)
	}
	out, err := UnmarshalSpecV2(data)
	if err != nil {
		t.Fatalf("UnmarshalSpecV2: %v", err)
	}
	if out.Zmx == nil || out.Zmx.SessionName != "engineer-z" || !out.Zmx.Verify {
		t.Errorf("Zmx block lost in round-trip: %+v", out.Zmx)
	}
	if err := ValidateSpecV2(out); err != nil {
		t.Errorf("ValidateSpecV2 on zmx spec: %v", err)
	}
}

func TestV2_TmuxBlock_LayoutNone_Rejected(t *testing.T) {
	// layout=none on a tmux block is a category error: the marker
	// "no in-pane layout" only makes sense for the zmx engine.
	s := &SpawnSpecV2{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlockV2{Layout: "none"},
	}
	err := ValidateSpecV2(s)
	if err == nil {
		t.Fatal("expected validation failure for tmux + layout=none")
	}
	if !strings.Contains(err.Error(), "layout=none") || !strings.Contains(err.Error(), "executor=zmx") {
		t.Errorf("error should mention layout=none + executor=zmx; got: %v", err)
	}
}

func TestV2_TmuxBlock_LayoutNone_AcceptedInEnumTag(t *testing.T) {
	// The struct tag enum allows "none" so the validator's oneof
	// doesn't fire on it; the layout-none-only-with-zmx rule is what
	// rejects it on a tmux block. This test pins the enum widening.
	s := &SpawnSpecV2{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlockV2{Layout: "rainbow"}, // unknown — must fail
	}
	err := ValidateSpecV2(s)
	if err == nil {
		t.Fatal("expected validation failure for unknown layout")
	}
	if !strings.Contains(err.Error(), "default grid full none") {
		t.Errorf("error should list the v2 layout enum including 'none'; got: %v", err)
	}
}

func TestV2_ExecutorXOR_Zero(t *testing.T) {
	s := &SpawnSpecV2{Name: "x", Agent: AgentEcho}
	err := ValidateSpecV2(s)
	if err == nil {
		t.Fatal("expected validation error for zero executor blocks")
	}
	if !strings.Contains(err.Error(), "missing executor block") {
		t.Errorf("error should explain zero-executor-block rule; got: %v", err)
	}
	if !strings.Contains(err.Error(), "cmux") || !strings.Contains(err.Error(), "zmx") {
		t.Errorf("v2 error should mention cmux + zmx in the executor enum; got: %v", err)
	}
}

func TestV2_ExecutorXOR_Multi(t *testing.T) {
	s := &SpawnSpecV2{
		Name:  "x",
		Agent: AgentEcho,
		Tmux:  &TmuxBlockV2{},
		Cmux:  &CmuxBlock{},
	}
	err := ValidateSpecV2(s)
	if err == nil {
		t.Fatal("expected validation error for multi executor blocks")
	}
	if !strings.Contains(err.Error(), "multiple executor blocks") {
		t.Errorf("error should explain multi-executor-block rule; got: %v", err)
	}
}

// ─── Version dispatcher ──────────────────────────────────────────────

func TestV2_UnmarshalAnySpec_V1Document(t *testing.T) {
	yaml := []byte("spec_version: v1\nname: x\nagent: echo\ntmux:\n  headless: true\n")
	got, err := UnmarshalAnySpec(yaml)
	if err != nil {
		t.Fatalf("UnmarshalAnySpec: %v", err)
	}
	if got.Version != SpecVersionV1 || got.V1 == nil || got.V2 != nil {
		t.Errorf("expected V1 routing, got %+v", got)
	}
}

func TestV2_UnmarshalAnySpec_V2Document(t *testing.T) {
	yaml := []byte("spec_version: v2\nname: x\nagent: echo\ncmux:\n  verify: true\n")
	got, err := UnmarshalAnySpec(yaml)
	if err != nil {
		t.Fatalf("UnmarshalAnySpec: %v", err)
	}
	if got.Version != SpecVersionV2 || got.V2 == nil || got.V1 != nil {
		t.Errorf("expected V2 routing, got %+v", got)
	}
	if got.V2.Cmux == nil || !got.V2.Cmux.Verify {
		t.Errorf("V2 cmux block not parsed: %+v", got.V2.Cmux)
	}
}

func TestV2_UnmarshalAnySpec_MissingVersionDefaultsV1(t *testing.T) {
	// A document without spec_version is treated as v1 (back-compat
	// with documents authored before v2 existed).
	yaml := []byte("name: x\nagent: echo\ntmux:\n  headless: true\n")
	got, err := UnmarshalAnySpec(yaml)
	if err != nil {
		t.Fatalf("UnmarshalAnySpec: %v", err)
	}
	if got.Version != SpecVersionV1 {
		t.Errorf("expected V1 default routing, got %q", got.Version)
	}
}

func TestV2_UnmarshalAnySpec_RejectsFutureVersion(t *testing.T) {
	yaml := []byte("spec_version: v99\nname: x\nagent: echo\ntmux:\n  headless: true\n")
	_, err := UnmarshalAnySpec(yaml)
	if err == nil {
		t.Fatal("expected unsupported-version rejection")
	}
	if !strings.Contains(err.Error(), "v99") {
		t.Errorf("error should name the bad version; got: %v", err)
	}
}

// ─── v1 + cmux/zmx must reject (forward-incompatibility) ────────────

func TestV2_V1Spec_WithCmuxKeyRejected(t *testing.T) {
	// v1's SpawnSpec struct has no `cmux:` field, so KnownFields=true
	// catches the field as unknown at parse time. This is the
	// designed-in forward-incompatibility: v1 binaries do not
	// silently downgrade a v2-shaped document.
	yaml := []byte("spec_version: v1\nname: x\nagent: echo\ncmux:\n  verify: true\n")
	_, err := UnmarshalSpec(yaml)
	if err == nil {
		t.Fatal("expected unknown-field error for v1 + cmux block")
	}
	if !strings.Contains(err.Error(), "cmux") {
		t.Errorf("error should name the cmux field; got: %v", err)
	}
}

func TestV2_V1Spec_WithZmxKeyRejected(t *testing.T) {
	yaml := []byte("spec_version: v1\nname: x\nagent: echo\nzmx:\n  session_name: foo\n")
	_, err := UnmarshalSpec(yaml)
	if err == nil {
		t.Fatal("expected unknown-field error for v1 + zmx block")
	}
	if !strings.Contains(err.Error(), "zmx") {
		t.Errorf("error should name the zmx field; got: %v", err)
	}
}

func TestV2_AnySpec_V1Plus_CmuxRejected(t *testing.T) {
	// Via the version-aware dispatcher, the v1 unknown-field rejection
	// still applies — the dispatcher routes to UnmarshalSpec, which
	// has KnownFields=true.
	yaml := []byte("spec_version: v1\nname: x\nagent: echo\nzmx:\n  session_name: foo\n")
	_, err := UnmarshalAnySpec(yaml)
	if err == nil {
		t.Fatal("expected rejection of v1+zmx via dispatcher")
	}
}

// ─── v2 WorkerHandle ────────────────────────────────────────────────

func TestV2_WorkerHandle_RoundTrip_Cmux(t *testing.T) {
	in := &WorkerHandleV2{
		Name:      "engineer",
		Agent:     AgentCodex,
		Session:   "engineer",
		CreatedAt: time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC),
		Executor:  "cmux",
		PaneID:    "surface:30",
		Status:    "ready",
	}
	data, err := MarshalHandleV2(in)
	if err != nil {
		t.Fatalf("MarshalHandleV2: %v", err)
	}
	out, err := UnmarshalHandleV2(data)
	if err != nil {
		t.Fatalf("UnmarshalHandleV2: %v\nyaml:\n%s", err, data)
	}
	if out.Executor != "cmux" || out.PaneID != "surface:30" {
		t.Errorf("Executor/PaneID lost: executor=%q pane_id=%q", out.Executor, out.PaneID)
	}
	if err := ValidateHandleV2(out); err != nil {
		t.Errorf("ValidateHandleV2 on cmux handle: %v", err)
	}
}

func TestV2_WorkerHandle_RoundTrip_Zmx(t *testing.T) {
	in := &WorkerHandleV2{
		Name:      "engineer",
		Agent:     AgentClaudeCode,
		CreatedAt: time.Now(),
		Executor:  "zmx",
		PaneID:    "engineer-z", // session name lives in PaneID
		Status:    "ready",
	}
	data, err := MarshalHandleV2(in)
	if err != nil {
		t.Fatalf("MarshalHandleV2: %v", err)
	}
	out, err := UnmarshalHandleV2(data)
	if err != nil {
		t.Fatalf("UnmarshalHandleV2: %v", err)
	}
	if err := ValidateHandleV2(out); err != nil {
		t.Errorf("ValidateHandleV2 on zmx handle: %v", err)
	}
}

func TestV2_WorkerHandle_CmuxRequiresPaneID(t *testing.T) {
	h := &WorkerHandleV2{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "cmux",
		// PaneID empty
		Status: "ready",
	}
	err := ValidateHandleV2(h)
	if err == nil {
		t.Fatal("expected validation failure for cmux executor with empty pane_id")
	}
	if !strings.Contains(err.Error(), "pane_id") {
		t.Errorf("error should mention pane_id; got: %v", err)
	}
}

func TestV2_WorkerHandle_ZmxRequiresPaneID(t *testing.T) {
	h := &WorkerHandleV2{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "zmx",
		Status:    "ready",
	}
	if err := ValidateHandleV2(h); err == nil {
		t.Fatal("expected validation failure for zmx executor with empty pane_id")
	}
}

func TestV2_WorkerHandle_ExecutorEnumExtended(t *testing.T) {
	for _, exec := range []string{"tmux", "cf-worker", "cf-durable-object", "cmux", "zmx"} {
		t.Run(exec, func(t *testing.T) {
			h := &WorkerHandleV2{
				Name:      "x",
				Agent:     AgentEcho,
				CreatedAt: time.Now(),
				Executor:  exec,
				PaneID:    "loc",
				Status:    "ready",
			}
			// cf-worker / cf-durable-object don't require pane_id in v1.
			// Mirror that in v2: only tmux / cmux / zmx use pane_id.
			if exec == "cf-worker" || exec == "cf-durable-object" {
				h.PaneID = ""
				h.ID = "https://example.com"
			}
			if err := ValidateHandleV2(h); err != nil {
				t.Errorf("v2 executor %q failed validation: %v", exec, err)
			}
		})
	}
}

func TestV2_WorkerHandle_BadExecutorRejected(t *testing.T) {
	h := &WorkerHandleV2{
		Name:      "x",
		Agent:     AgentEcho,
		CreatedAt: time.Now(),
		Executor:  "bogus",
		PaneID:    "loc",
		Status:    "ready",
	}
	err := ValidateHandleV2(h)
	if err == nil {
		t.Fatal("expected validation failure for unknown executor")
	}
}

// ─── Schemas (v2 publish artifacts) ─────────────────────────────────

func TestV2_SpecSchemaContainsNewBlocks(t *testing.T) {
	data, err := SpecSchemaV2()
	if err != nil {
		t.Fatalf("SpecSchemaV2: %v", err)
	}
	body := string(data)
	for _, want := range []string{"CmuxBlock", "ZmxBlock", "TmuxBlockV2", "spawn-spec.v2.json", `"SpawnSpec v2"`} {
		if !strings.Contains(body, want) {
			t.Errorf("v2 schema missing %q", want)
		}
	}
}

func TestV2_HandleSchemaTitled(t *testing.T) {
	data, err := HandleSchemaV2()
	if err != nil {
		t.Fatalf("HandleSchemaV2: %v", err)
	}
	body := string(data)
	if !strings.Contains(body, `"WorkerHandle v2"`) || !strings.Contains(body, "worker-handle.v2.json") {
		t.Errorf("v2 handle schema title/id missing; got first 200 bytes:\n%s", body[:min(200, len(body))])
	}
}
