package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Apply seeds a compiled workflow into a sesh task bucket via the
// supplied SeshClient. It is the runtime counterpart to Compile: where
// Compile turns YAML into an in-memory Plan, Apply persists that Plan
// as live sesh task records the workers will pull from.
//
// Idempotency model: Apply assigns each compiled node a deterministic
// fingerprint over its body (kind + description + deps + assign +
// pull-refs). The lookup key for "do I already have this task?" is
// the triple (workflow_id, node_id, fingerprint) — same triple → same
// task → no-op; different fingerprint → fresh task with a new sesh
// ULID. The old task is left in place; operators run `cancel` if they
// want to retire stale pending work.
//
// Walking nodes in topological order lets each task's depends_on
// reference its predecessors' freshly assigned sesh task IDs (sesh's
// task DAG keys on task IDs, not the workflow's node IDs).
//
// Apply also ensures a workflow-anchoring goal exists in the scope so
// `cancel` has something to point sesh-ops `goal cleanup-tasks` at.
// Each created task is linked to that goal. The link is idempotent on
// the sesh side, so re-applying after a partial failure converges.
//
// The returned ApplyReport surfaces concrete diffs (created /
// unchanged) so operators see what actually happened rather than
// "applied 7 tasks" silence.
func Apply(ctx context.Context, wf *Workflow, client SeshClient, opts ApplyOptions) (*ApplyReport, error) {
	if wf == nil {
		return nil, fmt.Errorf("apply: workflow is nil")
	}
	if client == nil {
		return nil, fmt.Errorf("apply: sesh client is nil")
	}

	scopeID := opts.ScopeID
	if scopeID == "" {
		scopeID = wf.ScopeID
	}
	if scopeID == "" {
		return nil, fmt.Errorf("apply: scope-id required (set workflow.scope-id or pass --scope-id)")
	}

	plan, err := Compile(wf)
	if err != nil {
		return nil, fmt.Errorf("apply: %w", err)
	}

	existing, err := client.ListTasks(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("apply: list existing tasks: %w", err)
	}
	existingByKey := indexExistingTasks(existing, wf.Name)

	goalID, err := client.EnsureWorkflowGoal(ctx, scopeID, wf.Name)
	if err != nil {
		return nil, fmt.Errorf("apply: ensure workflow goal: %w", err)
	}

	rep := &ApplyReport{
		WorkflowID: wf.Name,
		ScopeID:    scopeID,
		GoalID:     goalID,
		TaskIDs:    make(map[string]string, len(plan.Tasks)),
	}

	order, err := topoOrder(plan)
	if err != nil {
		return nil, fmt.Errorf("apply: topo: %w", err)
	}
	nodeToTaskID := make(map[string]string, len(plan.Tasks))

	for _, idx := range order {
		task := plan.Tasks[idx]
		fp := fingerprintTask(task)
		key := taskKey{NodeID: task.NodeID, Fingerprint: fp}

		if hit, ok := existingByKey[key]; ok {
			nodeToTaskID[task.NodeID] = hit.ID
			rep.TaskIDs[task.NodeID] = hit.ID
			rep.Unchanged = append(rep.Unchanged, NodeChange{NodeID: task.NodeID, TaskID: hit.ID})
			continue
		}

		seshDeps, err := translateDeps(task.DependsOn, nodeToTaskID)
		if err != nil {
			return nil, fmt.Errorf("apply: %s: %w", task.NodeID, err)
		}

		seshID, err := client.AddTask(ctx, AddTaskRequest{
			ScopeID:     scopeID,
			Title:       fmt.Sprintf("%s.%s", wf.Name, task.NodeID),
			Description: task.Description,
			DependsOn:   seshDeps,
			Metadata:    taskMetadata(wf.Name, task, fp),
		})
		if err != nil {
			return nil, fmt.Errorf("apply: add task %s: %w", task.NodeID, err)
		}
		if err := client.LinkTaskToGoal(ctx, scopeID, goalID, seshID); err != nil {
			return nil, fmt.Errorf("apply: link task %s to goal: %w", task.NodeID, err)
		}
		nodeToTaskID[task.NodeID] = seshID
		rep.TaskIDs[task.NodeID] = seshID
		rep.Created = append(rep.Created, NodeChange{NodeID: task.NodeID, TaskID: seshID})
	}

	return rep, nil
}

