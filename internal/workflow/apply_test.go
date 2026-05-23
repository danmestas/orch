package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// fakeSeshClient is the unit-test backend for SeshClient. It records
// every call so tests can assert request shape, and it tracks the
// emitted tasks + goals in an in-memory bucket so re-apply / cancel
// scenarios observe their own writes the same way the real sesh-ops
// binary would.
//
// The fake intentionally enforces sesh-ops's CAS-flip-only-pending
// semantic for CleanupWorkflowTasks (the surface Cancel rides on);
// running / blocked / terminal tasks stay put.
type fakeSeshClient struct {
	mu          sync.Mutex
	nextTaskID  int
	nextGoalID  int
	tasks       map[string]*TaskRecord
	goals       map[string]*goalRecord
	goalTaskIDs map[string][]string

	addCalls    []AddTaskRequest
	linkCalls   []struct{ goalID, taskID string }
	cleanupRuns int

	errOnAddFor string // inject error for titles starting with this
}

func newFakeSeshClient() *fakeSeshClient {
	return &fakeSeshClient{
		tasks:       make(map[string]*TaskRecord),
		goals:       make(map[string]*goalRecord),
		goalTaskIDs: make(map[string][]string),
	}
}

func (f *fakeSeshClient) AddTask(_ context.Context, req AddTaskRequest) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.errOnAddFor != "" && strings.HasPrefix(req.Title, f.errOnAddFor) {
		return "", fmt.Errorf("fake: injected error for %s", req.Title)
	}
	f.nextTaskID++
	id := fmt.Sprintf("01TASK%d", f.nextTaskID)
	metaRaw, _ := json.Marshal(req.Metadata)
	f.tasks[id] = &TaskRecord{
		ID:        id,
		Title:     req.Title,
		Status:    "pending",
		DependsOn: append([]string(nil), req.DependsOn...),
		Metadata:  metaRaw,
	}
	f.addCalls = append(f.addCalls, req)
	return id, nil
}

func (f *fakeSeshClient) ListTasks(_ context.Context, _ string) ([]TaskRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]TaskRecord, 0, len(f.tasks))
	for _, t := range f.tasks {
		out = append(out, *t)
	}
	return out, nil
}

func (f *fakeSeshClient) EnsureWorkflowGoal(_ context.Context, _, workflowID string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, g := range f.goals {
		var meta struct {
			OrchWorkflowGoal bool   `json:"orch_workflow_goal"`
			WorkflowID       string `json:"workflow_id"`
		}
		_ = json.Unmarshal(g.Metadata, &meta)
		if meta.OrchWorkflowGoal && meta.WorkflowID == workflowID {
			return g.ID, nil
		}
	}
	f.nextGoalID++
	id := fmt.Sprintf("01GOAL%d", f.nextGoalID)
	meta, _ := json.Marshal(map[string]any{
		"orch_workflow_goal": true,
		"workflow_id":        workflowID,
	})
	f.goals[id] = &goalRecord{
		ID: id, Objective: "orch-workflow: " + workflowID,
		Owner: WorkflowGoalOwner, Metadata: meta,
	}
	return id, nil
}

func (f *fakeSeshClient) LinkTaskToGoal(_ context.Context, _, goalID, taskID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.goalTaskIDs[goalID] {
		if existing == taskID {
			return nil // idempotent
		}
	}
	f.goalTaskIDs[goalID] = append(f.goalTaskIDs[goalID], taskID)
	f.linkCalls = append(f.linkCalls, struct{ goalID, taskID string }{goalID, taskID})
	return nil
}

func (f *fakeSeshClient) CleanupWorkflowTasks(_ context.Context, _, goalID string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleanupRuns++
	cancelled := make([]string, 0)
	for _, tid := range f.goalTaskIDs[goalID] {
		t, ok := f.tasks[tid]
		if !ok {
			continue
		}
		if t.Status != "pending" {
			continue // mirror sesh-ops cleanup-tasks semantics
		}
		t.Status = "cancelled"
		cancelled = append(cancelled, tid)
	}
	return cancelled, nil
}

// setStatus is a fake-only helper for simulating "task progressed in
// the wild" between operations.
func (f *fakeSeshClient) setStatus(taskID, status string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if t, ok := f.tasks[taskID]; ok {
		t.Status = status
	}
}

const minimalWF = `name: build-feature
scope-id: e2ecafe1
nodes:
  - id: plan
    prompt: "make a plan"
  - id: implement
    depends_on: [plan]
    prompt: "do the thing per $plan.output"
`

func parseTestWF(t *testing.T, yml string) *Workflow {
	t.Helper()
	wf, err := ParseBytes([]byte(yml))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return wf
}

