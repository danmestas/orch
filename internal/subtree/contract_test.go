package subtree

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// Contract tests pin down the cross-cutting invariants that Ousterhout's
// review surfaced as load-bearing properties of Proposal 0006:
//
//  1. Apply runs phases in strict order (Parse → Resolve → Spawn → Seed
//     → Persist); a failure in phase N prevents phase N+1.
//  2. Apply is idempotent on phase boundaries: re-running after a
//     partial failure only does what's still missing.
//  3. Persist only writes the cache after spawn + seed succeed; a
//     failure mid-spawn must not leave a "we succeeded" cache record.
//  4. Destroy preserves KV state by default (PurgeState=false).
//  5. Sesh discriminator XOR is enforced at validate, not at apply
//     (defense in depth — Apply re-validates).

// recordingEngine wires fakes that log every call so the test can
// assert ordering and counts without coupling to internal calls.
type recordingEngine struct {
	t       *testing.T
	calls   []string
	mu      sync.Mutex
	spawned []string
	seeded  []string
}

func (r *recordingEngine) note(s string) {
	r.mu.Lock()
	r.calls = append(r.calls, s)
	r.mu.Unlock()
}

type fakeSesh struct {
	r *recordingEngine
}

func (f *fakeSesh) Resolve(_ context.Context, _ SeshSection, name string) (ResolvedSesh, error) {
	f.r.note("resolve-sesh:" + name)
	return ResolvedSesh{URL: "nats://test:1234"}, nil
}

type fakeRegistry struct {
	r     *recordingEngine
	alive map[string]struct{}
}

func (f *fakeRegistry) AliveByName(_ context.Context) (map[string]struct{}, error) {
	f.r.note("registry-snapshot")
	return f.alive, nil
}

type fakeSpawner struct {
	r       *recordingEngine
	failOn  string
	handles map[string]*spawnspec.WorkerHandle
}

func (f *fakeSpawner) Spawn(_ context.Context, s spawnspec.SpawnSpec, _ ResolvedSesh) (*spawnspec.WorkerHandle, error) {
	f.r.note("spawn:" + s.Name)
	f.r.spawned = append(f.r.spawned, s.Name)
	if s.Name == f.failOn {
		return nil, errors.New("forced spawn failure")
	}
	if h, ok := f.handles[s.Name]; ok {
		return h, nil
	}
	return &spawnspec.WorkerHandle{
		SpecVersion: spawnspec.SpecVersion,
		Name:        s.Name,
		Agent:       s.Agent,
		Executor:    "tmux",
		PaneID:      "%99",
		Status:      "ready",
		CreatedAt:   time.Now(),
	}, nil
}

type fakeSeeder struct {
	r      *recordingEngine
	failOn string
}

func (f *fakeSeeder) SeedTask(_ context.Context, t TaskSeed, _ ResolvedSesh) error {
	f.r.note("seed-task:" + t.Title)
	f.r.seeded = append(f.r.seeded, "task:"+t.Title)
	if t.Title == f.failOn {
		return errors.New("forced seed failure")
	}
	return nil
}

func (f *fakeSeeder) SeedGoal(_ context.Context, g GoalSeed, _ ResolvedSesh) error {
	f.r.note("seed-goal:" + g.Objective)
	f.r.seeded = append(f.r.seeded, "goal:"+g.Objective)
	if g.Objective == f.failOn {
		return errors.New("forced seed failure")
	}
	return nil
}

type memCache struct {
	mu      sync.Mutex
	entries map[string]*AppliedSubtree
	writes  int
}

func newMemCache() *memCache { return &memCache{entries: map[string]*AppliedSubtree{}} }

func (m *memCache) Read(name string) (*AppliedSubtree, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.entries[name]
	if !ok {
		return nil, errors.New("not in cache")
	}
	return a, nil
}

func (m *memCache) Write(a *AppliedSubtree) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[a.Name] = a
	m.writes++
	return nil
}

func (m *memCache) Delete(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, name)
	return nil
}

func (m *memCache) List() ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.entries))
	for k := range m.entries {
		out = append(out, k)
	}
	return out, nil
}

func goodTopology() *Topology {
	return &Topology{
		SpecVersion: SpecVersion,
		Name:        "fleet",
		Sesh:        SeshSection{Existing: "nats://test:1234"},
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
				{Scope: "workflow", ScopeID: "abc", Title: "t1"},
				{Scope: "workflow", ScopeID: "abc", Title: "t2"},
			},
			Goals: []GoalSeed{
				{Scope: "workflow", ScopeID: "abc", Objective: "g1"},
			},
		},
	}
}