// ApplyOptions tunes a single Apply call.
type ApplyOptions struct {
	// ScopeID overrides the workflow's own scope-id when non-empty.
	// Used by `orch subtree apply --workflow` to route the workflow
	// into the subtree's KV scope.
	ScopeID string
}

// ApplyReport summarises an Apply call. NodeChanges are appended in
// the same topological order Apply visited them so output stays
// deterministic.
type ApplyReport struct {
	WorkflowID string
	ScopeID    string
	GoalID     string

	// TaskIDs maps node ID → sesh task ID for every node in the plan
	// (regardless of created/unchanged). Useful for `status`
	// follow-ups that want the live ID set.
	TaskIDs map[string]string

	Created   []NodeChange
	Unchanged []NodeChange
}

// NodeChange records one entry in an ApplyReport bucket.
type NodeChange struct {
	NodeID string
	TaskID string
}

// String renders the report as a short human-readable summary suitable
// for the CLI. Stable formatting so tests can assert exact output.
func (r *ApplyReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow=%s scope-id=%s goal=%s\n", r.WorkflowID, r.ScopeID, r.GoalID)
	fmt.Fprintf(&b, "  created:    %d\n", len(r.Created))
	for _, c := range r.Created {
		fmt.Fprintf(&b, "    + %s → %s\n", c.NodeID, c.TaskID)
	}
	fmt.Fprintf(&b, "  unchanged:  %d\n", len(r.Unchanged))
	for _, c := range r.Unchanged {
		fmt.Fprintf(&b, "    = %s → %s\n", c.NodeID, c.TaskID)
	}
	return b.String()
}

// fingerprintTask hashes the compiled task body. The same compiled
// PlannedTask must always produce the same fingerprint regardless of
// iteration order. JSON encoding of the canonical fields gives that
// (Go's encoding/json sorts map keys; our task struct has no maps).
//
// SHA-256 truncated to 16 hex chars (64 bits) keeps the fingerprint
// short enough to inspect by eye while staying well clear of
// birthday collisions inside a single workflow (~4 billion tasks
// before 50% collision risk).
func fingerprintTask(t PlannedTask) string {
	body := struct {
		NodeID      string   `json:"node_id"`
		Kind        string   `json:"kind"`
		DependsOn   []string `json:"depends_on,omitempty"`
		Description string   `json:"description"`
		Assign      string   `json:"assign,omitempty"`
		PullRefs    []string `json:"pull_refs,omitempty"`
	}{
		NodeID:      t.NodeID,
		Kind:        t.Kind,
		DependsOn:   append([]string(nil), t.DependsOn...),
		Description: t.Description,
		Assign:      t.Assign,
		PullRefs:    append([]string(nil), t.PullRefs...),
	}
	// Sort lists so re-ordering depends_on without semantic change
	// doesn't flip the fingerprint.
	sort.Strings(body.DependsOn)
	sort.Strings(body.PullRefs)
	buf, _ := json.Marshal(body)
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:8])
}

// taskMetadata constructs the JSON metadata blob attached to every
// sesh task created by a workflow apply. The orch_workflow.* keys are
// reserved namespace so other systems writing to the same bucket don't
// collide. Status / cancel rely on these keys for lookup.
func taskMetadata(workflowID string, t PlannedTask, fingerprint string) map[string]any {
	inner := map[string]any{
		"workflow_id": workflowID,
		"node_id":     t.NodeID,
		"node_kind":   t.Kind,
		"fingerprint": fingerprint,
	}
	if t.Assign != "" {
		inner["assign"] = t.Assign
	}
	if len(t.PullRefs) > 0 {
		inner["pull_refs"] = t.PullRefs
	}
	return map[string]any{"orch_workflow": inner}
}

