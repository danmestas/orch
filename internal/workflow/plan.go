package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Plan is the flattened, validation-checked task DAG that the compiler
// would seed into sesh. Phase A's `orch workflow compile --print` emits
// this structure (JSON) so operators can eyeball the result without
// running apply. Phase B replaces the placeholder TaskID derivation
// with calls into sesh-ops's task add.
type Plan struct {
	Workflow string         `json:"workflow"`
	ScopeID  string         `json:"scope_id,omitempty"`
	Tasks    []PlannedTask  `json:"tasks"`
	Warnings []DiagnosticOK `json:"warnings,omitempty"`
}

// PlannedTask is one entry in the compiled DAG. The Description field
// holds the post-compile-time-substitution string; PullRefs lists the
// cross-task references that the puller must resolve at task pull time.
type PlannedTask struct {
	TaskID      string   `json:"task_id"`     // <workflow-name>.<node-id>
	NodeID      string   `json:"node_id"`
	Kind        string   `json:"kind"`
	DependsOn   []string `json:"depends_on,omitempty"`
	Description string   `json:"description,omitempty"`
	Assign      string   `json:"assign,omitempty"`
	PullRefs    []string `json:"pull_refs,omitempty"` // $nodeId.output[.path] refs needing pull-time resolution
}

// DiagnosticOK is the JSON-serializable view of a Diagnostic.
type DiagnosticOK struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	NodeID   string `json:"node_id,omitempty"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
}

// Compile produces a Plan from a validated workflow. The caller must
// run Validate first — Compile returns an error if the workflow has
// any error-severity diagnostics rather than emitting a partial plan
// against invalid input.
//
// Compile-time substitutions ($ENV.*, $WORKFLOW.*) are resolved here.
// Pull-time substitutions ($nodeId.output[.path]) are collected into
// PullRefs but left as-is in Description — the puller (or the runtime
// resolver) replaces them when the task is claimed.
func Compile(wf *Workflow) (*Plan, error) {
	rpt := Validate(wf)
	if !rpt.Valid() {
		return nil, fmt.Errorf("workflow is invalid; refusing to compile:\n%s", rpt.String())
	}
	plan := &Plan{
		Workflow: wf.Name,
		ScopeID:  wf.ScopeID,
		Tasks:    make([]PlannedTask, 0, len(wf.Nodes)),
	}
	for _, w := range rpt.Warnings() {
		plan.Warnings = append(plan.Warnings, DiagnosticOK{
			Code: w.Code, Severity: w.Severity.String(), NodeID: w.NodeID,
			Line: w.Line, Message: w.Message,
		})
	}
	env := envSnapshot()
	for i := range wf.Nodes {
		n := &wf.Nodes[i]
		desc := nodeDescription(n)
		desc = substituteCompileTime(desc, env, wf)
		plan.Tasks = append(plan.Tasks, PlannedTask{
			TaskID:      wf.Name + "." + n.ID,
			NodeID:      n.ID,
			Kind:        string(n.Kind()),
			DependsOn:   append([]string(nil), n.DependsOn...),
			Description: desc,
			Assign:      n.Assign,
			PullRefs:    pullRefRawList(desc),
		})
	}
	return plan, nil
}

// JSON serialises the plan as indented JSON for `compile --print`.
func (p *Plan) JSON() ([]byte, error) { return json.MarshalIndent(p, "", "  ") }

// nodeDescription returns the operator-visible body for a node — the
// string that would land in sesh's `description` field once compiled.
// For non-string-body kinds (script/command/spawn) we emit a stable
// short summary so the plan view is human-readable.
func nodeDescription(n *Node) string {
	switch n.Kind() {
	case KindPrompt:
		return n.Prompt
	case KindBash:
		return n.Bash
	case KindLoop:
		return n.Loop.Prompt
	case KindApproval:
		return n.Approval.Prompt
	case KindScript:
		return fmt.Sprintf("script:%s", n.Script.Name)
	case KindCommand:
		return fmt.Sprintf("command:%s", n.Command.Name)
	case KindSpawn:
		return fmt.Sprintf("spawn:%s", n.Spawn.Name)
	default:
		return ""
	}
}

func envSnapshot() map[string]string {
	out := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

func substituteCompileTime(s string, env map[string]string, wf *Workflow) string {
	if s == "" {
		return s
	}
	refs := ExtractRefs(s)
	if len(refs) == 0 {
		return s
	}
	out := s
	for _, ref := range refs {
		switch ref.Category {
		case CategoryEnv:
			val, ok := env[ref.Name]
			if !ok {
				val = "" // unresolved env → empty string; documented behavior
			}
			out = strings.ReplaceAll(out, ref.Raw, val)
		case CategoryStatic:
			val := workflowStatic(wf, ref.Name)
			out = strings.ReplaceAll(out, ref.Raw, val)
		case CategoryNode:
			// pull-time — leave untouched
		}
	}
	return out
}

func workflowStatic(wf *Workflow, key string) string {
	switch key {
	case "name":
		return wf.Name
	case "scope_id", "scope-id":
		return wf.ScopeID
	case "description":
		return wf.Description
	default:
		return ""
	}
}

func pullRefRawList(desc string) []string {
	refs := NodeRefs(desc)
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	seen := make(map[string]struct{})
	for _, r := range refs {
		if _, dupe := seen[r.Raw]; dupe {
			continue
		}
		seen[r.Raw] = struct{}{}
		out = append(out, r.Raw)
	}
	return out
}
