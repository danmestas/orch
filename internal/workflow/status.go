package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Status reads the live state of one workflow from the targeted scope
// and aggregates per-node progress. Implementation reads the bucket
// once (a single `task list --json` call), filters to tasks tagged
// with the workflow id, and returns a typed report the CLI renders.
//
// Phase B is a per-call read — no subscription. The proposal allows a
// KV watch in a later phase if polling becomes expensive; today the
// scope holds at most a few dozen tasks per workflow, so a one-shot
// list is the cheapest option.
//
// When multiple task records share a node id (e.g. an earlier
// fingerprint got superseded by a newer apply pass), Status surfaces
// the most-alive record per node — workers and operators care about
// the currently-active task, not orphaned history. The per-status
// totals are tallied across the deduplicated set so the "5/7 done"
// summary matches the visible table.
func Status(ctx context.Context, workflowID, scopeID string, client SeshClient) (*StatusReport, error) {
	if workflowID == "" {
		return nil, fmt.Errorf("status: workflow-id required")
	}
	if scopeID == "" {
		return nil, fmt.Errorf("status: scope-id required")
	}
	if client == nil {
		return nil, fmt.Errorf("status: sesh client is nil")
	}
	all, err := client.ListTasks(ctx, scopeID)
	if err != nil {
		return nil, fmt.Errorf("status: list tasks: %w", err)
	}
	report := &StatusReport{
		WorkflowID: workflowID,
		ScopeID:    scopeID,
	}
	byNode := make(map[string]NodeStatus)
	for _, t := range all {
		md := parseOrchMetadata(t.Metadata)
		if md.WorkflowID != workflowID {
			continue
		}
		next := NodeStatus{
			NodeID:   md.NodeID,
			NodeKind: md.NodeKind,
			TaskID:   t.ID,
			Status:   t.Status,
			Puller:   t.Puller,
		}
		if existing, ok := byNode[md.NodeID]; ok {
			if statusPriority(next.Status) <= statusPriority(existing.Status) {
				continue
			}
		}
		byNode[md.NodeID] = next
	}
	report.Nodes = make([]NodeStatus, 0, len(byNode))
	for _, n := range byNode {
		report.Nodes = append(report.Nodes, n)
	}
	sort.Slice(report.Nodes, func(i, j int) bool {
		return report.Nodes[i].NodeID < report.Nodes[j].NodeID
	})
	report.Totals = tallyStatuses(report.Nodes)
	return report, nil
}

// NodeStatus is one row in a StatusReport.
type NodeStatus struct {
	NodeID   string
	NodeKind string
	TaskID   string
	Status   string
	Puller   string
}

// StatusReport is the per-workflow aggregation Status returns. Totals
// is keyed on the sesh task status string ("pending", "in_progress",
// "blocked", "completed", "failed", "cancelled") so callers can build
// terse "5/7 done" summaries without iterating again.
type StatusReport struct {
	WorkflowID string
	ScopeID    string
	Nodes      []NodeStatus
	Totals     map[string]int
}

// String renders a compact table. One node per line, columns aligned
// so a `column -t`-style view works in any terminal. Followed by a
// single totals line so operators can grep "completed=N" in CI logs.
func (s *StatusReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "workflow=%s scope-id=%s\n", s.WorkflowID, s.ScopeID)
	if len(s.Nodes) == 0 {
		fmt.Fprintln(&b, "  (no tasks found — workflow not applied to this scope?)")
		return b.String()
	}
	widest := 0
	for _, n := range s.Nodes {
		if len(n.NodeID) > widest {
			widest = len(n.NodeID)
		}
	}
	for _, n := range s.Nodes {
		pad := strings.Repeat(" ", widest-len(n.NodeID))
		puller := ""
		if n.Puller != "" {
			puller = " puller=" + n.Puller
		}
		fmt.Fprintf(&b, "  %s%s  status=%-12s kind=%-8s task=%s%s\n",
			n.NodeID, pad, n.Status, n.NodeKind, n.TaskID, puller)
	}
	fmt.Fprintf(&b, "totals: %s\n", formatTotals(s.Totals))
	return b.String()
}

// AllTerminal reports whether every node has reached a terminal
// status. Useful for CI gating ("wait until workflow is done").
func (s *StatusReport) AllTerminal() bool {
	if len(s.Nodes) == 0 {
		return false
	}
	for _, n := range s.Nodes {
		if !isTerminalStatus(n.Status) {
			return false
		}
	}
	return true
}

func tallyStatuses(nodes []NodeStatus) map[string]int {
	out := make(map[string]int)
	for _, n := range nodes {
		out[n.Status]++
	}
	return out
}

func formatTotals(totals map[string]int) string {
	if len(totals) == 0 {
		return "(empty)"
	}
	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, totals[k]))
	}
	return strings.Join(parts, " ")
}

// isTerminalStatus reports whether a sesh task status will never
// transition further. Shared with apply (don't supersede terminal
// records) and status (AllTerminal helper).
func isTerminalStatus(s string) bool {
	switch s {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}
