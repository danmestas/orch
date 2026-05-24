package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadFixtureWorkers verifies the integration-test seam: when
// ORCH_REGISTRY_FIXTURE_FILE is set, snapshotOnce reads from JSON
// instead of dialling NATS. The fixture shape matches what the
// bash integration tests in test/test-orch-observer-role.sh emit.
func TestLoadFixtureWorkers(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fixture.json")
	body := `[
		{
			"pane_id": "%500",
			"instance_id": "stub-inst",
			"name": "pct500",
			"role": "observer",
			"agent": "claude-code",
			"cwd": "/tmp",
			"alive": true,
			"subjects": {"prompt": "agents.prompt.stub.fake.pct500"},
			"metadata": {"pane_id": "%500", "role": "observer"}
		},
		{
			"pane_id": "%501",
			"role": "worker",
			"agent": "claude-code",
			"session": "engineer",
			"subjects": {"prompt": "agents.prompt.stub.fake.pct501"}
		}
	]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	workers, err := loadFixtureWorkers(path)
	if err != nil {
		t.Fatalf("loadFixtureWorkers: %v", err)
	}
	if len(workers) != 2 {
		t.Fatalf("len = %d, want 2", len(workers))
	}
	// Row 1: observer, name populated from fixture.
	if workers[0].PaneID != "%500" || workers[0].Role != "observer" || workers[0].Name != "pct500" {
		t.Errorf("row 0: %+v", workers[0])
	}
	if workers[0].Subjects.Prompt != "agents.prompt.stub.fake.pct500" {
		t.Errorf("row 0 prompt subject: %q", workers[0].Subjects.Prompt)
	}
	// Row 2: name absent → fall back to session label.
	if workers[1].Name != "engineer" {
		t.Errorf("row 1 name fallback: %q want %q", workers[1].Name, "engineer")
	}
	// Default-role inference when missing — bash fixtures often drop role.
	if workers[1].Role != "worker" {
		t.Errorf("row 1 role: %q want worker", workers[1].Role)
	}
}

func TestLoadFixtureWorkers_NameFallbackFromPaneID(t *testing.T) {
	// No name, no session → fallback to pct-form of pane id.
	tmp := t.TempDir()
	path := filepath.Join(tmp, "fixture.json")
	body := `[{"pane_id":"%42","agent":"claude-code","subjects":{"prompt":"x"}}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	workers, err := loadFixtureWorkers(path)
	if err != nil {
		t.Fatalf("loadFixtureWorkers: %v", err)
	}
	if len(workers) != 1 {
		t.Fatalf("len = %d, want 1", len(workers))
	}
	if workers[0].Name != "pct42" {
		t.Errorf("name fallback: %q want pct42", workers[0].Name)
	}
}
