package workflow

import (
	"os"
	"strings"
	"testing"
)

func TestCompile_refusesInvalid(t *testing.T) {
	wf, err := ParseBytes([]byte(`name: x
nodes:
  - id: a
    depends_on: [b]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := Compile(wf); err == nil {
		t.Fatalf("expected compile to refuse cyclic workflow")
	}
}

func TestCompile_substitutesCompileTime(t *testing.T) {
	t.Setenv("FOO_BAR", "wired")
	wf, err := ParseBytes([]byte(`name: demo
scope-id: abc123
nodes:
  - id: hello
    prompt: "env=$ENV.FOO_BAR wf=$WORKFLOW.name scope=$WORKFLOW.scope_id"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	got := plan.Tasks[0].Description
	want := "env=wired wf=demo scope=abc123"
	if got != want {
		t.Fatalf("description:\n got=%q\nwant=%q", got, want)
	}
	// And no pull-refs collected (none of these are node refs).
	if len(plan.Tasks[0].PullRefs) != 0 {
		t.Errorf("expected no pull refs, got %v", plan.Tasks[0].PullRefs)
	}
}

func TestCompile_preservesPullRefs(t *testing.T) {
	wf, err := ParseBytes([]byte(`name: demo
nodes:
  - id: plan
    prompt: plan
  - id: impl
    depends_on: [plan]
    prompt: "based on $plan.output do the thing"
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	plan, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	implTask := plan.Tasks[1]
	if !strings.Contains(implTask.Description, "$plan.output") {
		t.Errorf("expected $plan.output preserved in description, got: %q", implTask.Description)
	}
	if len(implTask.PullRefs) != 1 || implTask.PullRefs[0] != "$plan.output" {
		t.Errorf("expected PullRefs=[$plan.output], got %v", implTask.PullRefs)
	}
}

func TestCompile_taskIDsAreWorkflowDotNode(t *testing.T) {
	wf, _ := ParseBytes([]byte(`name: build-feature
nodes:
  - id: plan
    prompt: a
`))
	plan, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if plan.Tasks[0].TaskID != "build-feature.plan" {
		t.Errorf("want task_id=build-feature.plan, got %q", plan.Tasks[0].TaskID)
	}
}

func TestCompile_unresolvedEnvIsEmptyString(t *testing.T) {
	// Make sure we don't have it set in the environment.
	os.Unsetenv("ORCH_TEST_DEFINITELY_UNSET")
	wf, _ := ParseBytes([]byte(`name: demo
nodes:
  - id: hi
    prompt: "x=$ENV.ORCH_TEST_DEFINITELY_UNSET done"
`))
	plan, err := Compile(wf)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if plan.Tasks[0].Description != "x= done" {
		t.Errorf("want 'x= done', got %q", plan.Tasks[0].Description)
	}
}
