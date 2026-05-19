package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const validFixture = `
name: bench-fleet
sesh:
  existing: nats://127.0.0.1:58413
workers:
  - name: lead-engineer
    agent: claude-code
    tmux: { headless: true }
  - name: verifier
    agent: codex
    tmux: { headless: true }
state:
  tasks:
    - scope: workflow
      scope-id: e2ecafe1
      title: seed task
`

const invalidFixture = `
name: bench-fleet
sesh: {}
workers:
  - name: x
    agent: claude-code
    tmux: { headless: true }
`

func TestRunValidateOK(t *testing.T) {
	path := writeFixture(t, validFixture)
	if err := run([]string{"validate", path}); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestRunValidateBad(t *testing.T) {
	path := writeFixture(t, invalidFixture)
	err := run([]string{"validate", path})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected errInvalid; got %v", err)
	}
}

func TestRunListEmpty(t *testing.T) {
	dir := t.TempDir()
	if err := run([]string{"list", "--cache-dir", dir}); err != nil {
		t.Fatalf("list: %v", err)
	}
}

func TestRunDiffFromScratch(t *testing.T) {
	path := writeFixture(t, validFixture)
	dir := t.TempDir()
	if err := run([]string{"diff", "--cache-dir", dir, path}); err != nil {
		t.Fatalf("diff: %v", err)
	}
}

func TestRunDeferredVerb(t *testing.T) {
	for _, verb := range []string{"apply", "status", "destroy", "watch"} {
		err := run([]string{verb, "nope"})
		if err == nil || !strings.Contains(err.Error(), "Phase B") {
			t.Errorf("%s: expected Phase B not-implemented error, got %v", verb, err)
		}
	}
}

func writeFixture(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fleet.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return p
}
