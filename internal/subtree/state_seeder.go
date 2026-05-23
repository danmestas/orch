package subtree

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// SeshOpsStateSeeder implements StateSeeder by shelling out to
// `sesh-ops` for task add / goal create. sesh-ops owns idempotency
// (CAS on id for tasks, upsert-on-scope-id for goals); the seeder is
// a thin translator from TaskSeed / GoalSeed → CLI flags.
//
// Construction-time injection of BinPath / Stderr keeps the impl
// testable: tests substitute a stub binary that records the flags
// it was called with.
type SeshOpsStateSeeder struct {
	// BinPath is the absolute path to sesh-ops. Empty falls back to
	// $PATH lookup at seed time.
	BinPath string

	// Stderr receives sesh-ops's stderr. Nil discards.
	Stderr *os.File
}

// NewSeshOpsStateSeeder constructs the production seeder with stderr
// inherited.
func NewSeshOpsStateSeeder() *SeshOpsStateSeeder {
	return &SeshOpsStateSeeder{Stderr: os.Stderr}
}

// SeedTask translates a TaskSeed → `sesh-ops task add` flags.
//
//	--server <sesh.URL>
//	--scope <task.Scope>
//	--scope-id <task.ScopeID>
//	--title <task.Title>
//	--depends-on a,b,c           (when DependsOn non-empty)
//	--max-attempts N             (when MaxAttempts > 0)
//	--metadata <json>            (when Metadata non-empty)
//	--extra <json>               (when Extra non-empty — sesh-ops-specific
//	                               passthrough)
//
// Idempotency: sesh-ops `task add` is CAS-on-id when a stable id is
// embedded in metadata; otherwise it creates a new task per call.
// The seeder does NOT add an id field — operators do that in the
// TaskSeed.Metadata when they need replay-safe behaviour, otherwise
// re-applying creates a new task (which surfaces as drift in
// `status`).
func (s *SeshOpsStateSeeder) SeedTask(ctx context.Context, t TaskSeed, sesh ResolvedSesh) error {
	bin, err := s.resolve()
	if err != nil {
		return err
	}
	args := []string{}
	if sesh.URL != "" {
		args = append(args, "--server", sesh.URL)
	}
	if t.Scope != "" {
		args = append(args, "--scope", t.Scope)
	}
	if t.ScopeID != "" {
		args = append(args, "--scope-id", t.ScopeID)
	}
	args = append(args, "task", "add", "--title", t.Title)
	if len(t.DependsOn) > 0 {
		args = append(args, "--depends-on", commaJoin(t.DependsOn))
	}
	if t.MaxAttempts > 0 {
		args = append(args, "--max-attempts", strconv.Itoa(t.MaxAttempts))
	}
	if len(t.Metadata) > 0 {
		j, err := json.Marshal(t.Metadata)
		if err != nil {
			return fmt.Errorf("seed task %q: metadata marshal: %w", t.Title, err)
		}
		args = append(args, "--metadata", string(j))
	}
	if len(t.Extra) > 0 {
		j, err := json.Marshal(t.Extra)
		if err != nil {
			return fmt.Errorf("seed task %q: extra marshal: %w", t.Title, err)
		}
		args = append(args, "--extra", string(j))
	}
	return s.exec(ctx, bin, args, fmt.Sprintf("seed task %q", t.Title))
}

// SeedGoal translates a GoalSeed → `sesh-ops goal create` flags.
//
//	--server <sesh.URL>
//	--scope <goal.Scope>
//	--scope-id <goal.ScopeID>
//	--objective <goal.Objective>
//	--budget-tokens N              (when BudgetTokens > 0)
//	--metadata <json>              (when Metadata non-empty)
//	--extra <json>                 (when Extra non-empty)
//
// Idempotency: sesh-ops upserts goal records by (scope, scope-id)
// pair — re-creating a goal with the same scope-id is a no-op.
func (s *SeshOpsStateSeeder) SeedGoal(ctx context.Context, g GoalSeed, sesh ResolvedSesh) error {
	bin, err := s.resolve()
	if err != nil {
		return err
	}
	args := []string{}
	if sesh.URL != "" {
		args = append(args, "--server", sesh.URL)
	}
	if g.Scope != "" {
		args = append(args, "--scope", g.Scope)
	}
	if g.ScopeID != "" {
		args = append(args, "--scope-id", g.ScopeID)
	}
	args = append(args, "goal", "create", "--objective", g.Objective)
	if g.BudgetTokens > 0 {
		args = append(args, "--budget-tokens", strconv.Itoa(g.BudgetTokens))
	}
	if len(g.Metadata) > 0 {
		j, err := json.Marshal(g.Metadata)
		if err != nil {
			return fmt.Errorf("seed goal %q: metadata marshal: %w", g.Objective, err)
		}
		args = append(args, "--metadata", string(j))
	}
	if len(g.Extra) > 0 {
		j, err := json.Marshal(g.Extra)
		if err != nil {
			return fmt.Errorf("seed goal %q: extra marshal: %w", g.Objective, err)
		}
		args = append(args, "--extra", string(j))
	}
	return s.exec(ctx, bin, args, fmt.Sprintf("seed goal %q", g.Objective))
}

func (s *SeshOpsStateSeeder) resolve() (string, error) {
	if s.BinPath != "" {
		return s.BinPath, nil
	}
	p, err := exec.LookPath("sesh-ops")
	if err != nil {
		return "", fmt.Errorf("subtree state seeder: 'sesh-ops' not on PATH: %w", err)
	}
	return p, nil
}

func (s *SeshOpsStateSeeder) exec(ctx context.Context, bin string, args []string, label string) error {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if s.Stderr != nil {
		cmd.Stderr = stderrTee(s.Stderr, &stderr)
	}
	if err := cmd.Run(); err != nil {
		msg := stderr.String()
		if msg != "" {
			return fmt.Errorf("%s: sesh-ops exit: %w: %s", label, err, msg)
		}
		return fmt.Errorf("%s: sesh-ops exit: %w", label, err)
	}
	return nil
}

func commaJoin(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	return out
}

// stderrTee returns an io.Writer that writes to both targets. We do
// not import io here to keep the dependency surface tight; the
// minimal multiWriter inline is enough.
type stderrTeeWriter struct {
	a, b *os.File
	buf  *bytes.Buffer
}

func stderrTee(a *os.File, b *bytes.Buffer) *stderrTeeWriter {
	return &stderrTeeWriter{a: a, buf: b}
}

func (t *stderrTeeWriter) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.a != nil {
		return t.a.Write(p)
	}
	return len(p), nil
}
