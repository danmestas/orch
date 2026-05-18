package registry

import (
	"fmt"
	"strings"
	"time"
)

// AgentInfo is the slice of a $SRV.INFO.agents reply the registry uses.
// Sources produce these; Join consumes them. Defined here (not in sources/)
// so the join is testable without importing source-specific machinery.
type AgentInfo struct {
	InstanceID string
	Endpoints  []EndpointInfo
	Metadata   map[string]string
}

// EndpointInfo is one entry from the micro service's endpoints list.
type EndpointInfo struct {
	Name    string
	Subject string
}

// Heartbeat carries the last-seen timestamp for one worker, keyed by pane id.
type Heartbeat struct {
	PaneID string
	At     time.Time
}

// JoinInputs aggregates the per-source data the join consumes.
//
// Aliases maps alias name → pane id (operator's ~/.config/orch-aliases).
// OperatorPane is the pane id claimed as the operator (empty if no claim).
// Heartbeats maps pane id → most-recent agents.hb.> timestamp.
// Now is the reference time used to compute Alive (test injection point).
// HBWindow is the alive threshold; zero means DefaultHeartbeatWindow.
type JoinInputs struct {
	Agents       []AgentInfo
	Aliases      map[string]string
	OperatorPane string
	Heartbeats   map[string]time.Time
	Now          time.Time
	HBWindow     time.Duration
}

// Join produces the canonical Worker list from the per-source inputs.
//
// Source precedence:
//
//   - Identity (pane_id, instance_id) comes from $SRV.INFO.agents only.
//     A worker without a $SRV.INFO entry is not in the registry.
//   - Name resolution: alias-file entry > metadata.session > pct-form of pane
//     (e.g. "%64" → "pct64"). Operator's explicit alias always wins.
//   - Operator override: if OperatorPane matches an agent's pane_id, that
//     worker's Role is forced to "operator" even when metadata says
//     otherwise. This preserves backwards compatibility with the legacy
//     ~/.cache/orch-operator.json claim file when the shim metadata
//     hasn't caught up.
//   - Alive: heartbeat-within-window if we've seen a heartbeat; otherwise
//     true (registered on $SRV.INFO but no HB yet — happens in the
//     first ~Interval after spawn).
//
// Output is sorted by PaneID for deterministic test assertions and stable
// rendering in orch-peek.
func Join(in JoinInputs) []Worker {
	if in.HBWindow <= 0 {
		in.HBWindow = DefaultHeartbeatWindow
	}
	if in.Now.IsZero() {
		in.Now = time.Now()
	}

	// Build pane-id → alias-name lookup once (aliases is name → pane).
	paneToAlias := make(map[string]string, len(in.Aliases))
	for name, pane := range in.Aliases {
		paneToAlias[pane] = name
	}

	out := make([]Worker, 0, len(in.Agents))
	for _, a := range in.Agents {
		pane := a.Metadata["pane_id"]
		if pane == "" {
			// Agents without pane_id (e.g. external services that
			// advertise on $SRV.INFO but don't run on a pane) are not
			// "workers" in the registry sense; skip.
			continue
		}

		w := Worker{
			PaneID:     pane,
			InstanceID: a.InstanceID,
			Role:       a.Metadata["role"],
			Outfit:     a.Metadata["outfit"],
			Agent:      a.Metadata["agent"],
			CWD:        a.Metadata["cwd"],
			Owner:      a.Metadata["owner"],
			Session:    a.Metadata["session"],
			Metadata:   a.Metadata,
		}

		// Subjects from endpoint advertisements.
		for _, ep := range a.Endpoints {
			switch ep.Name {
			case "prompt":
				w.Subjects.Prompt = ep.Subject
			case "status":
				w.Subjects.Status = ep.Subject
			}
		}
		// Heartbeat subject is conventional; derive it when we have the
		// tokens. Endpoint advertisement covers prompt/status but the hb
		// subject is implicit in §8.2.
		w.Subjects.HB = heartbeatSubject(a.Metadata)

		// Name precedence: operator alias > session > pct-form fallback.
		if n, ok := paneToAlias[pane]; ok && n != "" {
			w.Name = n
		} else if w.Session != "" {
			w.Name = w.Session
		} else {
			w.Name = paneToPctForm(pane)
		}

		// Operator marker override.
		if in.OperatorPane != "" && pane == in.OperatorPane {
			w.Role = "operator"
		}
		if w.Role == "" {
			w.Role = "worker"
		}

		// Lifecycle.
		if hb, ok := in.Heartbeats[pane]; ok {
			w.LastHB = hb
			w.Alive = in.Now.Sub(hb) <= in.HBWindow
		} else {
			// Registered but no heartbeat seen yet. Alive=true is the
			// safe default — the alternative (Alive=false until first HB)
			// would flap workers on every spawn.
			w.Alive = true
		}

		out = append(out, w)
	}

	sortWorkersByPane(out)
	return out
}

// heartbeatSubject reconstructs agents.hb.<token>.<owner>.<session-or-pane>.
// Mirrors internal/shim/shim.go:heartbeatSubject. Returns empty when the
// required tokens are missing (defensive — a malformed metadata block
// shouldn't crash the join).
func heartbeatSubject(meta map[string]string) string {
	token := agentToken(meta["agent"])
	owner := meta["owner"]
	if token == "" || owner == "" {
		return ""
	}
	tail := meta["session"]
	if tail == "" {
		tail = paneToToken(meta["pane_id"])
	}
	if tail == "" {
		return ""
	}
	return fmt.Sprintf("agents.hb.%s.%s.%s", token, owner, tail)
}

// agentToken mirrors shim.withDefaults: "claude-code" → "cc"; otherwise the
// agent name is its own token. Keep in sync with internal/shim/shim.go.
func agentToken(agent string) string {
	switch agent {
	case "claude-code":
		return "cc"
	default:
		return agent
	}
}

// paneToToken strips the leading "%" and prefixes "pct" to make a tmux
// pane id usable as a NATS subject token. Mirrors shim.encodePane.
func paneToToken(pane string) string {
	if pane == "" {
		return ""
	}
	return "pct" + strings.TrimPrefix(pane, "%")
}

// paneToPctForm renders "%64" as "pct64" for display fallback when no
// alias and no session label are available.
func paneToPctForm(pane string) string {
	return paneToToken(pane)
}
