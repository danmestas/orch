package subtree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// TestOrchSpawnWorkerSpawner_TmuxOnly asserts that non-tmux executors
// surface a clear error today. CF executors are Phase B+; this test
// pins the message so the operator knows what's missing.
func TestOrchSpawnWorkerSpawner_NonTmux(t *testing.T) {
	s := NewOrchSpawnWorkerSpawner()
	_, err := s.Spawn(context.Background(), spawnspec.SpawnSpec{
		Name:  "x",
		Agent: spawnspec.AgentClaudeCode,
		CFWorker: &spawnspec.CFWorkerBlock{
			Script: "x.ts",
		},
	}, ResolvedSesh{})
	if err == nil {
		t.Fatal("expected non-tmux executor error; got nil")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error %q does not mention tmux", err)
	}
}

// TestOrchSpawnWorkerSpawner_BuildsHandle drives a fake orch-spawn
// binary that just echoes a pane id. We verify the spawner translates
// the spec → flags correctly (by re-reading what the fake recorded)
// and that the WorkerHandle it returns has the expected shape.
func TestOrchSpawnWorkerSpawner_BuildsHandle(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "args.log")
	// Fake orch-spawn: writes its args (one per line) to LOGFILE,
	// echoes the pane id, exits 0. The script reads LOGFILE from
	// the environment so the test can inspect it.
	fake := filepath.Join(tmp, "orch-spawn")
	body := `#!/usr/bin/env bash
printf '%s\n' "$@" > "$ORCH_SPAWN_TEST_LOG"
echo "%42"
`
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}

	s := &OrchSpawnWorkerSpawner{
		BinPath:  fake,
		ExtraEnv: []string{"ORCH_SPAWN_TEST_LOG=" + logFile},
		Stderr:   nil, // discard
		Now:      func() time.Time { return time.Unix(1700000000, 0).UTC() },
	}
	spec := spawnspec.SpawnSpec{
		Name:    "lead-engineer",
		Agent:   spawnspec.AgentClaudeCode,
		Cwd:     "/work/x",
		Session: "fleet",
		Tmux:    &spawnspec.TmuxBlock{Headless: true, Verify: true, Role: "worker", Position: "right"},
		Outfit:  &spawnspec.OutfitBlock{Name: "backend", Cut: "executing", Accessories: []string{"pr-policy"}},
		Env:     map[string]string{"FOO": "bar"},
	}
	handle, err := s.Spawn(context.Background(), spec, ResolvedSesh{URL: "nats://test:4222"})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Handle shape.
	if handle.Name != "lead-engineer" {
		t.Errorf("Name = %q", handle.Name)
	}
	if handle.Agent != spawnspec.AgentClaudeCode {
		t.Errorf("Agent = %q", handle.Agent)
	}
	if handle.Executor != "tmux" {
		t.Errorf("Executor = %q", handle.Executor)
	}
	if handle.PaneID != "%42" {
		t.Errorf("PaneID = %q, want %%42", handle.PaneID)
	}
	if handle.Status != "ready" {
		t.Errorf("Status = %q", handle.Status)
	}
	if handle.Abort == nil ||
		handle.Abort.Kind != "tmux-send-keys" ||
		handle.Abort.Target != "%42" ||
		handle.Abort.Keys != "C-c" {
		t.Errorf("Abort wrong: %+v", handle.Abort)
	}

	// Arg translation. We check substrings so reordering doesn't
	// fail the test.
	logged, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	logged = []byte(strings.ReplaceAll(string(logged), "\n", " "))
	for _, want := range []string{
		"claude-code", // agent positional
		"--instance-id lead-engineer",
		"--force-slug",
		"--cwd /work/x",
		"--sesh-session fleet",
		"--headless",
		"--verify",
		"--position right",
		"--role worker",
		"--outfit backend",
		"--cut executing",
		"--accessory pr-policy",
	} {
		if !strings.Contains(string(logged), want) {
			t.Errorf("missing %q in args log: %s", want, logged)
		}
	}
}

// TestOrchSpawnWorkerSpawner_EmptyPaneIDErrors surfaces the
// failure path when orch-spawn exits 0 but produces no pane id (a
// regression we want to catch loudly rather than store an empty
// PaneID in the cache).
func TestOrchSpawnWorkerSpawner_EmptyPaneIDErrors(t *testing.T) {
	tmp := t.TempDir()
	fake := filepath.Join(tmp, "orch-spawn-empty")
	if err := os.WriteFile(fake, []byte("#!/usr/bin/env bash\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	s := &OrchSpawnWorkerSpawner{BinPath: fake, Stderr: nil}
	_, err := s.Spawn(context.Background(), spawnspec.SpawnSpec{
		Name:  "x",
		Agent: spawnspec.AgentClaudeCode,
		Tmux:  &spawnspec.TmuxBlock{Headless: true},
	}, ResolvedSesh{})
	if err == nil {
		t.Fatal("expected empty-pane error; got nil")
	}
	if !strings.Contains(err.Error(), "empty pane") {
		t.Errorf("error %q does not mention empty pane", err)
	}
}

// TestOrchSpawnWorkerSpawner_OutfitBundle covers the shorthand path
// (Bundle takes precedence over explicit Name/Cut/Accessories).
func TestOrchSpawnWorkerSpawner_OutfitBundle(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "args.log")
	fake := filepath.Join(tmp, "orch-spawn")
	body := `#!/usr/bin/env bash
printf '%s\n' "$@" > "$ORCH_SPAWN_TEST_LOG"
echo "%7"
`
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	s := &OrchSpawnWorkerSpawner{
		BinPath:  fake,
		ExtraEnv: []string{"ORCH_SPAWN_TEST_LOG=" + logFile},
	}
	_, err := s.Spawn(context.Background(), spawnspec.SpawnSpec{
		Name:   "x",
		Agent:  spawnspec.AgentPi,
		Tmux:   &spawnspec.TmuxBlock{Headless: true},
		Outfit: &spawnspec.OutfitBlock{Bundle: "backend/executing+pr-policy"},
	}, ResolvedSesh{})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}
	logged, _ := os.ReadFile(logFile)
	got := strings.ReplaceAll(string(logged), "\n", " ")
	if !strings.Contains(got, "--outfit backend/executing+pr-policy") {
		t.Errorf("outfit bundle missing; args=%s", got)
	}
	// Bundle path MUST NOT emit --cut / --accessory (the bash side
	// expands the bundle string itself).
	if strings.Contains(got, "--cut ") {
		t.Errorf("bundle path emitted --cut; args=%s", got)
	}
}
