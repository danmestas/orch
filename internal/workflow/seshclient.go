package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// SeshClient is the seam between the workflow package and the sesh
// runtime. Production wires it to ExecSeshClient (shells out to the
// `sesh-ops` binary); tests inject a fake to avoid touching NATS.
//
// All methods take a context.Context so callers can cancel mid-apply
// (slow KV roundtrip, NATS hiccup). ScopeID is required on every call
// — sesh-ops cannot route a task without it.
//
// The interface is intentionally small. Workflow apply/status/cancel
// need exactly these primitives; broader sesh-ops surface (pull /
// complete / fail) belongs to workers, not the orchestrator.
type SeshClient interface {
	// AddTask creates a new task in the targeted scope and returns its
	// sesh-assigned ID (ULID). Apply uses the returned ID to translate
	// downstream node dependencies into sesh task-ID dependencies.
	AddTask(ctx context.Context, req AddTaskRequest) (string, error)

	// ListTasks returns every task currently in the scope. Used by
	// Apply for idempotency lookup and by Status for DAG aggregation.
	// Both consumers paginate in memory — there is no streaming
	// variant because sesh's KV bucket is intentionally small per
	// workflow scope.
	ListTasks(ctx context.Context, scopeID string) ([]TaskRecord, error)

	// EnsureWorkflowGoal returns the sesh goal id that anchors the
	// named workflow in the given scope, creating one on first call.
	// Subsequent calls in the same scope return the same id so apply
	// remains idempotent across runs.
	//
	// Goal-anchoring exists for cancel: sesh-ops's only public verb
	// that flips a pending task to cancelled is `goal cleanup-tasks`,
	// which scopes the cancel to a goal's linked tasks. The workflow
	// goal is metadata-tagged so it doesn't get confused with a
	// user-created goal in the same scope.
	EnsureWorkflowGoal(ctx context.Context, scopeID, workflowID string) (string, error)

	// LinkTaskToGoal binds a task into the workflow goal so a later
	// goal cleanup-tasks call cancels it. Idempotent on the sesh side
	// (link writes the same goal_id twice with no harm).
	LinkTaskToGoal(ctx context.Context, scopeID, goalID, taskID string) error

	// CleanupWorkflowTasks invokes `sesh-ops goal cleanup-tasks
	// <goal-id>`. The goal-side cleanup-tasks CAS-flips every pending
	// linked task to cancelled and returns the list of task IDs that
	// transitioned. In-progress / blocked / terminal tasks are left
	// alone (killing pullers is orch-spawn territory, #180).
	CleanupWorkflowTasks(ctx context.Context, scopeID, goalID string) ([]string, error)
}

// AddTaskRequest is the typed payload Apply hands to SeshClient.AddTask.
// Mirrors `sesh-ops task add` flags one-to-one so the CLI adapter is a
// thin shim.
type AddTaskRequest struct {
	ScopeID     string
	Title       string
	Description string
	DependsOn   []string
	Metadata    map[string]any
}

// TaskRecord is the JSON-decoded shape of one sesh task. Only the
// fields workflow apply/status/cancel actually consume are surfaced;
// everything else is dropped on read.
type TaskRecord struct {
	ID        string          `json:"id"`
	Title     string          `json:"title"`
	Status    string          `json:"status"`
	Puller    string          `json:"puller,omitempty"`
	DependsOn []string        `json:"depends_on,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// goalRecord is the JSON-decoded shape of one sesh goal. Only the
// fields workflow apply uses for idempotency lookup are surfaced.
type goalRecord struct {
	ID        string          `json:"id"`
	Objective string          `json:"objective"`
	Owner     string          `json:"owner"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

// WorkflowGoalOwner is the value Apply writes into a workflow goal's
// `owner` field. Operators querying with `sesh-ops goal list --owner
// orch-workflow` get the goals workflow apply manages.
const WorkflowGoalOwner = "orch-workflow"

// ExecSeshClient shells out to a sesh-ops binary on the path. Wired by
// the orch-workflow CLI as the production SeshClient.
//
// The constructor takes the per-process settings (binary path, NATS
// URL or session, default scope) so every per-call invocation can be a
// short flag list. Empty SessionFile means "let sesh-ops pick up
// $SESH_OPS_SESSION from the env"; same for Server / Scope.
type ExecSeshClient struct {
	Binary      string // default: "sesh-ops"
	Server      string // --server flag value (empty = inherit)
	SessionFile string // --session flag value (empty = inherit)
	Scope       string // --scope flag value; defaults to "workflow"
}

// NewExecSeshClient returns a client wired with sane defaults. Callers
// override individual fields before use.
func NewExecSeshClient() *ExecSeshClient {
	return &ExecSeshClient{Binary: "sesh-ops", Scope: "workflow"}
}

// AddTask runs `sesh-ops task add` and returns the assigned task ID
// (parsed from the JSON output). Errors include the captured stderr so
// operators see the actual sesh-ops complaint.
func (e *ExecSeshClient) AddTask(ctx context.Context, req AddTaskRequest) (string, error) {
	args := []string{"task", "add", "--title", req.Title, "--scope-id", req.ScopeID}
	if req.Description != "" {
		args = append(args, "--description", req.Description)
	}
	if len(req.DependsOn) > 0 {
		args = append(args, "--depends-on", strings.Join(req.DependsOn, ","))
	}
	if len(req.Metadata) > 0 {
		buf, err := json.Marshal(req.Metadata)
		if err != nil {
			return "", fmt.Errorf("encode metadata: %w", err)
		}
		args = append(args, "--metadata", string(buf))
	}
	out, err := e.run(ctx, args)
	if err != nil {
		return "", err
	}
	var rec TaskRecord
	if err := json.Unmarshal(out, &rec); err != nil {
		return "", fmt.Errorf("decode task add output: %w (raw: %q)", err, string(out))
	}
	if rec.ID == "" {
		return "", fmt.Errorf("sesh-ops task add returned no id (raw: %q)", string(out))
	}
	return rec.ID, nil
}

// ListTasks runs `sesh-ops task list --json` and decodes the result.
func (e *ExecSeshClient) ListTasks(ctx context.Context, scopeID string) ([]TaskRecord, error) {
	args := []string{"task", "list", "--json", "--scope-id", scopeID}
	out, err := e.run(ctx, args)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var rows []TaskRecord
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("decode task list output: %w (raw: %q)", err, string(out))
	}
	return rows, nil
}

