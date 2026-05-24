package tmux_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/persistence"
	tmuxengine "github.com/danmestas/orch/internal/persistence/tmux"
)

// Compile-time assertion that *Engine satisfies persistence.Engine.
var _ persistence.Engine = (*tmuxengine.Engine)(nil)

func TestEngineName(t *testing.T) {
	e := tmuxengine.New("/nonexistent")
	if e.Name() != "tmux" {
		t.Errorf("Name() = %q, want %q", e.Name(), "tmux")
	}
}

func TestEngineStartMissingScript(t *testing.T) {
	e := tmuxengine.New(t.TempDir())
	_, err := e.Start(persistence.StartSpec{Agent: "claude", Cwd: "/tmp", Slug: "x"})
	if err == nil {
		t.Fatal("Start with missing spawn script: want error, got nil")
	}
	if !strings.Contains(err.Error(), "spawn script not found") {
		t.Errorf("Start err message lacks 'spawn script not found': %v", err)
	}
}

// TestEngineStartViaMockScript wires a stand-in spawn script that
// echoes a tmux-shaped pane id. Validates that the engine:
//
//  1. Honors the WithSpawnScript override.
//  2. Propagates SpawnSpec fields as env vars.
//  3. Parses stdout into a Handle with the right Locator.
//  4. Returns the slug as the Handle.ID.
func TestEngineStartViaMockScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "spawn.sh")
	// Mock script captures env to a sidecar file and echoes a fake
	// pane id. Bash is invoked by the engine via `bash <script>`.
	body := `#!/usr/bin/env bash
set -euo pipefail
# Dump env for the test to inspect.
{
  echo "AGENT=$AGENT"
  echo "CWD=$CWD"
  echo "ROLE=$ROLE"
  echo "HEADLESS=$HEADLESS"
  echo "SLUG_EXPORTS=$SLUG_EXPORTS"
} > "$DUMP_FILE"
echo "%42"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}
	dump := filepath.Join(dir, "dump.txt")

	e := tmuxengine.New(dir, tmuxengine.WithSpawnScript(script))
	spec := persistence.StartSpec{
		Slug:     "alpha",
		Agent:    "claude",
		Cwd:      "/tmp/work",
		Role:     "worker",
		Headless: true,
		Env:      map[string]string{"DUMP_FILE": dump},
	}
	h, err := e.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if h.ID() != "alpha" {
		t.Errorf("Handle.ID() = %q, want %q", h.ID(), "alpha")
	}
	if h.Locator() != "%42" {
		t.Errorf("Handle.Locator() = %q, want %q", h.Locator(), "%42")
	}

	got, err := os.ReadFile(dump)
	if err != nil {
		t.Fatalf("read dump: %v", err)
	}
	for _, want := range []string{
		"AGENT=claude",
		"CWD=/tmp/work",
		"ROLE=worker",
		"HEADLESS=1",
		"SLUG_EXPORTS= export ORCH_INSTANCE_ID='alpha';",
	} {
		if !strings.Contains(string(got), want) {
			t.Errorf("env dump missing %q; got:\n%s", want, got)
		}
	}
}

func TestEngineStartRejectsNonPaneOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "spawn.sh")
	body := `#!/usr/bin/env bash
echo "not-a-pane"
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	e := tmuxengine.New(dir, tmuxengine.WithSpawnScript(script))
	_, err := e.Start(persistence.StartSpec{Slug: "x", Agent: "claude", Cwd: "/tmp"})
	if err == nil {
		t.Fatal("Start with non-pane output: want error, got nil")
	}
	if !strings.Contains(err.Error(), "non-pane-id output") {
		t.Errorf("Start err lacks 'non-pane-id output': %v", err)
	}
}

func TestEngineStartRejectsEmptyOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "spawn.sh")
	body := `#!/usr/bin/env bash
exit 0
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	e := tmuxengine.New(dir, tmuxengine.WithSpawnScript(script))
	_, err := e.Start(persistence.StartSpec{Slug: "x", Agent: "claude", Cwd: "/tmp"})
	if err == nil {
		t.Fatal("Start with empty output: want error, got nil")
	}
	if !strings.Contains(err.Error(), "empty pane id") {
		t.Errorf("Start err lacks 'empty pane id': %v", err)
	}
}

func TestEngineStartPropagatesScriptFailure(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "spawn.sh")
	body := `#!/usr/bin/env bash
echo "spawn-bad" >&2
exit 42
`
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write mock script: %v", err)
	}

	e := tmuxengine.New(dir, tmuxengine.WithSpawnScript(script))
	_, err := e.Start(persistence.StartSpec{Slug: "x", Agent: "claude", Cwd: "/tmp"})
	if err == nil {
		t.Fatal("Start with rc=42 script: want error, got nil")
	}
}

func TestEngineAttachReturnsNotFound(t *testing.T) {
	e := tmuxengine.New(t.TempDir())
	_, err := e.Attach("nonexistent")
	if err == nil {
		t.Fatal("Attach: want error, got nil")
	}
	if !errors.Is(err, persistence.ErrNotFound) {
		t.Errorf("Attach err = %v, want errors.Is ErrNotFound", err)
	}
}

func TestEngineAttachEmptySlug(t *testing.T) {
	e := tmuxengine.New(t.TempDir())
	_, err := e.Attach("")
	if err == nil {
		t.Fatal("Attach(\"\"): want error, got nil")
	}
}

func TestEngineList(t *testing.T) {
	e := tmuxengine.New(t.TempDir())
	got, err := e.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() = %d handles, want 0 in Phase A", len(got))
	}
}
