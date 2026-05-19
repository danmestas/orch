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
//	1. Topology.Name is DNS-label-shaped
//	2. Sesh: exactly one of Existing or Spawn is set
//	3. Each WorkerEntry embeds a valid SpawnSpec (delegated to
//	   spawnspec.ValidateSpec)
//	4. Worker names are unique within the subtree
//	5. State seed entries have non-empty Scope, ScopeID, and the
//	   right kind-specific titlebar field
//
// Returns a wrapped multi-error so the CLI can surface every problem
// in one shot rather than one fix at a time.
func Validate(t *Topology) error {
	if t == nil {
		return fmt.Errorf("subtree: cannot validate nil Topology")
	}
	var errs []string

	if t.Name == "" {
		errs = append(errs, "topology.name: required")
	} else if !dnsLabel.MatchString(t.Name) {
		errs = append(errs,
			fmt.Sprintf("topology.name: must be DNS-label-shaped (lowercase, hyphens, no dots); got %q",
				t.Name))
	}

	if err := validateSesh(&t.Sesh); err != nil {
		errs = append(errs, err.Error())
	}

	seenWorker := make(map[string]int, len(t.Workers))
	for i := range t.Workers {
		w := &t.Workers[i]
		// Each worker entry is a SpawnSpec — delegate the heavy
		// validation (Agent enum, executor XOR, env-var key shape,
		// outfit XOR) to spawnspec so we stay a thin pass-through
		// instead of redefining the rules.
		if err := spawnspec.ValidateSpec(&w.SpawnSpec); err != nil {
			errs = append(errs,
				fmt.Sprintf("workers[%d] (%s): %s", i, namedFallback(w.Name, i), err.Error()))
			continue
		}
		if prev, ok := seenWorker[w.Name]; ok {
			errs = append(errs,
				fmt.Sprintf("workers[%d].name: duplicate worker name %q (also defined at workers[%d])",
					i, w.Name, prev))
		}
		seenWorker[w.Name] = i
	}

	for i, ts := range t.State.Tasks {
		if err := validateTaskSeed(&ts); err != nil {
			errs = append(errs, fmt.Sprintf("state.tasks[%d]: %s", i, err.Error()))
		}
	}
	for i, gs := range t.State.Goals {
		if err := validateGoalSeed(&gs); err != nil {
			errs = append(errs, fmt.Sprintf("state.goals[%d]: %s", i, err.Error()))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("subtree: validation failed:\n  - %s",
			strings.Join(errs, "\n  - "))
	}
	return nil
}

// validateSesh enforces the discriminator XOR. The rule is part of
// the public interface: operators rely on "exactly one of existing /
// spawn" so downstream code can dispatch on which field is set.
func validateSesh(s *SeshSection) error {
	hasExisting := strings.TrimSpace(s.Existing) != ""
	hasSpawn := s.Spawn != nil
	switch {
	case !hasExisting && !hasSpawn:
		return fmt.Errorf("sesh: must set exactly one of `existing:` (NATS URL) or `spawn:` (fresh hub)")
	case hasExisting && hasSpawn:
		return fmt.Errorf("sesh: set either `existing:` OR `spawn:`, not both")
	}
	if hasSpawn {
		if s.Spawn.Scope != "" && s.Spawn.Scope != "session" && s.Spawn.Scope != "project" {
			return fmt.Errorf("sesh.spawn.scope: must be one of [session project]; got %q", s.Spawn.Scope)
		}
	}
	return nil
}

func validateTaskSeed(t *TaskSeed) error {
	if strings.TrimSpace(t.Scope) == "" {
		return fmt.Errorf("scope: required")
	}
	if strings.TrimSpace(t.ScopeID) == "" {
		return fmt.Errorf("scope-id: required")
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("title: required")
	}
	if t.MaxAttempts < 0 {
		return fmt.Errorf("max_attempts: must be >= 0; got %d", t.MaxAttempts)
	}
	return nil
}

func validateGoalSeed(g *GoalSeed) error {
	if strings.TrimSpace(g.Scope) == "" {
		return fmt.Errorf("scope: required")
	}
	if strings.TrimSpace(g.ScopeID) == "" {
		return fmt.Errorf("scope-id: required")
	}
	if strings.TrimSpace(g.Objective) == "" {
		return fmt.Errorf("objective: required")
	}
	if g.BudgetTokens < 0 {
		return fmt.Errorf("budget_tokens: must be >= 0; got %d", g.BudgetTokens)
	}
	return nil
}

func namedFallback(name string, idx int) string {
	if name != "" {
		return name
	}
	return fmt.Sprintf("[%d]", idx)
}
