package workflow

import (
	"strings"
	"testing"
)

// Each case feeds yaml → parse → validate, then asserts codes present.
// `wantCodes` is an expected superset of error codes (order-independent).
// `forbidCodes` is the list of codes that MUST NOT appear; used to make
// negative assertions explicit (e.g., "this workflow has a cycle but is
// reachable; don't also report unreachable").
type validateCase struct {
	name        string
	yaml        string
	opts        []ValidateOption
	wantCodes   []string
	forbidCodes []string
	wantValid   bool
	// wantWarnings lists warning codes (does not affect Valid()).
	wantWarnings []string
}

func TestValidate(t *testing.T) {
	cases := []validateCase{
		{
			name: "minimal valid",
			yaml: `name: ok
nodes:
  - id: plan
    prompt: do it
`,
			wantValid: true,
		},
		{
			name: "missing workflow name",
			yaml: `nodes:
  - id: plan
    prompt: do it
`,
			wantCodes: []string{CodeMissingWorkflow},
		},
		{
			name: "missing id",
			yaml: `name: x
nodes:
  - prompt: orphan
`,
			wantCodes: []string{CodeMissingID},
		},
		{
			name: "duplicate id",
			yaml: `name: x
nodes:
  - id: plan
    prompt: a
  - id: plan
    prompt: b
`,
			wantCodes: []string{CodeDuplicateID},
		},
		{
			name: "invalid identifier",
			yaml: `name: x
nodes:
  - id: "has space"
    prompt: a
`,
			wantCodes: []string{CodeInvalidIdentifier},
		},
		{
			name: "missing kind",
			yaml: `name: x
nodes:
  - id: plan
`,
			wantCodes: []string{CodeMissingKind},
		},
		{
			name: "multiple kinds",
			yaml: `name: x
nodes:
  - id: plan
    prompt: a
    bash: "echo b"
`,
			wantCodes: []string{CodeMultipleKind},
		},
		{
			name: "loop missing prompt + until",
			yaml: `name: x
nodes:
  - id: implement
    loop:
      max_iterations: 3
`,
			wantCodes: []string{CodeMissingSubField},
		},
		{
			name: "approval missing prompt",
			yaml: `name: x
nodes:
  - id: gate
    approval:
      until: APPROVED
`,
			wantCodes: []string{CodeMissingSubField},
		},
		{
			name: "spawn missing name",
			yaml: `name: x
nodes:
  - id: provision
    spawn:
      agent: claude-code
`,
			wantCodes: []string{CodeMissingSubField},
		},
		{
			name: "unknown dependency",
			yaml: `name: x
nodes:
  - id: a
    depends_on: [ghost]
    prompt: hi
`,
			wantCodes: []string{CodeUnknownDep, CodeUnreachable},
		},
		{
			name: "simple cycle a→b→a",
			yaml: `name: x
nodes:
  - id: a
    depends_on: [b]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
`,
			wantCodes:   []string{CodeCycle},
			forbidCodes: []string{CodeUnknownDep},
		},
		{
			name: "three-node cycle a→b→c→a",
			yaml: `name: x
nodes:
  - id: a
    depends_on: [c]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
  - id: c
    depends_on: [b]
    prompt: c
`,
			wantCodes: []string{CodeCycle},
		},
		{
			name: "dangling string ref",
			yaml: `name: x
nodes:
  - id: a
    prompt: "based on $ghost.output"
`,
			wantCodes: []string{CodeDanglingRef},
		},
		{
			name: "ref via .output to existing node ok",
			yaml: `name: x
nodes:
  - id: plan
    prompt: plan stuff
  - id: implement
    depends_on: [plan]
    prompt: "based on $plan.output"
`,
			wantValid: true,
		},
		{
			name: "json-path on bash node → warning",
			yaml: `name: x
nodes:
  - id: list
    bash: "ls /tmp"
  - id: act
    depends_on: [list]
    prompt: "first: $list.output.files.0"
`,
			wantValid:    true,
			wantWarnings: []string{CodeJSONPathOnNonJSON},
		},
		{
			name: "assign to declared spawn target ok",
			yaml: `name: x
nodes:
  - id: provision
    spawn:
      name: verifier
      agent: claude-code
  - id: review
    depends_on: [provision]
    prompt: review please
    assign: verifier
`,
			wantValid: true,
		},
		{
			name: "assign to unknown target (with fleet provided)",
			yaml: `name: x
nodes:
  - id: review
    prompt: review please
    assign: ghost
`,
			opts:      []ValidateOption{WithFleet([]string{"lead-engineer", "verifier"})},
			wantCodes: []string{CodeUnknownAssign},
		},
		{
			name: "assign to known fleet member",
			yaml: `name: x
nodes:
  - id: review
    prompt: review please
    assign: verifier
`,
			opts:      []ValidateOption{WithFleet([]string{"verifier"})},
			wantValid: true,
		},
		{
			name: "assign without fleet info is silent (Phase A)",
			yaml: `name: x
nodes:
  - id: review
    prompt: review please
    assign: someone-out-there
`,
			wantValid: true,
		},
		{
			name: "two-node cycle reports CodeCycle and does NOT double-report unreachable",
			// Both a and b are IN the cycle. checkCycles records their
			// membership; checkUnreachable must skip them so we don't
			// stack diagnostics.
			yaml: `name: x
nodes:
  - id: a
    depends_on: [b]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
`,
			wantCodes:   []string{CodeCycle},
			forbidCodes: []string{CodeUnreachable},
		},
		{
			name: "node depending into a cycle is still flagged unreachable",
			// c is downstream of the a↔b cycle. We want CodeCycle for
			// the cycle itself AND CodeUnreachable for c — c isn't a
			// cycle member, just an unreachable dependent.
			yaml: `name: x
nodes:
  - id: a
    depends_on: [b]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
  - id: c
    depends_on: [a]
    prompt: c
`,
			wantCodes: []string{CodeCycle, CodeUnreachable},
		},
		{
			name: "reference workflow from spec",
			yaml: specReferenceYAML,
			// The spec's example uses `spawn-verifier` (a `spawn:` node
			// declaring name=verifier), and `review` assigns to verifier
			// — that's the canonical happy-path coverage.
			wantValid: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wf, err := ParseBytes([]byte(tc.yaml))
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			rpt := Validate(wf, tc.opts...)
			t.Logf("report:\n%s", rpt.String())
			if tc.wantValid && !rpt.Valid() {
				t.Fatalf("want Valid()=true, got errors:\n%s", rpt.String())
			}
			for _, code := range tc.wantCodes {
				if !hasErrorCode(rpt, code) {
					t.Errorf("missing expected error code %q\nfull report:\n%s", code, rpt.String())
				}
			}
			for _, code := range tc.forbidCodes {
				if hasErrorCode(rpt, code) {
					t.Errorf("forbidden code %q present\nfull report:\n%s", code, rpt.String())
				}
			}
			for _, code := range tc.wantWarnings {
				if !hasWarningCode(rpt, code) {
					t.Errorf("missing expected warning code %q\nfull report:\n%s", code, rpt.String())
				}
			}
		})
	}
}

