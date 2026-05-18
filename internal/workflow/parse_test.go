package workflow

import "testing"

func TestParseBytes_minimalValid(t *testing.T) {
	src := []byte(`name: simple
nodes:
  - id: plan
    prompt: "do the thing"
`)
	wf, err := ParseBytes(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.Name != "simple" {
		t.Fatalf("want name=simple, got %q", wf.Name)
	}
	if got := len(wf.Nodes); got != 1 {
		t.Fatalf("want 1 node, got %d", got)
	}
	if wf.Nodes[0].Kind() != KindPrompt {
		t.Fatalf("want KindPrompt, got %s", wf.Nodes[0].Kind())
	}
	if wf.Nodes[0].SourceLine == 0 {
		t.Errorf("want SourceLine populated, got 0")
	}
}

func TestParseBytes_rejectsUnknownField(t *testing.T) {
	// Strict decoding — `nodez:` is a typo, must fail at parse, not silently dropped.
	src := []byte(`name: typo
nodez:
  - id: plan
    prompt: hi
`)
	if _, err := ParseBytes(src); err == nil {
		t.Fatalf("expected unknown-field error for `nodez:`, got nil")
	}
}

func TestParseBytes_rejectsEmpty(t *testing.T) {
	if _, err := ParseBytes([]byte("")); err == nil {
		t.Fatalf("expected error on empty input")
	}
}

func TestParseBytes_rejectsTopLevelSequence(t *testing.T) {
	src := []byte(`- id: foo
  prompt: bar
`)
	if _, err := ParseBytes(src); err == nil {
		t.Fatalf("expected error: top level must be mapping")
	}
}

func TestParseBytes_loopBody(t *testing.T) {
	src := []byte(`name: loops
nodes:
  - id: implement
    loop:
      prompt: "iterate"
      until: ALL_TASKS_COMPLETE
      max_iterations: 5
      fresh_context: true
`)
	wf, err := ParseBytes(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	n := wf.Nodes[0]
	if n.Loop == nil {
		t.Fatalf("expected loop body")
	}
	if n.Loop.Until != "ALL_TASKS_COMPLETE" {
		t.Errorf("want until=ALL_TASKS_COMPLETE, got %q", n.Loop.Until)
	}
	if n.Loop.MaxIterations != 5 {
		t.Errorf("want max_iterations=5, got %d", n.Loop.MaxIterations)
	}
	if !n.Loop.FreshContext {
		t.Errorf("want fresh_context=true")
	}
}

func TestParseBytes_spawnBody(t *testing.T) {
	src := []byte(`name: spawning
nodes:
  - id: spawn-verifier
    spawn:
      name: verifier
      agent: claude-code
      tmux:
        headless: true
      outfit:
        bundle: backend/verifying
`)
	wf, err := ParseBytes(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wf.Nodes[0].Spawn == nil || wf.Nodes[0].Spawn.Name != "verifier" {
		t.Fatalf("want Spawn.Name=verifier")
	}
}