// taskKey is the compound identity Apply uses for idempotency lookup:
// same node body in the same workflow yields the same key, which
// yields the same task. A body change flips the fingerprint and forces
// a fresh sesh task.
type taskKey struct {
	NodeID      string
	Fingerprint string
}

// indexExistingTasks builds the {taskKey → existing record} lookup
// table from the raw task list, filtering to entries that belong to
// this workflow. Terminal records are kept because hash-equality still
// implies "we already saw this exact node body"; if it's terminal,
// re-applying needs to surface that the workflow already finished
// rather than creating a fresh duplicate.
func indexExistingTasks(tasks []TaskRecord, workflowID string) map[taskKey]existingEntry {
	out := make(map[taskKey]existingEntry)
	for _, t := range tasks {
		md := parseOrchMetadata(t.Metadata)
		if md.WorkflowID != workflowID {
			continue
		}
		out[taskKey{NodeID: md.NodeID, Fingerprint: md.Fingerprint}] = existingEntry{
			ID:     t.ID,
			Status: t.Status,
		}
	}
	return out
}

type existingEntry struct {
	ID     string
	Status string
}

// orchMetadata is the typed view of the orch_workflow.* nested object
// inside a sesh task's metadata blob. Any task missing the nested
// object yields an empty struct (filtered out at the call site).
type orchMetadata struct {
	WorkflowID  string `json:"workflow_id"`
	NodeID      string `json:"node_id"`
	NodeKind    string `json:"node_kind"`
	Fingerprint string `json:"fingerprint"`
}

func parseOrchMetadata(raw json.RawMessage) orchMetadata {
	if len(raw) == 0 {
		return orchMetadata{}
	}
	var outer struct {
		OrchWorkflow orchMetadata `json:"orch_workflow"`
	}
	if err := json.Unmarshal(raw, &outer); err != nil {
		return orchMetadata{}
	}
	return outer.OrchWorkflow
}

// topoOrder returns plan-task indices in a topologically valid order.
// Compile already rejected cycles, so a missing depends_on (which
// could only mean a dep on a node outside the plan) is the only
// reason this can fail. The implementation is Kahn's algorithm with a
// precomputed children map for O(V + E) work.
func topoOrder(plan *Plan) ([]int, error) {
	idIndex := make(map[string]int, len(plan.Tasks))
	for i, t := range plan.Tasks {
		idIndex[t.NodeID] = i
	}
	// children[i] = indices of nodes that depend on plan.Tasks[i].
	children := make([][]int, len(plan.Tasks))
	indeg := make([]int, len(plan.Tasks))
	for i, t := range plan.Tasks {
		for _, dep := range t.DependsOn {
			parent, ok := idIndex[dep]
			if !ok {
				return nil, fmt.Errorf("node %s depends on unknown %s (plan/compile bug — should have been caught upstream)", t.NodeID, dep)
			}
			children[parent] = append(children[parent], i)
			indeg[i]++
		}
	}
	queue := make([]int, 0, len(plan.Tasks))
	for i := range plan.Tasks {
		if indeg[i] == 0 {
			queue = append(queue, i)
		}
	}
	out := make([]int, 0, len(plan.Tasks))
	for len(queue) > 0 {
		head := queue[0]
		queue = queue[1:]
		out = append(out, head)
		for _, child := range children[head] {
			indeg[child]--
			if indeg[child] == 0 {
				queue = append(queue, child)
			}
		}
	}
	if len(out) != len(plan.Tasks) {
		return nil, fmt.Errorf("topo: residual cycle (compile validator bug)")
	}
	return out, nil
}

// translateDeps maps the workflow's node-ID dependencies onto the
// concrete sesh task IDs returned by AddTask. Every depends_on entry
// must already be in nodeToTaskID — topoOrder guarantees this for
// any cycle-free plan.
func translateDeps(nodeDeps []string, nodeToTaskID map[string]string) ([]string, error) {
	if len(nodeDeps) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(nodeDeps))
	for _, dep := range nodeDeps {
		taskID, ok := nodeToTaskID[dep]
		if !ok {
			return nil, fmt.Errorf("dependency %s not yet applied (topo bug)", dep)
		}
		out = append(out, taskID)
	}
	return out, nil
}
