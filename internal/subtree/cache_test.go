package subtree

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

func TestFileCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache := NewFileCache(dir)

	a := &AppliedSubtree{
		SpecVersion:  SpecVersion,
		Name:         "demo",
		AppliedAt:    time.Unix(1700000000, 0).UTC(),
		ResolvedNATS: "nats://127.0.0.1:4222",
		Topology: Topology{
			SpecVersion: SpecVersion,
			Name:        "demo",
			Sesh:        SeshSection{Existing: "nats://127.0.0.1:4222"},
			Workers: []WorkerEntry{
				{SpawnSpec: spawnspec.SpawnSpec{
					SpecVersion: spawnspec.SpecVersion,
					Name:        "w1",
					Agent:       spawnspec.AgentClaudeCode,
					Tmux:        &spawnspec.TmuxBlock{Headless: true},
				}},
			},
		},
	}

	if err := cache.Write(a); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// File should exist at the documented path.
	p := filepath.Join(dir, "demo.applied.yaml")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected %s to exist: %v", p, err)
	}

	got, err := cache.Read("demo")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if got.Name != "demo" {
		t.Errorf("Name mismatch: %q", got.Name)
	}
	if len(got.Topology.Workers) != 1 || got.Topology.Workers[0].Name != "w1" {
		t.Errorf("Workers round-trip wrong: %+v", got.Topology.Workers)
	}

	names, err := cache.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "demo" {
		t.Errorf("List = %v, want [demo]", names)
	}

	if err := cache.Delete("demo"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("Delete left file behind: err=%v", err)
	}
}

func TestFileCacheReadMissing(t *testing.T) {
	dir := t.TempDir()
	cache := NewFileCache(dir)
	if _, err := cache.Read("absent"); err == nil {
		t.Fatal("expected error reading missing subtree")
	}
}

func TestFileCacheListEmpty(t *testing.T) {
	dir := t.TempDir()
	cache := NewFileCache(dir)
	names, err := cache.List()
	if err != nil {
		t.Fatalf("List on fresh dir: %v", err)
	}
	if len(names) != 0 {
		t.Errorf("expected empty list, got %v", names)
	}
}