func newRecordingEngine(t *testing.T, alive map[string]struct{}) (*Engine, *recordingEngine, *memCache) {
	r := &recordingEngine{t: t}
	cache := newMemCache()
	eng := &Engine{
		Sesh:     &fakeSesh{r: r},
		Registry: &fakeRegistry{r: r, alive: alive},
		Spawner:  &fakeSpawner{r: r},
		Seeder:   &fakeSeeder{r: r},
		Cache:    cache,
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	return eng, r, cache
}

// Contract 1: phase ordering.
func TestApplyPhaseOrdering(t *testing.T) {
	eng, r, _ := newRecordingEngine(t, nil)
	if _, err := eng.Apply(context.Background(), goodTopology()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Resolve must come before any spawn; registry-snapshot before any
	// spawn; every spawn before any seed; every seed before nothing.
	idxOf := func(s string) int {
		for i, c := range r.calls {
			if c == s {
				return i
			}
		}
		return -1
	}
	if idxOf("resolve-sesh:fleet") > idxOf("spawn:a") {
		t.Errorf("resolve-sesh must precede spawn; got %v", r.calls)
	}
	if idxOf("registry-snapshot") > idxOf("spawn:a") {
		t.Errorf("registry-snapshot must precede spawn; got %v", r.calls)
	}
	if idxOf("spawn:b") > idxOf("seed-task:t1") {
		t.Errorf("all spawns must precede seeds; got %v", r.calls)
	}
	if idxOf("seed-task:t2") > idxOf("seed-goal:g1") {
		t.Errorf("tasks should be seeded before goals (per definition order); got %v", r.calls)
	}
}

// Contract 2: idempotency — already-alive workers are not re-spawned.
func TestApplyIdempotentOnLiveWorker(t *testing.T) {
	alive := map[string]struct{}{"a": {}}
	eng, r, _ := newRecordingEngine(t, alive)
	res, err := eng.Apply(context.Background(), goodTopology())
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(res.Spawned) != 1 || res.Spawned[0] != "b" {
		t.Errorf("expected only b to spawn; got %v", res.Spawned)
	}
	if len(res.AlreadyRunning) != 1 || res.AlreadyRunning[0] != "a" {
		t.Errorf("expected a in AlreadyRunning; got %v", res.AlreadyRunning)
	}
	// Sanity: spawn:a not in calls.
	for _, c := range r.calls {
		if c == "spawn:a" {
			t.Errorf("live worker a was re-spawned; calls=%v", r.calls)
		}
	}
}

// Contract 3: persist only runs after spawn + seed succeed.
func TestApplyDoesNotPersistOnSpawnFailure(t *testing.T) {
	eng, _, cache := newRecordingEngine(t, nil)
	eng.Spawner = &fakeSpawner{r: &recordingEngine{t: t}, failOn: "b"}
	_, err := eng.Apply(context.Background(), goodTopology())
	if err == nil {
		t.Fatal("expected spawn failure to propagate")
	}
	var pe *PhaseError
	if !errors.As(err, &pe) || pe.Phase != PhaseSpawnWorkers {
		t.Errorf("expected PhaseSpawnWorkers error; got %v", err)
	}
	if cache.writes != 0 {
		t.Errorf("cache must not be written on spawn failure; writes=%d", cache.writes)
	}
}

func TestApplyDoesNotPersistOnSeedFailure(t *testing.T) {
	eng, r, cache := newRecordingEngine(t, nil)
	eng.Seeder = &fakeSeeder{r: r, failOn: "t1"}
	_, err := eng.Apply(context.Background(), goodTopology())
	if err == nil {
		t.Fatal("expected seed failure to propagate")
	}
	var pe *PhaseError
	if !errors.As(err, &pe) || pe.Phase != PhaseSeedState {
		t.Errorf("expected PhaseSeedState error; got %v", err)
	}
	if cache.writes != 0 {
		t.Errorf("cache must not be written on seed failure; writes=%d", cache.writes)
	}
}

// Contract 4: destroy preserves KV state by default.
func TestDestroyPreservesStateByDefault(t *testing.T) {
	eng, r, cache := newRecordingEngine(t, nil)
	if _, err := eng.Apply(context.Background(), goodTopology()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, ok := cache.entries["fleet"]; !ok {
		t.Fatal("apply did not write cache")
	}
	killer := &fakeKiller{r: r}
	res, err := eng.Destroy(context.Background(), "fleet", killer, nil, DestroyOptions{})
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if !res.CacheRemoved {
		t.Errorf("CacheRemoved=false")
	}
	if _, ok := cache.entries["fleet"]; ok {
		t.Errorf("cache entry remained after destroy")
	}
	if len(res.WorkersKilled) != 2 {
		t.Errorf("expected 2 workers killed, got %d", len(res.WorkersKilled))
	}
}

func TestDestroyPurgeStateNotImplemented(t *testing.T) {
	eng, r, _ := newRecordingEngine(t, nil)
	if _, err := eng.Apply(context.Background(), goodTopology()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	killer := &fakeKiller{r: r}
	_, err := eng.Destroy(context.Background(), "fleet", killer, nil, DestroyOptions{PurgeState: true})
	if err == nil {
		t.Error("expected --purge-state to surface a clear not-implemented error")
	}
}

func TestDestroyIdempotentOnUnknownSubtree(t *testing.T) {
	eng, r, _ := newRecordingEngine(t, nil)
	killer := &fakeKiller{r: r}
	res, err := eng.Destroy(context.Background(), "never-applied", killer, nil, DestroyOptions{})
	if err != nil {
		t.Fatalf("destroy of unknown subtree should be no-op; got %v", err)
	}
	if res.CacheRemoved || len(res.WorkersKilled) > 0 {
		t.Errorf("expected empty result for never-applied subtree; got %+v", res)
	}
}

// Contract 5: apply re-validates (defense in depth — caller bypassing
// parse-time validation still gets caught).
func TestApplyRevalidates(t *testing.T) {
	eng, _, _ := newRecordingEngine(t, nil)
	top := goodTopology()
	top.Sesh = SeshSection{} // strip both legs of the XOR
	_, err := eng.Apply(context.Background(), top)
	if err == nil {
		t.Fatal("apply did not re-validate")
	}
	var pe *PhaseError
	if !errors.As(err, &pe) || pe.Phase != PhaseParse {
		t.Errorf("expected PhaseParse error; got %v", err)
	}
}

func TestStatusFromCache(t *testing.T) {
	eng, _, _ := newRecordingEngine(t, map[string]struct{}{"a": {}})
	if _, err := eng.Apply(context.Background(), goodTopology()); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Now ask for status with a different alive set — b should be missing.
	eng.Registry = &fakeRegistry{r: &recordingEngine{t: t}, alive: map[string]struct{}{"a": {}}}
	report, err := eng.Status(context.Background(), "fleet")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var missingB bool
	for _, w := range report.Workers {
		if w.Name == "b" && w.Missing {
			missingB = true
		}
	}
	if !missingB {
		t.Errorf("expected b to be missing; report=%+v", report)
	}
}

func TestListSorted(t *testing.T) {
	eng, _, _ := newRecordingEngine(t, nil)
	top1 := goodTopology()
	top1.Name = "zeta"
	if _, err := eng.Apply(context.Background(), top1); err != nil {
		t.Fatalf("apply zeta: %v", err)
	}
	top2 := goodTopology()
	top2.Name = "alpha"
	if _, err := eng.Apply(context.Background(), top2); err != nil {
		t.Fatalf("apply alpha: %v", err)
	}
	names, err := eng.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 2 || names[0] != "alpha" || names[1] != "zeta" {
		t.Errorf("List not sorted; got %v", names)
	}
}

func TestDiffFromScratch(t *testing.T) {
	eng, _, _ := newRecordingEngine(t, nil)
	entries, err := eng.Diff(goodTopology())
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	// Expect 2 worker adds + 2 task adds + 1 goal add.
	if len(entries) != 5 {
		t.Errorf("expected 5 entries on first-apply diff, got %d (%+v)", len(entries), entries)
	}
}

func TestDiffAddRemove(t *testing.T) {
	eng, _, _ := newRecordingEngine(t, nil)
	if _, err := eng.Apply(context.Background(), goodTopology()); err != nil {
		t.Fatalf("apply: %v", err)
	}
	next := goodTopology()
	// Remove b, add c.
	next.Workers = next.Workers[:1]
	next.Workers = append(next.Workers, WorkerEntry{
		SpawnSpec: spawnspec.SpawnSpec{
			SpecVersion: spawnspec.SpecVersion,
			Name:        "c",
			Agent:       spawnspec.AgentPi,
			Tmux:        &spawnspec.TmuxBlock{Headless: true},
		},
	})
	entries, err := eng.Diff(next)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	var addedC, removedB bool
	for _, e := range entries {
		if e.Kind == "worker" && e.Op == "add" && e.Name == "c" {
			addedC = true
		}
		if e.Kind == "worker" && e.Op == "remove" && e.Name == "b" {
			removedB = true
		}
	}
	if !addedC || !removedB {
		t.Errorf("expected add c + remove b; got %+v", entries)
	}
}

// fakeKiller for Destroy tests.
type fakeKiller struct {
	r      *recordingEngine
	killed []string
}

func (k *fakeKiller) Kill(_ context.Context, name string, _ *WorkerHandleRef) error {
	k.r.note("kill:" + name)
	k.killed = append(k.killed, name)
	return nil
}
