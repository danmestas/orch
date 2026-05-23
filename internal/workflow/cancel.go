package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Cancel marks every pending task in the workflow as cancelled. The
// implementation delegates to sesh-ops's `goal cleanup-tasks` verb,
// which is the only public CLI surface that CAS-flips a pending task
// to cancelled. We anchor every applied workflow to a sesh goal at
// Apply time precisely so cancel has something concrete to target.
//
// Running (in_progress) and blocked tasks are deliberately left alone
// — sesh-ops's cleanup-tasks only touches pending records, and
// killing pullers is orch-spawn territory (issue #180). For full
// stop, an operator combines `orch workflow cancel` with whatever
// shutdown mechanism their workers expose.
//
// Cancel re-uses Apply's goal lookup (EnsureWorkflowGoal is
// idempotent: it returns the existing goal id without creating a new
// one when called against an already-applied workflow). If a
// workflow_id was never applied, cancel surfaces an empty report
// rather than erroring — operators can safely run cancel against an
// unknown id and get a clean "nothing to do" answer.
func Cancel(ctx context.Context, workflowID, scopeID string, client SeshClient) (*CancelReport, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("cancel: workflow-id required")
	}
	if scopeID == "" {
		return nil, fmt.Errorf("cancel: scope-id required")
	}
	if client == nil {
		return nil, fmt.Errorf("cancel: sesh client is nil")
	}

	report := &CancelReport{WorkflowID: workflowID, ScopeID: scopeID}

	all, err := client.ListTasks(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("cancel: list tasks: %w", err)
	}
	taskIndex := indexTasksByID(all)
	statuses := collectStatuses(all, workflowID)
	if len(statuses.byNode) == 0 {
		return report, nil // workflow was never applied or has no live tasks
	}

	goalID, err := client.EnsureWorkflowGoal(ctx, scopeID, workflowID)
	if err != nil {
		return nil, fmt.Errorf("cancel: locate workflow goal: %w", err)
	}
	report.GoalID = goalID

	cancelledIDs, err := client.CleanupWorkflowTasks(ctx, scopeID, goalID)
	if err != nil {
		return nil, fmt.Errorf("cancel: cleanup-tasks: %w", err)
	}

	cancelledSet := make(map[string]struct{}, len(cancelledIDs))
	for _, id := range cancelledIDs {
		cancelledSet[id] = struct{}{}
	}

	// Render the cancelled tasks first, sorted by node id for stable
	// output across runs. Skipped tasks (in_progress, blocked,
	// already-terminal) follow with their reason so the operator
	// understands the v1 cancel semantic — only pending tasks transition.
	for taskID := range cancelledSet {
		entry, ok := taskIndex[taskID]
		nodeID := ""
		if ok {
			nodeID = parseOrchMetadata(entry.Metadata).NodeID
		}
		report.Cancelled = append(report.Cancelled, CancelledTask{
			NodeID: nodeID, TaskID: taskID, PrevStatus: "pending",
		})
	}
	sort.Slice(report.Cancelled, func(i, j int) bool {
		return report.Cancelled[i].NodeID < report.Cancelled[j].NodeID
	})

	for _, node := range statuses.sortedNodeIDs() {
		entry := statuses.byNode[node]
		if _, did := cancelledSet[entry.TaskID]; did {
			continue
		}
		report.Skipped = append(report.Skipped, SkippedTask{
			NodeID: node,
			TaskID: entry.TaskID,
			Status: entry.Status,
			Reason: skipReason(entry.Status),
		})
	}

	return report, nil
}

// CancelReport summarises one Cancel call.
//
// Cancelled lists the tasks `goal cleanup-tasks` actually transitioned.
// Skipped covers everything else carrying the workflow id — terminal
// records (nothing to do) and in_progress / blocked tasks (out of v1
// scope per #180).
type CancelReport struct {
	WorkflowID string
	ScopeID    string
	GoalID     string
	Cancelled  []CancelledTask
	Skipped    []SkippedTask
}

// CancelledTask is one entry the cancel actually transitioned.
type CancelledTask struct {
	NodeID     string
	TaskID     string
	PrevStatus string
}

// SkippedTask is a task Cancel deliberately left alone.
type SkippedTask struct {
	NodeID string
	TaskID string
	Status string
	Reason string
}

// String renders the report for the CLI. Cancelled first, then
// skipped — matches operator priority of "what happened, what didn't".
func (r *CancelReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow=%s scope-id=%s goal=%s\n", r.WorkflowID, r.ScopeID, r.GoalID)
	fmt.Fprintf(&b, "  cancelled: %d\n", len(r.Cancelled))
	for _, c := range r.Cancelled {
		fmt.Fprintf(&b, "    - %s (%s, was %s)\n", c.NodeID, c.TaskID, c.PrevStatus)
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "  skipped:   %d\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "    . %s (%s, %s — %s)\n", s.NodeID, s.TaskID, s.Status, s.Reason)
		}
	}
	return b.String()
}

// nodeStatusEntry is the apply-pass-through view of a single
// workflow-tagged task, used by Cancel to render the skipped list.
type nodeStatusEntry struct {
	TaskID string
	Status string
}

type workflowStatuses struct {
	byNode map[string]nodeStatusEntry
}

func (w workflowStatuses) sortedNodeIDs() []string {
	ids := make([]string, 0, len(w.byNode))
	for id := range w.byNode {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// collectStatuses keeps the most informative record per node ID. When
// the same node has multiple historical task records (e.g. an earlier
// body that got superseded by a fingerprint change), we surface the
// one in the most "alive" status — the operator cares about what's
// currently running, not the orphaned history.
func collectStatuses(tasks []TaskRecord, workflowID string) workflowStatuses {
	byNode := make(map[string]nodeStatusEntry)
	for _, t := range tasks {
		md := parseOrchMetadata(t.Metadata)
		if md.WorkflowID != workflowID {
			continue
		}
		next := nodeStatusEntry{TaskID: t.ID, Status: t.Status}
		if existing, ok := byNode[md.NodeID]; ok {
			if statusPriority(next.Status) <= statusPriority(existing.Status) {
				continue
			}
		}
		byNode[md.NodeID] = next
	}
	return workflowStatuses{byNode: byNode}
}

// indexTasksByID flips the list into an id-keyed map for cancel's
// cancelled-set reverse lookup.
func indexTasksByID(tasks []TaskRecord) map[string]TaskRecord {
	out := make(map[string]TaskRecord, len(tasks))
	for _, t := range tasks {
		out[t.ID] = t
	}
	return out
}

// statusPriority orders task statuses by "how live" they are. Higher
// priority wins when collectStatuses sees multiple records for the
// same node — operators want the most-alive record surfaced.
func statusPriority(s string) int {
	switch s {
	case "in_progress":
		return 5
	case "blocked":
		return 4
	case "pending":
		return 3
	case "completed":
		return 2
	case "failed":
		return 1
	case "cancelled":
		return 0
	default:
		return -1
	}
}

func skipReason(status string) string {
	switch status {
	case "in_progress":
		return "in-flight; killing pullers is orch-spawn territory (#180)"
	case "blocked":
		return "blocked; sesh-ops cleanup-tasks only cancels pending records"
	case "completed", "failed", "cancelled":
		return "already terminal"
	default:
		return "unknown status"
	}
}