func TestApply_seedsTasksAndTranslatesDeps(t *testing.T) {
	client := newFakeSeshClient()
	rep, err := Apply(context.Background(), parseTestWF(t, minimalWF), client, ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(rep.Created) != 2 {
		t.Fatalf("expected 2 created, got %d (%+v)", len(rep.Created), rep.Created)
	}
	if len(rep.Unchanged) != 0 {
		t.Errorf("unexpected unchanged entries on first apply: %+v", rep.Unchanged)
	}
	if rep.GoalID == "" {
		t.Error("apply must surface the workflow goal id")
	}
	if len(client.addCalls) != 2 {
		t.Fatalf("expected 2 AddTask calls, got %d", len(client.addCalls))
	}
	if len(client.linkCalls) != 2 {
		t.Errorf("expected 2 link-task calls, got %d", len(client.linkCalls))
	}
	first := client.addCalls[0]
	second := client.addCalls[1]
	if len(first.DependsOn) != 0 {
		t.Errorf("plan should have no deps, got %v", first.DependsOn)
	}
	if len(second.DependsOn) != 1 {
		t.Fatalf("implement should have 1 dep, got %v", second.DependsOn)
	}
	planID := rep.TaskIDs["plan"]
	if planID == "" {
		t.Fatal("plan task id missing from report")
	}
	if second.DependsOn[0] != planID {
		t.Errorf("implement deps not translated; want %s, got %s", planID, second.DependsOn[0])
	}
}

func TestApply_isIdempotent(t *testing.T) {
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	first, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	addsBefore := len(client.addCalls)

	second, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if len(second.Created) != 0 {
		t.Errorf("re-apply created tasks (not idempotent): %+v", second.Created)
	}
	if len(second.Unchanged) != 2 {
		t.Errorf("re-apply expected 2 unchanged, got %d", len(second.Unchanged))
	}
	if len(client.addCalls) != addsBefore {
		t.Errorf("re-apply triggered extra AddTask calls (%d → %d)", addsBefore, len(client.addCalls))
	}
	if first.TaskIDs["plan"] != second.TaskIDs["plan"] {
		t.Errorf("task id should be reused across re-apply (plan)")
	}
	if first.GoalID != second.GoalID {
		t.Errorf("goal id should be reused across re-apply (got %s vs %s)", first.GoalID, second.GoalID)
	}
}

func TestApply_changedBodyCreatesFreshTask(t *testing.T) {
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	first, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("first apply: %v", err)
	}
	// Change implement's prompt; plan is untouched.
	modified := parseTestWF(t, strings.Replace(minimalWF, "do the thing", "ship the thing", 1))
	second, err := Apply(context.Background(), modified, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("modified apply: %v", err)
	}
	if len(second.Created) != 1 || second.Created[0].NodeID != "implement" {
		t.Errorf("expected implement to be created fresh; got %+v", second.Created)
	}
	if len(second.Unchanged) != 1 || second.Unchanged[0].NodeID != "plan" {
		t.Errorf("expected plan unchanged; got %+v", second.Unchanged)
	}
	if first.TaskIDs["implement"] == second.TaskIDs["implement"] {
		t.Errorf("implement task id should differ after body change (both %s)", second.TaskIDs["implement"])
	}
	// The old implement task is left in place per the documented model.
	if _, ok := client.tasks[first.TaskIDs["implement"]]; !ok {
		t.Errorf("old implement task should still exist (apply does NOT supersede)")
	}
}

func TestApply_requiresScopeID(t *testing.T) {
	client := newFakeSeshClient()
	wfNoScope := parseTestWF(t, `name: x
nodes:
  - id: a
    prompt: hi
`)
	if _, err := Apply(context.Background(), wfNoScope, client, ApplyOptions{}); err == nil {
		t.Fatal("expected error for missing scope-id")
	}
	if _, err := Apply(context.Background(), wfNoScope, client, ApplyOptions{ScopeID: "deadbeef"}); err != nil {
		t.Errorf("apply with scope-id override should succeed: %v", err)
	}
}

func TestApply_refusesInvalidWorkflow(t *testing.T) {
	client := newFakeSeshClient()
	cyclic := parseTestWF(t, `name: cyc
scope-id: abc
nodes:
  - id: a
    depends_on: [b]
    prompt: a
  - id: b
    depends_on: [a]
    prompt: b
`)
	if _, err := Apply(context.Background(), cyclic, client, ApplyOptions{}); err == nil {
		t.Fatal("expected apply to refuse cyclic workflow")
	}
	if len(client.addCalls) != 0 {
		t.Errorf("apply should not touch sesh on invalid input; got %d AddTask calls", len(client.addCalls))
	}
}

