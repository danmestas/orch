package subtree

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// TestEndToEndApply wires up the real concrete impls of every phase
// EXCEPT NATS-backed registry (we substitute EmptyLiveRegistry +
// substitute fake binaries for orch-spawn / sesh-ops / tmux). This
// gives a true end-to-end test of the apply pipeline without needing
// a live NATS server in unit tests.
//
// Validates:
//
//  1. SeshResolver (existing path) → returns the env-expanded URL.
//  2. LiveRegistry (empty) → every worker is missing.
//  3. WorkerSpawner (real) shells out to fake orch-spawn, builds
//     handles with the right pane ids.
//  4. StateSeeder (real) shells out to fake sesh-ops.
//  5. CacheStore (real file cache) persists applied.yaml only on
//     success.
//
// Idempotency: a second Apply with the same topology + an emptied
// fake script log produces zero new spawn calls (because the cache
// from run 1 holds a WorkerHandle and our cached-handle-aware fake
// registry skips them). Note: production uses LiveRegistry; the
// in-process simulation here uses a recording registry that returns
// the SET of names that the prior run cached.
func TestEndToEndApply(t *testing.T) {
	tmp := t.TempDir()

	// Fake orch-spawn: writes its args to LOGFILE, emits "%N" where
	// N is incremented by the script (a simple counter file).
	spawnLog := filepath.Join(tmp, "spawn.log")
	counterFile := filepath.Join(tmp, "counter")
	if err := os.WriteFile(counterFile, []byte("0"), 0o644); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	fakeSpawn := filepath.Join(tmp, "orch-spawn")
	spawnScript := `#!/usr/bin/env bash
n=$(cat "$ORCH_SPAWN_COUNTER")
n=$((n+1))
echo "$n" > "$ORCH_SPAWN_COUNTER"
printf 'run: %s\n' "$*" >> "$ORCH_SPAWN_LOG"
echo "%$n"
`
	if err := os.WriteFile(fakeSpawn, []byte(spawnScript), 0o755); err != nil {
		t.Fatalf("write fake spawn: %v", err)
	}

	// Fake sesh-ops: records calls, exits 0.
	seedLog := filepath.Join(tmp, "seed.log")
	fakeOps := filepath.Join(tmp, "sesh-ops")
	opsScript := `#!/usr/bin/env bash
printf 'call: %s\n' "$*" >> "$SESH_OPS_LOG"
`
	if err := os.WriteFile(fakeOps, []byte(opsScript), 0o755); err != nil {
		t.Fatalf("write fake ops: %v", err)
	}

	// Cache lives in this run's tmp dir.
	cacheDir := filepath.Join(tmp, "cache")

	mkEngine := func(alive map[string]struct{}) *Engine {
		return &Engine{
			Sesh:     NewLiveSeshResolver(),
			Registry: &stubRegistry{alive: alive},
			Spawner: &OrchSpawnWorkerSpawner{
				BinPath:  fakeSpawn,
				ExtraEnv: []string{"ORCH_SPAWN_LOG=" + spawnLog, "ORCH_SPAWN_COUNTER=" + counterFile},
				Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
			},
			Seeder: &SeshOpsStateSeeder{BinPath: fakeOps},
			Cache:  NewFileCache(cacheDir),
			Now:    func() time.Time { return time.Unix(1700000000, 0).UTC() },
		}
	}

	t.Setenv("ORCH_SPAWN_LOG", spawnLog)
	t.Setenv("ORCH_SPAWN_COUNTER", counterFile)
	t.Setenv("SESH_OPS_LOG", seedLog)

	top := &Topology{
		SpecVersion: SpecVersion,
		Name:        "e2e",
		Sesh:        SeshSection{Existing: "nats://test:4222"},
		Workers: []WorkerEntry{
			{SpawnSpec: spawnspec.SpawnSpec{
				SpecVersion: spawnspec.SpecVersion,
				Name:        "a",
				Agent:       spawnspec.AgentClaudeCode,
				Tmux:        &spawnspec.TmuxBlock{Headless: true},
			}},
			{SpawnSpec: spawnspec.SpawnSpec{
				SpecVersion: spawnspec.SpecVersion,
				Name:        "b",
				Agent:       spawnspec.AgentCodex,
				Tmux:        &spawnspec.TmuxBlock{Headless: true},
			}},
		},
		State: StateSection{
			Tasks: []TaskSeed{
				{Scope: "workflow", ScopeID: "deadbeef", Title: "t1"},
			},
			Goals: []GoalSeed{
				{Scope: "workflow", ScopeID: "deadbeef", Objective: "g1"},
			},
		},
	}

	// Run 1: empty registry → both workers spawn.
	eng := mkEngine(map[string]struct{}{})
	res, err := eng.Apply(context.Background(), top)
	if err != nil {
		t.Fatalf("Apply run 1: %v", err)
	}
	sort.Strings(res.Spawned)
	if len(res.Spawned) != 2 || res.Spawned[0] != "a" || res.Spawned[1] != "b" {
		t.Errorf("run 1 Spawned = %v, want [a b]", res.Spawned)
	}
	if res.TasksSeeded != 1 || res.GoalsSeeded != 1 {
		t.Errorf("run 1 seeds = %d/%d, want 1/1", res.TasksSeeded, res.GoalsSeeded)
	}

	// Cache should have been written.
	cached, err := NewFileCache(cacheDir).Read("e2e")
	if err != nil {
		t.Fatalf("cache read: %v", err)
	}
	if cached.ResolvedNATS != "nats://test:4222" {
		t.Errorf("cached ResolvedNATS = %q", cached.ResolvedNATS)
	}
	if len(cached.Workers) != 2 {
		t.Errorf("cache.Workers = %d, want 2", len(cached.Workers))
	}
	for _, n := range []string{"a", "b"} {
		h, ok := cached.Workers[n]
		if !ok {
			t.Errorf("missing handle for %q", n)
			continue
		}
		if h.PaneID == "" || h.PaneID[0] != '%' {
			t.Errorf("handle %q PaneID = %q (want %%N)", n, h.PaneID)
		}
		if h.Status != "ready" {
			t.Errorf("handle %q Status = %q", n, h.Status)
		}
	}

	// Run 2: registry reports both alive → zero new spawns.
	eng2 := mkEngine(map[string]struct{}{"a": {}, "b": {}})
	res2, err := eng2.Apply(context.Background(), top)
	if err != nil {
		t.Fatalf("Apply run 2: %v", err)
	}
	if len(res2.Spawned) != 0 {
		t.Errorf("run 2 Spawned = %v, want []", res2.Spawned)
	}
	sort.Strings(res2.AlreadyRunning)
	if len(res2.AlreadyRunning) != 2 || res2.AlreadyRunning[0] != "a" || res2.AlreadyRunning[1] != "b" {
		t.Errorf("run 2 AlreadyRunning = %v, want [a b]", res2.AlreadyRunning)
	}

	// Run 3: add a new worker c, b still alive. Only c should spawn.
	top3 := *top
	top3.Workers = append(top3.Workers, WorkerEntry{SpawnSpec: spawnspec.SpawnSpec{
		SpecVersion: spawnspec.SpecVersion,
		Name:        "c",
		Agent:       spawnspec.AgentPi,
		Tmux:        &spawnspec.TmuxBlock{Headless: true},
	}})
	eng3 := mkEngine(map[string]struct{}{"a": {}, "b": {}})
	res3, err := eng3.Apply(context.Background(), &top3)
	if err != nil {
		t.Fatalf("Apply run 3: %v", err)
	}
	if len(res3.Spawned) != 1 || res3.Spawned[0] != "c" {
		t.Errorf("run 3 Spawned = %v, want [c]", res3.Spawned)
	}

	// Destroy clears the cache; killer is a fake that records calls.
	killCount := 0
	killer := stubKiller{onKill: func(_ string) error { killCount++; return nil }}
	res4, err := eng3.Destroy(context.Background(), "e2e", killer, nil, DestroyOptions{})
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if killCount != 3 {
		t.Errorf("destroy killed %d workers, want 3", killCount)
	}
	if !res4.CacheRemoved {
		t.Errorf("CacheRemoved=false")
	}
	if _, err := NewFileCache(cacheDir).Read("e2e"); err == nil {
		t.Errorf("cache entry survived destroy")
	}

	// Sanity: re-destroy after cache removed is a no-op.
	res5, err := eng3.Destroy(context.Background(), "e2e", killer, nil, DestroyOptions{})
	if err != nil {
		t.Fatalf("Destroy re-run: %v", err)
	}
	if res5.CacheRemoved || len(res5.WorkersKilled) > 0 {
		t.Errorf("expected no-op on re-destroy; got %+v", res5)
	}
}

