package subtree

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/danmestas/orch/internal/spawnspec"
)

// dnsLabel constrains Topology.Name and SeshSpawn.Session so they
// round-trip through filesystems, NATS subjects, and tmux pane titles.
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

// Validate checks the post-parse topology against the contract rules
// that the parser deliberately leaves until after defaults are applied:
//
//  1. Topology.Name is DNS-label-shaped (CodeMissingName / CodeBadDNSLabel)
//  2. Sesh: exactly one of Existing or Spawn is set (CodeMissingSesh /
//     CodeSeshXOR / CodeSeshBadScope)
//  3. Each WorkerEntry embeds a valid SpawnSpec
//     (CodeMissingWorkerName / CodeMissingWorkerAgent /
//     CodeMissingExecutor / CodeExecutorXOR / CodeBadAgent /
//     CodeBadWorkerDNSLabel — plus CodeSpawnSpecInvalid for the
//     residual checks delegated to spawnspec)
//  4. Worker names are unique within the subtree (CodeDuplicateWorker)
//  5. State seed entries have non-empty Scope, ScopeID, and the
//     right kind-specific titlebar field (CodeMissingStateScope,
//     CodeMissingStateScopeID, CodeMissingTaskTitle,
//     CodeMissingGoalObjective, CodeNegativeMaxAttempts,
//     CodeNegativeBudgetTokens)
//
// Returns a *Report so callers can grep by code (CI, IDE), surface
// the first error (CLI), or accumulate everything (programmatic
// callers). Use rpt.Err() for the `err != nil` idiom.
func Validate(t *Topology) *Report {
	rpt := &Report{}
	if t == nil {
		rpt.Add(Diagnostic{
			Code: CodeMissingName, Severity: SeverityError,
			Message: "topology is nil",
		})
		return rpt
	}

	validateName(t, rpt)
	validateSesh(&t.Sesh, rpt)
	validateWorkers(t, rpt)
	validateState(t, rpt)
	return rpt
}

func validateName(t *Topology, rpt *Report) {
	if t.Name == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingName, Severity: SeverityError,
			Path: "topology.name", Line: t.SourceLine,
			Message: "topology.name is required",
		})
		return
	}
	if !dnsLabel.MatchString(t.Name) {
		rpt.Add(Diagnostic{
			Code: CodeBadDNSLabel, Severity: SeverityError,
			Path: "topology.name", Line: t.SourceLine,
			Message: fmt.Sprintf("topology.name must be DNS-label-shaped (lowercase, hyphens, no dots); got %q", t.Name),
		})
	}
}

// validateSesh enforces the discriminator XOR. The rule is part of
// the public interface: operators rely on "exactly one of existing /
// spawn" so downstream code can dispatch on which field is set.
func validateSesh(s *SeshSection, rpt *Report) {
	hasExisting := strings.TrimSpace(s.Existing) != ""
	hasSpawn := s.Spawn != nil
	switch {
	case !hasExisting && !hasSpawn:
		rpt.Add(Diagnostic{
			Code: CodeMissingSesh, Severity: SeverityError,
			Path:    "sesh",
			Message: "sesh: must set exactly one of `existing:` (NATS URL) or `spawn:` (fresh hub)",
		})
		return
	case hasExisting && hasSpawn:
		rpt.Add(Diagnostic{
			Code: CodeSeshXOR, Severity: SeverityError,
			Path:    "sesh",
			Message: "sesh: set either `existing:` OR `spawn:`, not both",
		})
		return
	}
	if hasSpawn {
		if s.Spawn.Scope != "" && s.Spawn.Scope != "session" && s.Spawn.Scope != "project" {
			rpt.Add(Diagnostic{
				Code: CodeSeshBadScope, Severity: SeverityError,
				Path:    "sesh.spawn.scope",
				Message: fmt.Sprintf("sesh.spawn.scope: must be one of [session project]; got %q", s.Spawn.Scope),
			})
		}
	}
}

// validateWorkers walks each WorkerEntry and emits typed codes for the
// structural failures (missing name / agent / executor; XOR
// violations; duplicate worker name; agent / DNS-label shape).
//
// We pre-check the well-defined fields ourselves rather than parsing
// the spawnspec validator's error string — that's what gives us stable
// codes per failure mode instead of substring-matching on the message.
// Residual spawnspec failures (outfit shape, env-key shape) still
// flow through under CodeSpawnSpecInvalid; those are operator-rare and
// the message stays as the source of truth until / if they get their
// own codes.
func validateWorkers(t *Topology, rpt *Report) {
	seenWorker := make(map[string]int, len(t.Workers))
	for i := range t.Workers {
		w := &t.Workers[i]
		path := fmt.Sprintf("workers[%d]", i)
		line := w.SourceLine

		// Pre-checks: structural rules we want named codes for.
		preErrs := preCheckSpawnSpec(&w.SpawnSpec, path, line, rpt)

		// If pre-checks already flagged this entry, skip spawnspec
		// delegation — spawnspec would report the same problems under
		// a generic code, drowning the typed diagnostics in noise.
		if !preErrs {
			if err := spawnspec.ValidateSpec(&w.SpawnSpec); err != nil {
				rpt.Add(Diagnostic{
					Code: CodeSpawnSpecInvalid, Severity: SeverityError,
					Path: path, Line: line,
					Message: fmt.Sprintf("%s (%s): %s",
						path, namedFallback(w.Name, i), err.Error()),
				})
				continue
			}
		}

		if w.Name == "" {
			// Already reported above; skip duplicate check to avoid
			// flagging "" twice.
			continue
		}
		if prev, ok := seenWorker[w.Name]; ok {
			rpt.Add(Diagnostic{
				Code: CodeDuplicateWorker, Severity: SeverityError,
				Path: path + ".name", Line: line,
				Message: fmt.Sprintf("duplicate worker name %q (also defined at workers[%d])",
					w.Name, prev),
			})
			continue
		}
		seenWorker[w.Name] = i
	}
}

