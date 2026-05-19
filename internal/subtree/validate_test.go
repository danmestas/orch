package subtree

import (
	"strings"
	"testing"

	"github.com/danmestas/orch/internal/spawnspec"
)

func validTopology() *Topology {
	return &Topology{
		SpecVersion: SpecVersion,
		Name:        "fleet",
		Sesh:        SeshSection{Existing: "nats://localhost:4222"},
		Workers: []WorkerEntry{{
			SpawnSpec: spawnspec.SpawnSpec{
				SpecVersion: spawnspec.SpecVersion,
				Name:        "engineer",
				Agent:       spawnspec.AgentClaudeCode,
				Tmux:        &spawnspec.TmuxBlock{Headless: true},
			},
		}},
	}
}

func TestValidateOK(t *testing.T) {
	if err := Validate(validTopology()); err != nil {
		t.Fatalf("valid topology rejected: %v", err)
	}
}

func TestValidateRequiresName(t *testing.T) {
	top := validTopology()
	top.Name = ""
	if err := Validate(top); err == nil {
		t.Fatal("missing name accepted")
	}
}

func TestValidateNameMustBeDNSLabel(t *testing.T) {
	top := validTopology()
	top.Name = "Bad Name!"
	err := Validate(top)
	if err == nil || !strings.Contains(err.Error(), "DNS-label") {
		t.Fatalf("expected DNS-label error, got %v", err)
	}
}

func TestValidateSeshXOR(t *testing.T) {
	// Zero is rejected.
	top := validTopology()
	top.Sesh = SeshSection{}
	if err := Validate(top); err == nil {
		t.Fatal("empty sesh accepted")
	}

	// Both is rejected.
	top = validTopology()
	top.Sesh = SeshSection{
		Existing: "nats://x",
		Spawn:    &SeshSpawn{Session: "y"},
	}
	if err := Validate(top); err == nil {
		t.Fatal("both existing+spawn accepted")
	}

	// Only spawn is OK.
	top = validTopology()
	top.Sesh = SeshSection{Spawn: &SeshSpawn{Session: "y"}}
	if err := Validate(top); err != nil {
		t.Fatalf("spawn-only sesh rejected: %v", err)
	}
}

func TestValidateSeshSpawnScope(t *testing.T) {
	top := validTopology()
	top.Sesh = SeshSection{Spawn: &SeshSpawn{Session: "x", Scope: "wrong"}}
	if err := Validate(top); err == nil {
		t.Fatal("invalid scope accepted")
	}
}

func TestValidateWorkerDelegatesToSpawnspec(t *testing.T) {
	top := validTopology()
	// Drop the executor block — spawnspec should report executor_xor_zero.
	top.Workers[0].SpawnSpec.Tmux = nil
	err := Validate(top)
	if err == nil {
		t.Fatal("worker without executor accepted")
	}
	if !strings.Contains(err.Error(), "executor") {
		t.Errorf("error should mention executor; got %v", err)
	}
}

func TestValidateWorkerNamesUnique(t *testing.T) {
	top := validTopology()
	top.Workers = append(top.Workers, WorkerEntry{
		SpawnSpec: spawnspec.SpawnSpec{
			SpecVersion: spawnspec.SpecVersion,
			Name:        "engineer", // duplicate
			Agent:       spawnspec.AgentCodex,
			Tmux:        &spawnspec.TmuxBlock{Headless: true},
		},
	})
	err := Validate(top)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-name error, got %v", err)
	}
}

func TestValidateTaskSeed(t *testing.T) {
	top := validTopology()
	top.State.Tasks = []TaskSeed{{Title: "", Scope: "workflow", ScopeID: "abc"}}
	if err := Validate(top); err == nil {
		t.Fatal("task without title accepted")
	}
	top.State.Tasks = []TaskSeed{{Title: "x", Scope: "", ScopeID: "abc"}}
	if err := Validate(top); err == nil {
		t.Fatal("task without scope accepted")
	}
	top.State.Tasks = []TaskSeed{{Title: "x", Scope: "workflow", ScopeID: ""}}
	if err := Validate(top); err == nil {
		t.Fatal("task without scope-id accepted")
	}
}

func TestValidateGoalSeed(t *testing.T) {
	top := validTopology()
	top.State.Goals = []GoalSeed{{Objective: "", Scope: "workflow", ScopeID: "abc"}}
	if err := Validate(top); err == nil {
		t.Fatal("goal without objective accepted")
	}
	top.State.Goals = []GoalSeed{{Objective: "x", Scope: "workflow", ScopeID: "abc", BudgetTokens: -1}}
	if err := Validate(top); err == nil {
		t.Fatal("negative budget_tokens accepted")
	}
}