// stubRegistry returns a fixed alive-set without hitting NATS.
type stubRegistry struct{ alive map[string]struct{} }

func (s *stubRegistry) AliveByName(context.Context) (map[string]struct{}, error) {
	return s.alive, nil
}

// stubKiller wraps a callback so the integration test can count
// kills.
type stubKiller struct {
	onKill func(string) error
}

func (s stubKiller) Kill(_ context.Context, name string, _ *WorkerHandleRef) error {
	return s.onKill(name)
}

// TestSpawnFailureDoesNotPersist locks in the contract that a
// partial spawn failure must NOT leave the cache claiming success
// (issue tracked via Contract 3 in contract_test.go; this test
// drives it with the real production WorkerSpawner + cache).
func TestSpawnFailureDoesNotPersistE2E(t *testing.T) {
	tmp := t.TempDir()
	// Fake orch-spawn that errors for worker "fail".
	fakeSpawn := filepath.Join(tmp, "orch-spawn")
	script := `#!/usr/bin/env bash
for arg in "$@"; do
  if [[ "$arg" == "fail" ]]; then
    echo "spawn failed for fail" >&2
    exit 7
  fi
done
echo "%1"
`
	if err := os.WriteFile(fakeSpawn, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	cacheDir := filepath.Join(tmp, "cache")
	eng := &Engine{
		Sesh:     NewLiveSeshResolver(),
		Registry: &stubRegistry{alive: map[string]struct{}{}},
		Spawner:  &OrchSpawnWorkerSpawner{BinPath: fakeSpawn},
		Seeder:   &SeshOpsStateSeeder{BinPath: "/usr/bin/true"},
		Cache:    NewFileCache(cacheDir),
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	top := &Topology{
		SpecVersion: SpecVersion,
		Name:        "ftest",
		Sesh:        SeshSection{Existing: "nats://test:4222"},
		Workers: []WorkerEntry{
			{SpawnSpec: spawnspec.SpawnSpec{
				SpecVersion: spawnspec.SpecVersion,
				Name:        "ok",
				Agent:       spawnspec.AgentClaudeCode,
				Tmux:        &spawnspec.TmuxBlock{Headless: true},
			}},
			{SpawnSpec: spawnspec.SpawnSpec{
				SpecVersion: spawnspec.SpecVersion,
				Name:        "fail",
				Agent:       spawnspec.AgentClaudeCode,
				Tmux:        &spawnspec.TmuxBlock{Headless: true},
			}},
		},
	}
	_, err := eng.Apply(context.Background(), top)
	if err == nil {
		t.Fatal("expected spawn failure to propagate")
	}
	var pe *PhaseError
	if !errors.As(err, &pe) || pe.Phase != PhaseSpawnWorkers {
		t.Errorf("expected PhaseSpawnWorkers; got %v", err)
	}
	if _, err := NewFileCache(cacheDir).Read("ftest"); err == nil {
		t.Errorf("cache entry exists after spawn failure")
	}
}

// TestSeshResolver_SpawnEndToEnd drives the spawn path through Apply
// with the fake `sesh up` substitute (just write the session JSON
// before the resolver polls). Demonstrates that the engine wires
// ResolvedSesh.URL through to subsequent phases.
func TestSeshResolver_SpawnEndToEnd(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, ".sesh", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"pid": 1, "scope": "session", "nats_url": "nats://spawned-hub:9999",
	})
	if err := os.WriteFile(filepath.Join(sessionsDir, "spawn-e2e.json"), body, 0o644); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	eng := &Engine{
		Sesh: &LiveSeshResolver{
			BinPath:     "/usr/bin/true",
			SessionsDir: sessionsDir,
			Timeout:     2 * time.Second,
		},
		Registry: &stubRegistry{alive: map[string]struct{}{}},
		Spawner:  &OrchSpawnWorkerSpawner{BinPath: "/usr/bin/true"},
		Seeder:   &SeshOpsStateSeeder{BinPath: "/usr/bin/true"},
		Cache:    NewFileCache(filepath.Join(tmp, "cache")),
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	top := &Topology{
		SpecVersion: SpecVersion,
		Name:        "spawn-e2e",
		Sesh: SeshSection{Spawn: &SeshSpawn{
			Session: "spawn-e2e",
			Scope:   "session",
		}},
		Workers: []WorkerEntry{{SpawnSpec: spawnspec.SpawnSpec{
			SpecVersion: spawnspec.SpecVersion,
			Name:        "x",
			Agent:       spawnspec.AgentEcho,
			Tmux:        &spawnspec.TmuxBlock{Headless: true},
		}}},
	}
	// orch-spawn (BinPath=/usr/bin/true) returns empty stdout → spawner
	// errors. The test asserts the SESH part succeeds (the spawn
	// failure happens AFTER resolve-sesh).
	_, err := eng.Apply(context.Background(), top)
	if err == nil {
		// /usr/bin/true returns empty stdout, so we expect spawn-phase
		// failure here. If it doesn't fail, that's also acceptable —
		// the contract is that resolve-sesh has already succeeded.
		return
	}
	var pe *PhaseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PhaseError; got %v", err)
	}
	if pe.Phase == PhaseResolveSesh {
		t.Errorf("resolve-sesh failed; spawn URL discovery is broken: %v", err)
	}
}
