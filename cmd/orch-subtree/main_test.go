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

// Phase B wires the previously-deferred verbs (apply/status/destroy/
// watch) to live infrastructure. We can't run those against real NATS
// in a unit test, so the smoke check is: the verbs exist (no "unknown
// subcommand" error) and they fail in the expected way on an
// unreachable subtree (cache-miss for status/destroy/watch, parse
// error for apply on a non-existent file).
func TestRunWiredVerbsExist(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		verb    string
		args    []string
		wantSub string // substring expected in the error message
	}{
		// status / destroy / watch all read the cache first; an
		// unknown subtree name surfaces "not found in cache".
		{"status", []string{"--cache-dir", dir, "nope"}, "not found"},
		{"destroy", []string{"--cache-dir", dir, "nope"}, ""}, // destroy is idempotent on unknown subtree → nil err
		{"watch", []string{"--cache-dir", dir, "nope"}, "not found"},
		// apply requires a yaml path; bogus path errors at parse.
		{"apply", []string{"--cache-dir", dir, "/nonexistent/x.yaml"}, "no such file"},
	}
	for _, c := range cases {
		err := run(append([]string{c.verb}, c.args...))
		if c.wantSub == "" {
			if err != nil {
				t.Errorf("%s: expected nil error (idempotent no-op), got %v", c.verb, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected error containing %q, got nil", c.verb, c.wantSub)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSub) {
			t.Errorf("%s: error mismatch; want substring %q got %v", c.verb, c.wantSub, err)
		}
	}
}

// Sanity: unknown verbs still surface as "unknown subcommand" rather
// than being misrouted into apply/status/etc.
func TestRunUnknownVerb(t *testing.T) {
	err := run([]string{"bogus"})
	if err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("expected unknown subcommand error, got %v", err)
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
