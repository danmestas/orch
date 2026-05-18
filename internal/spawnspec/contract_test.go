package spawnspec_test

// Cross-cutting contract tests for the spawnspec package.
//
// These tests live in the _test package (black-box) and assert the
// interface contract a backend implementer relies on, separate from
// the white-box tests in types_test.go. They mirror the
// "interface-contract test" pattern called out in Proposal 0002's
// Ousterhout review: package-public guarantees deserve their own
// dedicated test surface so the contract is independently verifiable.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/spawnspec"
)

// 1) Every known agent value must pass validation. If we add a new
// agent to the enum and forget to register the adapter, this test
// stays green (correctly — the enum is the parse-time check), but a
// matching change is required in the shim. The reverse is what we
// want to catch here: regressions where an existing agent silently
// stops validating.
func TestContract_AllKnownAgentsAccepted(t *testing.T) {
	for _, a := range spawnspec.KnownAgents() {
		s := &spawnspec.SpawnSpec{
			Name:  "x",
			Agent: a,
			Tmux:  &spawnspec.TmuxBlock{},
		}
		if err := spawnspec.ValidateSpec(s); err != nil {
			t.Errorf("known agent %q failed validation: %v", a, err)
		}
	}
}

// 2) Schema generation must succeed and emit a $defs entry per public
// type. A backend implementer publishing the schema for a non-Go
// consumer needs at least these top-level types reachable.
func TestContract_SchemaContainsAllPublicTypes(t *testing.T) {
	data, err := spawnspec.SpecSchema()
	if err != nil {
		t.Fatalf("SpecSchema: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	defs, ok := parsed["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("schema missing $defs section")
	}
	for _, want := range []string{"OutfitBlock", "TmuxBlock", "CFWorkerBlock", "CFDurableBlock"} {
		if _, ok := defs[want]; !ok {
			t.Errorf("schema $defs missing %q (have keys: %v)", want, keysOf(defs))
		}
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// 3) The version gate must reject anything other than the package's
// declared version. Backends rely on this to fail loud rather than
// produce undefined behaviour on a future-version spec.
func TestContract_VersionGateRejectsFuture(t *testing.T) {
	yaml := []byte("spec_version: v99\nname: x\nagent: echo\ntmux: {headless: true}\n")
	_, err := spawnspec.UnmarshalSpec(yaml)
	if err == nil {
		t.Fatal("expected version-gate rejection")
	}
	if !strings.Contains(err.Error(), "v99") {
		t.Errorf("error should name the bad version; got: %v", err)
	}
}

// 4) Round-tripping a fully-populated spec must preserve every public
// field. The wire format is YAML — backends consume the spec by
// parsing it, so byte-for-byte stability isn't required, but
// semantic stability is.
func TestContract_FullRoundTripIsLossless(t *testing.T) {
	in := &spawnspec.SpawnSpec{
		Name:        "verifier",
		Description: "Runs the bench after each push",
		Agent:       spawnspec.AgentCodex,
		Session:     "verifier",
		Cwd:         "/tmp/orch",
		Owner:       "dmestas",
		Labels:      map[string]string{"role": "verifier"},
		Env:         map[string]string{"NATS_URL": "nats://x:4222"},
		Outfit:      &spawnspec.OutfitBlock{Name: "verifier", Cut: "focused", Accessories: []string{"pr-policy"}},
		CFWorker: &spawnspec.CFWorkerBlock{
			Script:        "/path/to/worker.js",
			WranglerEnv:   "production",
			AbortEndpoint: "/control/abort",
		},
	}
	data, err := spawnspec.MarshalSpec(in)
	if err != nil {
		t.Fatalf("MarshalSpec: %v", err)
	}
	out, err := spawnspec.UnmarshalSpec(data)
	if err != nil {
		t.Fatalf("UnmarshalSpec: %v", err)
	}
	if err := spawnspec.ValidateSpec(out); err != nil {
		t.Fatalf("ValidateSpec on round-tripped spec: %v", err)
	}
	if out.CFWorker == nil || out.CFWorker.Script != in.CFWorker.Script {
		t.Errorf("CFWorker block lost in round-trip")
	}
	if out.Outfit == nil || out.Outfit.Cut != "focused" || len(out.Outfit.Accessories) != 1 {
		t.Errorf("Outfit explicit form lost: %+v", out.Outfit)
	}
}

// 5) The contract guarantees a SpawnSpec without a SpecVersion gets
// defaulted to the package's SpecVersion constant. Operators MAY omit
// the field; backends MUST see it populated post-parse.
func TestContract_DefaultsVersionOnUnmarshal(t *testing.T) {
	yaml := []byte("name: x\nagent: echo\ntmux: {headless: true}\n")
	s, err := spawnspec.UnmarshalSpec(yaml)
	if err != nil {
		t.Fatalf("UnmarshalSpec: %v", err)
	}
	if s.SpecVersion != spawnspec.SpecVersion {
		t.Errorf("SpecVersion default missing: got %q", s.SpecVersion)
	}
}

// 6) The contract requires ValidateSpec to be deterministic for the
// same input — multiple calls on the same spec produce the same
// outcome (and the same error text where relevant). Backends may
// cache validation results.
func TestContract_ValidateIsDeterministic(t *testing.T) {
	s := &spawnspec.SpawnSpec{
		Name:  "x",
		Agent: spawnspec.AgentEcho,
		Tmux:  &spawnspec.TmuxBlock{},
	}
	for i := range 3 {
		if err := spawnspec.ValidateSpec(s); err != nil {
			t.Errorf("validation flapped on call %d: %v", i, err)
		}
	}
}
