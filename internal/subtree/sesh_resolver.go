package subtree

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// SeshSpawnTimeout is the default wall-clock budget for waiting on
// `sesh up` to publish the session JSON. The hub boots a Fossil
// http server + a NATS leaf + (optionally) a WebSocket listener; on
// laptops this is sub-second, on cold-cache CI runners it can take
// several seconds. The default lands in the middle.
const SeshSpawnTimeout = 30 * time.Second

// LiveSeshResolver is the production SeshResolver. It does the simple
// case (env-expanded `existing:`) inline and shells out to `sesh up`
// for `spawn:` blocks, then polls `.sesh/sessions/<session>.json`
// for the published NATS URL.
//
// Construction-time injection of binPath / sessionsDir / now keeps
// the impl testable without coupling to a global $PATH or a real
// sesh binary.
type LiveSeshResolver struct {
	// BinPath is the absolute path to the `sesh` binary. Empty falls
	// back to looking up `sesh` on $PATH at resolve time.
	BinPath string

	// SessionsDir is the directory where `sesh up` writes
	// `<session>.json`. Empty defaults to `<cwd>/.sesh/sessions`
	// (sesh's own default).
	SessionsDir string

	// Timeout caps the spawn-wait. Zero defaults to SeshSpawnTimeout.
	Timeout time.Duration

	// Stderr receives sesh up's stderr (and the resolver's progress
	// notes). Nil discards.
	Stderr *os.File

	// now is the clock used for the spawn-wait deadline. Test hook.
	now func() time.Time
}

// NewLiveSeshResolver constructs the production resolver with sensible
// defaults: $PATH lookup for sesh, `<cwd>/.sesh/sessions`, 30s
// timeout, stderr to os.Stderr.
func NewLiveSeshResolver() *LiveSeshResolver {
	return &LiveSeshResolver{Stderr: os.Stderr}
}

// Resolve implements SeshResolver. See the SeshResolver doc for the
// contract; the implementation notes per discriminator:
//
//   - `existing:` — Sesh.Existing has already been env-expanded by
//     ResolveEnv at parse time, so we just return it.
//   - `spawn:` — fork-exec `sesh up`, then poll the session JSON
//     until nats_url appears or the timeout fires.
func (r *LiveSeshResolver) Resolve(ctx context.Context, s SeshSection, subtreeName string) (ResolvedSesh, error) {
	if s.Existing != "" {
		// Defensive re-expand for callers that bypassed ResolveEnv.
		expanded := expandEnv(s.Existing, os.Getenv)
		if expanded == "" {
			return ResolvedSesh{}, fmt.Errorf("sesh.existing resolves to empty (env var unset?): %q", s.Existing)
		}
		return ResolvedSesh{URL: expanded, WeSpawnedIt: false}, nil
	}
	if s.Spawn == nil {
		return ResolvedSesh{}, errors.New("sesh: neither existing nor spawn set (validator should have caught this)")
	}

	session := s.Spawn.Session
	if session == "" {
		session = subtreeName
	}
	scope := s.Spawn.Scope
	if scope == "" {
		scope = "session"
	}
	cwd := s.Spawn.Cwd
	if cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return ResolvedSesh{}, fmt.Errorf("sesh spawn: getwd: %w", err)
		}
		cwd = wd
	}

	bin := r.BinPath
	if bin == "" {
		p, err := exec.LookPath("sesh")
		if err != nil {
			return ResolvedSesh{}, fmt.Errorf("sesh spawn: 'sesh' not on PATH: %w", err)
		}
		bin = p
	}

	// `sesh up` blocks until SIGINT, so we launch it detached and
	// poll for the session JSON. The CLI invariant we depend on is:
	// once `<session>.json` exists and contains a non-empty nats_url,
	// the hub is ready to accept leaf connections.
	cmd := exec.CommandContext(ctx, bin, "up",
		"--session="+session,
		"--scope="+scope,
	)
	cmd.Dir = cwd
	cmd.Stdout = r.Stderr
	cmd.Stderr = r.Stderr
	// Detach: a fresh process group so SIGINT to orch-subtree doesn't
	// also reach the hub the operator is still using.
	cmd.SysProcAttr = newSeshProcAttr()
	if err := cmd.Start(); err != nil {
		return ResolvedSesh{}, fmt.Errorf("sesh spawn: start: %w", err)
	}
	// We intentionally do NOT Wait — the hub is meant to outlive the
	// apply. Destroy invokes `sesh down --session=<label>` to tear it
	// down. Reaping is the OS's problem; on a long-lived sesh up the
	// pid is parented to PID 1 (or pid namespace init) when orch
	// exits.

	sessionsDir := r.SessionsDir
	if sessionsDir == "" {
		sessionsDir = filepath.Join(cwd, ".sesh", "sessions")
	}
	jsonPath := filepath.Join(sessionsDir, session+".json")

	timeout := r.Timeout
	if timeout <= 0 {
		timeout = SeshSpawnTimeout
	}
	now := r.now
	if now == nil {
		now = time.Now
	}

	deadline := now().Add(timeout)
	for now().Before(deadline) {
		url, err := readSessionURL(jsonPath)
		if err == nil && url != "" {
			return ResolvedSesh{URL: url, WeSpawnedIt: true}, nil
		}
		select {
		case <-ctx.Done():
			return ResolvedSesh{}, ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
	return ResolvedSesh{}, fmt.Errorf(
		"sesh spawn: timed out after %s waiting for nats_url in %s (is `sesh up` healthy?)",
		timeout, jsonPath,
	)
}

func readSessionURL(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil // not yet written
		}
		return "", err
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return "", nil
	}
	var body struct {
		NATSURL string `json:"nats_url"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		// Half-written file. Caller retries.
		return "", nil
	}
	return body.NATSURL, nil
}
