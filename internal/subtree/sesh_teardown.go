package subtree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
)

// SeshDownTeardown implements SeshTeardown by shelling out to
// `sesh down --session=<label>`. The sesh CLI handles SIGINT'ing the
// foregrounded `sesh up` process; this seeder just invokes it.
//
// Invoked from Destroy only when the subtree owns the hub
// (Topology.Sesh.Spawn != nil). Subtrees that joined an existing hub
// never trigger teardown.
type SeshDownTeardown struct {
	// BinPath is the absolute path to sesh. Empty falls back to
	// $PATH lookup.
	BinPath string

	// Stderr receives sesh's stderr. Nil discards.
	Stderr *os.File
}

// NewSeshDownTeardown constructs the production teardown.
func NewSeshDownTeardown() *SeshDownTeardown {
	return &SeshDownTeardown{Stderr: os.Stderr}
}

// Down implements SeshTeardown.
func (s *SeshDownTeardown) Down(ctx context.Context, sessionLabel string) error {
	if sessionLabel == "" {
		return fmt.Errorf("subtree sesh teardown: empty session label")
	}
	bin := s.BinPath
	if bin == "" {
		p, err := exec.LookPath("sesh")
		if err != nil {
			return fmt.Errorf("subtree sesh teardown: 'sesh' not on PATH: %w", err)
		}
		bin = p
	}
	cmd := exec.CommandContext(ctx, bin, "down", "--session="+sessionLabel)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if s.Stderr != nil {
		cmd.Stderr = stderrTee(s.Stderr, &stderr)
	}
	if err := cmd.Run(); err != nil {
		// `sesh down` on an already-down session is idempotent on
		// the user's end-state but exits non-zero with "no such
		// session". Treat that as success — destroy is idempotent.
		if containsLower(stderr.String(), "no such session") ||
			containsLower(stderr.String(), "not running") {
			return nil
		}
		return fmt.Errorf("subtree sesh teardown %q: %w: %s", sessionLabel, err, stderr.String())
	}
	return nil
}