// preCheckSpawnSpec inspects the SpawnSpec for the structural failures
// that get typed codes. Returns true if any pre-check fired (so the
// caller can skip the residual spawnspec delegation).
func preCheckSpawnSpec(s *spawnspec.SpawnSpec, path string, line int, rpt *Report) bool {
	fired := false

	if s.Name == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingWorkerName, Severity: SeverityError,
			Path: path + ".name", Line: line,
			Message: "worker name is required",
		})
		fired = true
	} else if !dnsLabel.MatchString(s.Name) {
		rpt.Add(Diagnostic{
			Code: CodeBadWorkerDNSLabel, Severity: SeverityError,
			Path: path + ".name", Line: line,
			Message: fmt.Sprintf("worker name must be DNS-label-shaped (lowercase, hyphens, no dots); got %q", s.Name),
		})
		fired = true
	}

	if s.Agent == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingWorkerAgent, Severity: SeverityError,
			Path: path + ".agent", Line: line,
			Message: "worker agent is required",
		})
		fired = true
	} else if !isKnownAgent(s.Agent) {
		rpt.Add(Diagnostic{
			Code: CodeBadAgent, Severity: SeverityError,
			Path: path + ".agent", Line: line,
			Message: fmt.Sprintf("worker agent %q is not one of %v",
				s.Agent, spawnspec.KnownAgents()),
		})
		fired = true
	}

	switch executorCount(s) {
	case 0:
		rpt.Add(Diagnostic{
			Code: CodeMissingExecutor, Severity: SeverityError,
			Path: path, Line: line,
			Message: "worker is missing an executor block (need exactly one of: tmux, cf-worker, cf-durable-object)",
		})
		fired = true
	case 1:
		// good
	default:
		rpt.Add(Diagnostic{
			Code: CodeExecutorXOR, Severity: SeverityError,
			Path: path, Line: line,
			Message: "worker has multiple executor blocks (exactly one of tmux, cf-worker, cf-durable-object allowed)",
		})
		fired = true
	}

	return fired
}

func executorCount(s *spawnspec.SpawnSpec) int {
	n := 0
	if s.Tmux != nil {
		n++
	}
	if s.CFWorker != nil {
		n++
	}
	if s.CFDurableObject != nil {
		n++
	}
	return n
}

func isKnownAgent(a spawnspec.Agent) bool {
	for _, k := range spawnspec.KnownAgents() {
		if a == k {
			return true
		}
	}
	return false
}

func validateState(t *Topology, rpt *Report) {
	for i := range t.State.Tasks {
		validateTaskSeed(&t.State.Tasks[i], i, rpt)
	}
	for i := range t.State.Goals {
		validateGoalSeed(&t.State.Goals[i], i, rpt)
	}
}

func validateTaskSeed(t *TaskSeed, i int, rpt *Report) {
	base := fmt.Sprintf("state.tasks[%d]", i)
	if strings.TrimSpace(t.Scope) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingStateScope, Severity: SeverityError,
			Path: base + ".scope", Message: "scope is required",
		})
	}
	if strings.TrimSpace(t.ScopeID) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingStateScopeID, Severity: SeverityError,
			Path: base + ".scope-id", Message: "scope-id is required",
		})
	}
	if strings.TrimSpace(t.Title) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingTaskTitle, Severity: SeverityError,
			Path: base + ".title", Message: "title is required",
		})
	}
	if t.MaxAttempts < 0 {
		rpt.Add(Diagnostic{
			Code: CodeNegativeMaxAttempts, Severity: SeverityError,
			Path:    base + ".max_attempts",
			Message: fmt.Sprintf("max_attempts must be >= 0; got %d", t.MaxAttempts),
		})
	}
}

func validateGoalSeed(g *GoalSeed, i int, rpt *Report) {
	base := fmt.Sprintf("state.goals[%d]", i)
	if strings.TrimSpace(g.Scope) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingStateScope, Severity: SeverityError,
			Path: base + ".scope", Message: "scope is required",
		})
	}
	if strings.TrimSpace(g.ScopeID) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingStateScopeID, Severity: SeverityError,
			Path: base + ".scope-id", Message: "scope-id is required",
		})
	}
	if strings.TrimSpace(g.Objective) == "" {
		rpt.Add(Diagnostic{
			Code: CodeMissingGoalObjective, Severity: SeverityError,
			Path: base + ".objective", Message: "objective is required",
		})
	}
	if g.BudgetTokens < 0 {
		rpt.Add(Diagnostic{
			Code: CodeNegativeBudgetTokens, Severity: SeverityError,
			Path:    base + ".budget_tokens",
			Message: fmt.Sprintf("budget_tokens must be >= 0; got %d", g.BudgetTokens),
		})
	}
}

func namedFallback(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("[%d]", idx)
}
