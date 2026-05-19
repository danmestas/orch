package subtree

import (
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/spawnspec"
)

const minimalYAML = `
name: bench-fleet
description: "5-worker fleet for bench"
sesh:
  existing: nats://127.0.0.1:58413
workers:
  - name: lead-engineer
    agent: claude-code
    tmux:
      headless: true
`

func TestParseMinimal(t *testing.T) {
	top, err := ParseBytes([]byte(minimalYAML))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	if top.Name != "bench-fleet" {
		t.Errorf("Name = %q, want bench-fleet", top.Name)
	}
	if top.SpecVersion != SpecVersion {
		t.Errorf("SpecVersion = %q, want %q", top.SpecVersion, SpecVersion)
	}
	if top.Sesh.Existing != "nats://127.0.0.1:58413" {
		t.Errorf("Sesh.Existing = %q", top.Sesh.Existing)
	}
	if len(top.Workers) != 1 {
		t.Fatalf("Workers = %d, want 1", len(top.Workers))
	}
	w := top.Workers[0]
	if w.Name != "lead-engineer" {
		t.Errorf("worker name = %q", w.Name)
	}
	if w.SpawnSpec.SpecVersion != spawnspec.SpecVersion {
		t.Errorf("worker spec_version = %q, want auto-fill to %q",
			w.SpawnSpec.SpecVersion, spawnspec.SpecVersion)
	}
	if w.Tmux == nil || !w.Tmux.Headless {
		t.Errorf("worker tmux block not parsed: %+v", w.Tmux)
	}
}

func TestParseUnknownField(t *testing.T) {
	bad := `
name: x
sesh: { existing: "nats://localhost" }
worker:
  - name: y
`
	if _, err := ParseBytes([]byte(bad)); err == nil {
		t.Fatal("expected error on unknown field, got nil")
	}
}

func TestParseEmpty(t *testing.T) {
	if _, err := ParseBytes(nil); err == nil {
		t.Fatal("expected error on empty document, got nil")
	}
	if _, err := ParseBytes([]byte("   \n  \n")); err == nil {
		t.Fatal("expected error on whitespace-only document, got nil")
	}
}

func TestParseVersionMismatch(t *testing.T) {
	bad := `
spec_version: v99
name: x
sesh: { existing: "nats://localhost" }
`
	_, err := ParseBytes([]byte(bad))
	if err == nil || !strings.Contains(err.Error(), "unsupported spec_version") {
		t.Fatalf("want unsupported spec_version error, got %v", err)
	}
}

func TestResolveEnv(t *testing.T) {
	src := `
name: x
sesh:
  existing: $MY_NATS_URL
workers:
  - name: w
    agent: claude-code
    cwd: $MY_CWD
    env:
      ORCH_OWNER: $MY_OWNER
    tmux: { headless: true }
`
	top, err := ParseBytes([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	ResolveEnv(top, func(k string) string {
		return map[string]string{
			"MY_NATS_URL": "nats://test:1234",
			"MY_CWD":      "/tmp/x",
			"MY_OWNER":    "alice",
		}[k]
	})
	if top.Sesh.Existing != "nats://test:1234" {
		t.Errorf("Sesh.Existing not expanded: %q", top.Sesh.Existing)
	}
	if top.Workers[0].SpawnSpec.Cwd != "/tmp/x" {
		t.Errorf("worker cwd not expanded: %q", top.Workers[0].SpawnSpec.Cwd)
	}
	if top.Workers[0].SpawnSpec.Env["ORCH_OWNER"] != "alice" {
		t.Errorf("worker env not expanded: %v", top.Workers[0].SpawnSpec.Env)
	}
}

func TestResolveEnvMissingVarsExpandEmpty(t *testing.T) {
	src := `
name: x
sesh: { existing: "$MISSING" }
`
	top, _ := ParseBytes([]byte(src))
	ResolveEnv(top, func(string) string { return "" })
	if top.Sesh.Existing != "" {
		t.Errorf("missing var should expand to empty; got %q", top.Sesh.Existing)
	}
}