func TestStatus_reportsPerNodeProgress(t *testing.T) {
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	rep, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	planID := rep.TaskIDs["plan"]
	client.setStatus(planID, "completed")
	status, err := Status(context.Background(), wf.Name, wf.ScopeID, client)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(status.Nodes) != 2 {
		t.Fatalf("expected 2 node statuses, got %d", len(status.Nodes))
	}
	if status.Totals["completed"] != 1 || status.Totals["pending"] != 1 {
		t.Errorf("totals wrong: %+v", status.Totals)
	}
	if status.AllTerminal() {
		t.Errorf("AllTerminal should be false while implement is pending")
	}
}

func TestStatus_filtersByWorkflowID(t *testing.T) {
	client := newFakeSeshClient()
	wf1 := parseTestWF(t, minimalWF)
	wf2 := parseTestWF(t, strings.Replace(minimalWF, "build-feature", "other-flow", 1))
	if _, err := Apply(context.Background(), wf1, client, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Apply(context.Background(), wf2, client, ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	s1, _ := Status(context.Background(), "build-feature", wf1.ScopeID, client)
	if len(s1.Nodes) != 2 {
		t.Errorf("status should only see build-feature tasks, got %d", len(s1.Nodes))
	}
}

func TestStatus_collapsesDuplicateNodes(t *testing.T) {
	// Apply once, then re-apply with a changed body so the same node id
	// has two task records. Status must surface the most-alive one.
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	first, _ := Apply(context.Background(), wf, client, ApplyOptions{})
	modified := parseTestWF(t, strings.Replace(minimalWF, "do the thing", "ship the thing", 1))
	second, _ := Apply(context.Background(), modified, client, ApplyOptions{})

	// Mark the old implement task completed and leave the new one pending.
	client.setStatus(first.TaskIDs["implement"], "completed")
	status, err := Status(context.Background(), wf.Name, wf.ScopeID, client)
	if err != nil {
		t.Fatal(err)
	}
	if len(status.Nodes) != 2 {
		t.Errorf("expected 2 deduplicated nodes; got %d", len(status.Nodes))
	}
	// The implement row should reference the *new* task (still pending
	// outranks completed in the priority scheme).
	for _, n := range status.Nodes {
		if n.NodeID == "implement" && n.TaskID != second.TaskIDs["implement"] {
			t.Errorf("status should surface the live implement record (%s); got %s",
				second.TaskIDs["implement"], n.TaskID)
		}
	}
}

func TestCancel_flipsPendingViaGoalCleanup(t *testing.T) {
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	rep, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	client.setStatus(rep.TaskIDs["plan"], "in_progress")

	cancelRep, err := Cancel(context.Background(), wf.Name, wf.ScopeID, client)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(cancelRep.Cancelled) != 1 || cancelRep.Cancelled[0].NodeID != "implement" {
		t.Errorf("expected implement cancelled, got %+v", cancelRep.Cancelled)
	}
	if len(cancelRep.Skipped) != 1 || cancelRep.Skipped[0].NodeID != "plan" {
		t.Errorf("expected plan skipped (in_progress), got %+v", cancelRep.Skipped)
	}
	if cancelRep.GoalID != rep.GoalID {
		t.Errorf("cancel goal id mismatch (apply=%s cancel=%s)", rep.GoalID, cancelRep.GoalID)
	}

	st, _ := Status(context.Background(), wf.Name, wf.ScopeID, client)
	planFound, implementFound := false, false
	for _, n := range st.Nodes {
		switch n.NodeID {
		case "plan":
			planFound = true
			if n.Status != "in_progress" {
				t.Errorf("plan must stay in_progress; got %s", n.Status)
			}
		case "implement":
			implementFound = true
			if n.Status != "cancelled" {
				t.Errorf("implement must be cancelled; got %s", n.Status)
			}
		}
	}
	if !planFound || !implementFound {
		t.Fatalf("status missing nodes (plan=%v implement=%v)", planFound, implementFound)
	}
}

func TestCancel_handlesUnappliedWorkflowGracefully(t *testing.T) {
	client := newFakeSeshClient()
	rep, err := Cancel(context.Background(), "never-applied", "abc", client)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if len(rep.Cancelled) != 0 || len(rep.Skipped) != 0 {
		t.Errorf("cancel of missing workflow should be empty success, got %+v", rep)
	}
	// Important: we must NOT have created a goal in this case — cancel
	// of a non-existent workflow shouldn't pollute the scope.
	if len(client.goals) != 0 {
		t.Errorf("cancel of unapplied workflow should not create a goal; got %d", len(client.goals))
	}
}

func TestApply_reAppliesCleanlyAfterCancel(t *testing.T) {
	// After cancel marks pending tasks cancelled, re-apply should see
	// the same (workflow_id, node_id, fingerprint) triple, find the
	// existing cancelled record, and treat it as "unchanged" — the
	// node body hasn't changed, so there's no reason to create a new
	// task. The operator must explicitly bump the body (or rotate the
	// scope) to get fresh tasks. This matches the brief's "task ids =
	// deterministic hash" rule: same hash → same task, regardless of
	// status.
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	if _, err := Apply(context.Background(), wf, client, ApplyOptions{}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	if _, err := Cancel(context.Background(), wf.Name, wf.ScopeID, client); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	reapply, err := Apply(context.Background(), wf, client, ApplyOptions{})
	if err != nil {
		t.Fatalf("re-apply after cancel: %v", err)
	}
	if len(reapply.Created) != 0 {
		t.Errorf("re-apply after cancel should NOT create duplicates (same body); got %d created (%+v)",
			len(reapply.Created), reapply)
	}
	if len(reapply.Unchanged) != 2 {
		t.Errorf("re-apply after cancel should see 2 unchanged records; got %d", len(reapply.Unchanged))
	}
}

func TestFingerprint_ignoresDepOrderChange(t *testing.T) {
	t1 := PlannedTask{
		NodeID: "x", Kind: "prompt", Description: "hi",
		DependsOn: []string{"a", "b"},
	}
	t2 := PlannedTask{
		NodeID: "x", Kind: "prompt", Description: "hi",
		DependsOn: []string{"b", "a"},
	}
	if fingerprintTask(t1) != fingerprintTask(t2) {
		t.Error("fingerprint must be invariant to depends_on order")
	}
}

func TestFingerprint_changesWhenDescriptionChanges(t *testing.T) {
	t1 := PlannedTask{NodeID: "x", Kind: "prompt", Description: "a"}
	t2 := PlannedTask{NodeID: "x", Kind: "prompt", Description: "b"}
	if fingerprintTask(t1) == fingerprintTask(t2) {
		t.Error("fingerprint must change with description")
	}
}

func TestFingerprint_isDeterministic(t *testing.T) {
	tk := PlannedTask{NodeID: "x", Kind: "prompt", Description: "hello", PullRefs: []string{"$a.output"}}
	if fingerprintTask(tk) != fingerprintTask(tk) {
		t.Error("fingerprint not deterministic")
	}
}

func TestApply_metadataContainsOrchKeys(t *testing.T) {
	client := newFakeSeshClient()
	wf := parseTestWF(t, minimalWF)
	if _, err := Apply(context.Background(), wf, client, ApplyOptions{}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, call := range client.addCalls {
		md, ok := call.Metadata["orch_workflow"].(map[string]any)
		if !ok {
			t.Fatalf("missing orch_workflow metadata in %s", call.Title)
		}
		for _, key := range []string{"workflow_id", "node_id", "node_kind", "fingerprint"} {
			if _, ok := md[key]; !ok {
				t.Errorf("%s metadata missing %s", call.Title, key)
			}
		}
	}
}

func TestStatus_emptyScopeIsNoFailure(t *testing.T) {
	client := newFakeSeshClient()
	rep, err := Status(context.Background(), "missing", "abc", client)
	if err != nil {
		t.Fatalf("status on empty scope: %v", err)
	}
	if len(rep.Nodes) != 0 {
		t.Errorf("expected zero nodes, got %d", len(rep.Nodes))
	}
	if rep.AllTerminal() {
		t.Errorf("AllTerminal must be false for empty workflow")
	}
}

func TestEnsureWorkflowGoal_idempotentOnSeshClient(t *testing.T) {
	// The fake implements lookup by metadata.workflow_id; the real
	// ExecSeshClient relies on `goal list --owner orch-workflow`
	// + the same metadata filter. Pin the behaviour here so the fake
	// can't drift from the production contract.
	client := newFakeSeshClient()
	a, err := client.EnsureWorkflowGoal(context.Background(), "scope", "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := client.EnsureWorkflowGoal(context.Background(), "scope", "wf-1")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("EnsureWorkflowGoal must be idempotent for the same workflow id: %s vs %s", a, b)
	}
	c, err := client.EnsureWorkflowGoal(context.Background(), "scope", "wf-2")
	if err != nil {
		t.Fatal(err)
	}
	if c == a {
		t.Errorf("EnsureWorkflowGoal must produce a fresh id for a new workflow")
	}
}

func TestFindWorkflowGoal_handlesMissingMetadata(t *testing.T) {
	// Goals without metadata or with the wrong shape must NOT match.
	g := goalRecord{ID: "01GOAL999", Metadata: json.RawMessage(`null`)}
	if id := findWorkflowGoal([]goalRecord{g}, "wf"); id != "" {
		t.Errorf("nil metadata should not match; got %q", id)
	}
	g2 := goalRecord{ID: "01GOAL999", Metadata: json.RawMessage(`{"orch_workflow_goal": false, "workflow_id": "wf"}`)}
	if id := findWorkflowGoal([]goalRecord{g2}, "wf"); id != "" {
		t.Errorf("flag-false should not match; got %q", id)
	}
}
