package subtree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSeshOps writes its args to a file the test inspects, then
// exits 0. Lets us assert the CLI-flag translation without depending
// on a real sesh-ops binary.
func fakeSeshOps(t *testing.T, logFile string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sesh-ops")
	body := `#!/usr/bin/env bash
printf '%s\n' "$@" > "$SESH_OPS_TEST_LOG"
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("SESH_OPS_TEST_LOG", logFile)
	return path
}

func TestSeshOpsStateSeeder_TaskAdd(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "args.log")
	bin := fakeSeshOps(t, logFile)
	s := &SeshOpsStateSeeder{BinPath: bin}
	err := s.SeedTask(context.Background(), TaskSeed{
		Scope:       "workflow",
		ScopeID:     "deadbeef",
		Title:       "ship phase B",
		DependsOn:   []string{"parse", "validate"},
		MaxAttempts: 5,
		Metadata:    map[string]any{"k": "v"},
	}, ResolvedSesh{URL: "nats://x:4222"})
	if err != nil {
		t.Fatalf("SeedTask: %v", err)
	}
	logged, _ := os.ReadFile(logFile)
	got := strings.ReplaceAll(string(logged), "\n", " ")
	for _, want := range []string{
		"--server nats://x:4222",
		"--scope workflow",
		"--scope-id deadbeef",
		"task add",
		"--title ship phase B",
		"--depends-on parse,validate",
		"--max-attempts 5",
		`--metadata {"k":"v"}`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in args: %s", want, got)
		}
	}
}

func TestSeshOpsStateSeeder_GoalCreate(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "args.log")
	bin := fakeSeshOps(t, logFile)
	s := &SeshOpsStateSeeder{BinPath: bin}
	err := s.SeedGoal(context.Background(), GoalSeed{
		Scope:        "workflow",
		ScopeID:      "deadbeef",
		Objective:    "ship",
		BudgetTokens: 100000,
	}, ResolvedSesh{URL: "nats://x:4222"})
	if err != nil {
		t.Fatalf("SeedGoal: %v", err)
	}
	logged, _ := os.ReadFile(logFile)
	got := strings.ReplaceAll(string(logged), "\n", " ")
	for _, want := range []string{
		"--server nats://x:4222",
		"--scope workflow",
		"--scope-id deadbeef",
		"goal create",
		"--objective ship",
		"--budget-tokens 100000",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in args: %s", want, got)
		}
	}
}

// TestSeshOpsStateSeeder_PropagatesError surfaces the failure path
// — operator needs the stderr from sesh-ops to debug a misconfigured
// scope-id.
func TestSeshOpsStateSeeder_PropagatesError(t *testing.T) {
	tmp := t.TempDir()
	fake := filepath.Join(tmp, "sesh-ops-fail")
	body := `#!/usr/bin/env bash
echo "bogus scope-id" >&2
exit 5
`
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	s := &SeshOpsStateSeeder{BinPath: fake}
	err := s.SeedTask(context.Background(), TaskSeed{
		Scope: "workflow", ScopeID: "x", Title: "t",
	}, ResolvedSesh{})
	if err == nil {
		t.Fatal("expected error from failing sesh-ops; got nil")
	}
	if !strings.Contains(err.Error(), "bogus scope-id") {
		t.Errorf("expected stderr in wrapped error; got %v", err)
	}
}