// EnsureWorkflowGoal looks up the workflow's anchoring goal (filtering
// by owner + workflow_id in metadata) and creates one if it's missing.
// The goal's objective is the workflow id so `goal status` prints
// something meaningful; metadata tags it as orch-managed so operators
// don't mistake it for one of their own.
func (e *ExecSeshClient) EnsureWorkflowGoal(ctx context.Context, scopeID, workflowID string) (string, error) {
	goals, err := e.listGoals(ctx, scopeID)
	if err != nil {
		return "", err
	}
	if g := findWorkflowGoal(goals, workflowID); g != "" {
		return g, nil
	}
	meta := map[string]any{
		"orch_workflow_goal": true,
		"workflow_id":        workflowID,
	}
	buf, err := json.Marshal(meta)
	if err != nil {
		return "", fmt.Errorf("encode goal metadata: %w", err)
	}
	args := []string{
		"goal", "create",
		"--objective", fmt.Sprintf("orch-workflow: %s", workflowID),
		"--owner", WorkflowGoalOwner,
		"--scope-id", scopeID,
		"--metadata", string(buf),
		"--allow-multiple-roots", // never collide with operator-created root goals
	}
	out, err := e.run(ctx, args)
	if err != nil {
		return "", err
	}
	var g goalRecord
	if err := json.Unmarshal(out, &g); err != nil {
		return "", fmt.Errorf("decode goal create output: %w (raw: %q)", err, string(out))
	}
	if g.ID == "" {
		return "", fmt.Errorf("sesh-ops goal create returned no id (raw: %q)", string(out))
	}
	return g.ID, nil
}

// LinkTaskToGoal calls `sesh-ops goal link-task`. The call is
// idempotent on the sesh side (LinkTask writes the same goal_id twice
// without harm).
func (e *ExecSeshClient) LinkTaskToGoal(ctx context.Context, scopeID, goalID, taskID string) error {
	args := []string{"goal", "link-task", goalID, taskID, "--scope-id", scopeID}
	_, err := e.run(ctx, args)
	return err
}

// CleanupWorkflowTasks calls `sesh-ops goal cleanup-tasks` and parses
// the cancelled task IDs from stdout. sesh-ops prints the list as
// newline-separated IDs; we tolerate any whitespace.
func (e *ExecSeshClient) CleanupWorkflowTasks(ctx context.Context, scopeID, goalID string) ([]string, error) {
	args := []string{"goal", "cleanup-tasks", goalID, "--scope-id", scopeID}
	out, err := e.run(ctx, args)
	if err != nil {
		return nil, err
	}
	return parseCleanupIDs(out), nil
}

// listGoals reads every goal in the scope. Used by EnsureWorkflowGoal
// for idempotent lookup. Filters by owner to keep the wire payload
// small.
func (e *ExecSeshClient) listGoals(ctx context.Context, scopeID string) ([]goalRecord, error) {
	args := []string{"goal", "list", "--json", "--scope-id", scopeID, "--owner", WorkflowGoalOwner}
	out, err := e.run(ctx, args)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return nil, nil
	}
	var rows []goalRecord
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, fmt.Errorf("decode goal list output: %w (raw: %q)", err, string(out))
	}
	return rows, nil
}

// findWorkflowGoal locates the orch-workflow goal for workflowID in a
// pre-fetched list. Returns "" if none matches; the caller treats that
// as "needs create". Match is on metadata.workflow_id rather than the
// human objective so renaming the objective (operator typo fix) is
// non-breaking.
func findWorkflowGoal(goals []goalRecord, workflowID string) string {
	for _, g := range goals {
		var meta struct {
			OrchWorkflowGoal bool   `json:"orch_workflow_goal"`
			WorkflowID       string `json:"workflow_id"`
		}
		if len(g.Metadata) == 0 {
			continue
		}
		if err := json.Unmarshal(g.Metadata, &meta); err != nil {
			continue
		}
		if meta.OrchWorkflowGoal && meta.WorkflowID == workflowID {
			return g.ID
		}
	}
	return ""
}

func parseCleanupIDs(out []byte) []string {
	if len(bytes.TrimSpace(out)) == 0 {
		return nil
	}
	lines := strings.Split(string(out), "\n")
	ids := make([]string, 0, len(lines))
	for _, line := range lines {
		if id := strings.TrimSpace(line); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func (e *ExecSeshClient) run(ctx context.Context, perCall []string) ([]byte, error) {
	bin := e.Binary
	if bin == "" {
		bin = "sesh-ops"
	}
	args := make([]string, 0, len(perCall)+6)
	if e.Server != "" {
		args = append(args, "--server", e.Server)
	}
	if e.SessionFile != "" {
		args = append(args, "--session", e.SessionFile)
	}
	if e.Scope != "" {
		args = append(args, "--scope", e.Scope)
	}
	args = append(args, perCall...)

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s %v: %w (stderr: %s)", bin, args, err, bytes.TrimSpace(stderr.Bytes()))
	}
	return stdout.Bytes(), nil
}