func TestValidate_nilWorkflow(t *testing.T) {
	rpt := Validate(nil)
	if rpt.Valid() {
		t.Fatalf("expected invalid for nil workflow")
	}
	if !strings.Contains(rpt.String(), "workflow is nil") {
		t.Errorf("want message 'workflow is nil', got: %s", rpt.String())
	}
}

func hasErrorCode(r *Report, code string) bool {
	for _, d := range r.Errors() {
		if d.Code == code {
			return true
		}
	}
	return false
}

func hasWarningCode(r *Report, code string) bool {
	for _, d := range r.Warnings() {
		if d.Code == code {
			return true
		}
	}
	return false
}

// specReferenceYAML is the example from docs/proposals/0007. Embedded
// here so the test breaks loudly if anyone edits the spec example into
// something the validator rejects.
const specReferenceYAML = `name: build-feature
description: "Plan, implement, validate, review, PR — Archon-shaped"
scope-id: e2ecafe1
nodes:
  - id: plan
    prompt: "Explore the codebase and create an implementation plan"
    assign: lead-engineer
  - id: spawn-verifier
    depends_on: [plan]
    spawn:
      name: verifier
      agent: claude-code
      tmux:
        headless: true
      outfit:
        bundle: backend/verifying
  - id: implement
    depends_on: [plan]
    loop:
      prompt: "Read the plan from $plan.output. Implement next task. Validate."
      until: ALL_TASKS_COMPLETE
      max_iterations: 5
      fresh_context: true
    assign: lead-engineer
  - id: run-tests
    depends_on: [implement]
    bash: "bun run validate"
  - id: review
    depends_on: [run-tests, spawn-verifier]
    prompt: "Review all changes against the plan. Fix any issues."
    assign: verifier
  - id: approve
    depends_on: [review]
    approval:
      prompt: "Present the changes for review. Address any feedback."
      until: APPROVED
  - id: create-pr
    depends_on: [approve]
    prompt: "Push changes and create a pull request"
    assign: lead-engineer
`
