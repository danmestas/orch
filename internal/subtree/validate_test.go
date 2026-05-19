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

// validateCase pins down which codes Validate must emit for a given
// Topology. `wantCodes` is an expected superset of error codes (order-
// independent). `forbidCodes` is the list of codes that MUST NOT
// appear. Mirrors internal/workflow/validate_test.go.
type validateCase struct {
	name        string
	mutate      func(*Topology) // applied to validTopology(); nil = no mutation
	wantCodes   []string
	forbidCodes []string
	wantValid   bool
}

func TestValidate(t *testing.T) {
	cases := []validateCase{
		{
			name:      "minimal valid",
			wantValid: true,
		},
		{
			name:      "nil topology",
			mutate:    nil, // overridden below
			wantCodes: []string{CodeMissingName},
		},
		{
			name:      "missing name",
			mutate:    func(top *Topology) { top.Name = "" },
			wantCodes: []string{CodeMissingName},
		},
		{
			name:      "non-DNS-label name",
			mutate:    func(top *Topology) { top.Name = "Bad Name!" },
			wantCodes: []string{CodeBadDNSLabel},
		},
		{
			name:      "missing sesh (both legs empty)",
			mutate:    func(top *Topology) { top.Sesh = SeshSection{} },
			wantCodes: []string{CodeMissingSesh},
		},
		{
			name: "sesh xor violation (both set)",
			mutate: func(top *Topology) {
				top.Sesh = SeshSection{
					Existing: "nats://x",
					Spawn:    &SeshSpawn{Session: "y"},
				}
			},
			wantCodes: []string{CodeSeshXOR},
		},
		{
			name: "sesh spawn bad scope",
			mutate: func(top *Topology) {
				top.Sesh = SeshSection{Spawn: &SeshSpawn{Session: "y", Scope: "wrong"}}
			},
			wantCodes: []string{CodeSeshBadScope},
		},
		{
			name: "sesh spawn-only is ok",
			mutate: func(top *Topology) {
				top.Sesh = SeshSection{Spawn: &SeshSpawn{Session: "y"}}
			},
			wantValid: true,
		},
		{
			name: "worker missing executor",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.Tmux = nil
			},
			wantCodes:   []string{CodeMissingExecutor},
			forbidCodes: []string{CodeSpawnSpecInvalid}, // pre-check captured it
		},
		{
			name: "worker executor xor violation (tmux + cf-worker)",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.CFWorker = &spawnspec.CFWorkerBlock{Script: "x.js"}
			},
			wantCodes: []string{CodeExecutorXOR},
		},
		{
			name: "worker missing name",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.Name = ""
			},
			wantCodes: []string{CodeMissingWorkerName},
		},
		{
			name: "worker bad DNS-label name",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.Name = "Bad Name!"
			},
			wantCodes: []string{CodeBadWorkerDNSLabel},
		},
		{
			name: "worker missing agent",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.Agent = ""
			},
			wantCodes: []string{CodeMissingWorkerAgent},
		},
		{
			name: "worker bad agent enum",
			mutate: func(top *Topology) {
				top.Workers[0].SpawnSpec.Agent = "not-a-real-agent"
			},
			wantCodes: []string{CodeBadAgent},
		},
		{
			name: "duplicate worker name",
			mutate: func(top *Topology) {
				top.Workers = append(top.Workers, WorkerEntry{
					SpawnSpec: spawnspec.SpawnSpec{
						SpecVersion: spawnspec.SpecVersion,
						Name:        "engineer", // duplicate
						Agent:       spawnspec.AgentCodex,
						Tmux:        &spawnspec.TmuxBlock{Headless: true},
					},
				})
			},
			wantCodes: []string{CodeDuplicateWorker},
		},
		{
			name: "task seed missing title",
			mutate: func(top *Topology) {
				top.State.Tasks = []TaskSeed{{Scope: "workflow", ScopeID: "abc"}}
			},
			wantCodes: []string{CodeMissingTaskTitle},
		},
		{
			name: "task seed missing scope",
			mutate: func(top *Topology) {
				top.State.Tasks = []TaskSeed{{Title: "x", ScopeID: "abc"}}
			},
			wantCodes: []string{CodeMissingStateScope},
		},
		{
			name: "task seed missing scope-id",
			mutate: func(top *Topology) {
				top.State.Tasks = []TaskSeed{{Title: "x", Scope: "workflow"}}
			},
			wantCodes: []string{CodeMissingStateScopeID},
		},
		{
			name: "task seed negative max_attempts",
			mutate: func(top *Topology) {
				top.State.Tasks = []TaskSeed{{Title: "x", Scope: "workflow", ScopeID: "abc", MaxAttempts: -1}}
			},
			wantCodes: []string{CodeNegativeMaxAttempts},
		},
		{
			name: "goal seed missing objective",
			mutate: func(top *Topology) {
				top.State.Goals = []GoalSeed{{Scope: "workflow", ScopeID: "abc"}}
			},
			wantCodes: []string{CodeMissingGoalObjective},
		},
		{
			name: "goal seed negative budget_tokens",
			mutate: func(top *Topology) {
				top.State.Goals = []GoalSeed{{Scope: "workflow", ScopeID: "abc", Objective: "x", BudgetTokens: -1}}
			},
			wantCodes: []string{CodeNegativeBudgetTokens},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var top *Topology
			if tc.name == "nil topology" {
				top = nil
			} else {
				top = validTopology()
				if tc.mutate != nil {
					tc.mutate(top)
				}
			}
			rpt := Validate(top)
			t.Logf("report:\n%s", rpt.String())
			if tc.wantValid && !rpt.Valid() {
				t.Fatalf("want Valid()=true, got errors:\n%s", rpt.String())
			}
			for _, code := range tc.wantCodes {
				requireCode(t, rpt, code)
			}
			forbidCodes(t, rpt, tc.forbidCodes)
		})
	}
}

func TestValidate_ErrRoundTrip(t *testing.T) {
	// Sanity: a valid topology returns nil from rpt.Err().
	if err := Validate(validTopology()).Err(); err != nil {
		t.Fatalf("valid topology produced Err: %v", err)
	}
	// An invalid topology surfaces the codes in the error message.
	top := validTopology()
	top.Name = ""
	err := Validate(top).Err()
	if err == nil {
		t.Fatal("invalid topology returned nil Err")
	}
	if !strings.Contains(err.Error(), CodeMissingName) {
		t.Errorf("Err() should mention code %q; got %v", CodeMissingName, err)
	}
}

// requireCode fails the test if the report does not contain an error
// diagnostic with the given code. Mirrors workflow's hasErrorCode +
// t.Errorf idiom.
func requireCode(t *testing.T, r *Report, code string) {
	t.Helper()
	for _, d := range r.Errors() {
		if d.Code == code {
			return
		}
	}
	t.Errorf("missing expected error code %q\nfull report:\n%s", code, r.String())
}

// forbidCodes fails the test if any of the given codes appear in the
// report. Mirrors workflow's "forbidden code present" check.
func forbidCodes(t *testing.T, r *Report, codes []string) {
	t.Helper()
	for _, code := range codes {
		for _, d := range r.Errors() {
			if d.Code == code {
				t.Errorf("forbidden code %q present\nfull report:\n%s", code, r.String())
			}
		}
	}
}
