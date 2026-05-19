// orch-goal-stop-account-daemon subscribes to the Synadia Agent Protocol
// prompt reply subjects and fires sesh-ops goal account on every §6.5
// terminator (zero-byte, no-header message). This replaces the claude-code-
// specific Stop hook (hooks/orch-goal-stop-account.sh) with a mechanism that
// works uniformly across all harnesses: claude-code, codex, pi, gemini.
//
// One daemon instance is bound to one goal. It is launched by orch-goal-pursue
// and exits when sent SIGTERM/SIGINT or when the goal is cleared.
//
// Subject pattern: agents.prompt.*.<owner>.*.>
//
//	  ^             ^  ^   ^      ^  ^-- reply-inbox tokens (one or more)
//	  |             |  |   |      +----- pane-encoded (wildcarded — any pane)
//	  |             |  |   +------------ owner (this daemon's goal owner; filter)
//	  |             |  +---------------- agent-token (cc/codex/pi/gemini)
//	  |             +------------------- "prompt" endpoint namespace
//	  +--------------------------------- "agents" service namespace
//
// Session-level filtering rationale: on a shared NATS server (e.g. a hub
// running for multiple operators on the same machine), the prompt namespace
// is global. Subscribing to agents.prompt.*.*.*.> would over-count turns
// produced by unrelated operators' agents. The Synadia subject layout per
// internal/shim/shim.go (promptSubject) is:
//
//	agents.prompt.<agent-token>.<owner>.<pane-enc>
//
// The owner token is the per-operator multi-tenancy boundary baked into the
// subject itself, so we filter on it at the broker. Same-operator concurrent
// goals across panes are out of scope here — orch's current flow is one
// active goal per shell (SESH_GOAL_ID). A future metadata.session-precise
// filter would require querying $SRV.INFO.agents at startup to discover
// instance_ids by session label — higher complexity for a case the current
// single-goal-per-shell model doesn't hit.
//
// Terminator detection (§6.5): msg.Data length == 0 AND len(msg.Header) == 0.
// The ack chunk is also zero-header but is JSON ("{"...) so the zero-byte
// guard is sufficient on its own.
//
// Env (read once at startup):
//
//	SESH_GOAL_ID         — required; goal record id
//	SESH_GOAL_SCOPE      — optional; defaults to "project"
//	SESH_GOAL_SCOPE_ID   — optional; defaults to cwd basename
//	ORCH_GOAL_OWNER      — optional; subject-owner filter (default
//	                       $ORCH_OWNER, then $USER) — must match the owner
//	                       token used by orch-agent-shim for this operator's
//	                       panes
//	ORCH_GOAL_TOKEN_ESTIMATE — optional; per-turn token estimate (default 5000)
//	SESH_OPS_BIN         — optional; sesh-ops binary (default "sesh-ops")
//	NATS_URL             — optional; NATS server URL. Resolution order:
//	                       $NATS_URL → ~/.sesh/hub.nats.url →
//	                       ~/.sesh/hub.url (legacy; emits a deprecation
//	                       warning on stderr) → nats://127.0.0.1:4222.
//
// PID file: ~/.cache/orch-goal-daemon/<goal-id>.pid
// Cleaned up on exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/danmestas/orch/internal/natsurl"
)

const (
	// defaultTokenEstimate matches the Stop hook's default.
	defaultTokenEstimate = 5000

	// maxBackoff caps the reconnect delay.
	maxBackoff = 5 * time.Minute

	// pidDirMode is the permission for the PID directory.
	pidDirMode = 0o700

	// accountTimeout bounds each sesh-ops invocation. A hung sesh-ops
	// (broken hub socket, frozen disk, etc.) would otherwise block the
	// terminator handler forever, queueing all subsequent terminators
	// behind it. 30s is generous for a CAS counter increment but short
	// enough that the daemon recovers within one heartbeat cycle.
	accountTimeout = 30 * time.Second
)

// promptSubject builds the NATS subject this daemon subscribes to. The
// owner token is locked to this daemon's owner so terminators emitted by
// other operators' agents on the same NATS server don't double-count.
//
// agents.prompt.<agent-token>.<owner>.<pane-enc>.<reply-inbox-tail>
//
// Wildcards: agent-token and pane-enc match any value (any harness, any
// pane this operator owns). The reply-inbox tail uses `>` to capture
// however many tokens the broker generates for the inbox.
func promptSubject(owner string) string {
	return fmt.Sprintf("agents.prompt.*.%s.*.>", owner)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "orch-goal-stop-account-daemon: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	pidPath, err := writePID(cfg.goalID)
	if err != nil {
		// Non-fatal: log and continue. PID file is best-effort.
		log.Printf("warn: could not write PID file: %v", err)
	} else {
		defer removePID(pidPath)
	}

	log.SetFlags(log.LstdFlags | log.LUTC)
	log.Printf("orch-goal-stop-account-daemon starting: goal=%s owner=%s nats=%s estimate=%d",
		cfg.goalID, cfg.owner, cfg.natsURL, cfg.tokenEstimate)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	return subscribe(ctx, cfg)
}

// config holds values read from env once at startup.
type config struct {
	goalID        string
	scope         string
	scopeID       string
	owner         string // subject-owner filter — see promptSubject
	tokenEstimate int
	seshOpsBin    string
	natsURL       string
}

func loadConfig() (config, error) {
	goalID := os.Getenv("SESH_GOAL_ID")
	if goalID == "" {
		return config{}, errors.New("SESH_GOAL_ID is required but not set")
	}

	scopeID := os.Getenv("SESH_GOAL_SCOPE_ID")
	if scopeID == "" {
		cwd, _ := os.Getwd()
		scopeID = sanitizeScopeID(filepath.Base(cwd))
	}

	estimate := defaultTokenEstimate
	if raw := os.Getenv("ORCH_GOAL_TOKEN_ESTIMATE"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return config{}, fmt.Errorf("ORCH_GOAL_TOKEN_ESTIMATE must be a positive integer, got %q", raw)
		}
		estimate = n
	}

	seshOpsBin := os.Getenv("SESH_OPS_BIN")
	if seshOpsBin == "" {
		seshOpsBin = "sesh-ops"
	}

	// Owner resolution mirrors orch-agent-shim/main.go so the daemon's
	// subject filter matches the same panes the shim is publishing on.
	owner := firstNonEmpty(os.Getenv("ORCH_GOAL_OWNER"), os.Getenv("ORCH_OWNER"), os.Getenv("USER"))
	if owner == "" {
		return config{}, errors.New("could not resolve owner: set ORCH_GOAL_OWNER, ORCH_OWNER, or USER")
	}

	return config{
		goalID:        goalID,
		scope:         firstNonEmpty(os.Getenv("SESH_GOAL_SCOPE"), "project"),
		scopeID:       scopeID,
		owner:         owner,
		tokenEstimate: estimate,
		seshOpsBin:    seshOpsBin,
		natsURL:       natsurl.Resolve("orch-goal-stop-account-daemon", ""),
	}, nil
}

// subscribe connects to NATS with bounded exponential backoff and runs the
// terminator watch loop. Re-subscribes on disconnect. Returns only when ctx
// is done.
func subscribe(ctx context.Context, cfg config) error {
	var attempt int
	for {
		err := connectAndWatch(ctx, cfg)
		if ctx.Err() != nil {
			// Clean shutdown requested — not an error.
			return nil
		}
		attempt++
		wait := backoff(attempt)
		log.Printf("orch-goal-stop-account-daemon: disconnected (%v); retry in %s (attempt %d)", err, wait, attempt)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

// connectAndWatch dials NATS, subscribes, and processes terminators until
// the connection drops or ctx is done.
func connectAndWatch(ctx context.Context, cfg config) error {
	nc, err := nats.Connect(cfg.natsURL,
		nats.Name("orch-goal-stop-account-daemon/"+cfg.goalID),
		// Let the subscribe loop handle reconnect logic with bounded backoff.
		nats.MaxReconnects(0),
	)
	if err != nil {
		return fmt.Errorf("dial %q: %w", cfg.natsURL, err)
	}
	defer nc.Drain()

	subject := promptSubject(cfg.owner)
	sub, err := nc.Subscribe(subject, func(msg *nats.Msg) {
		if !isTerminator(msg) {
			return
		}
		if err := accountTurn(ctx, cfg); err != nil {
			log.Printf("warn: goal account failed (goal=%s): %v", cfg.goalID, err)
		} else {
			log.Printf("accounted %d tokens for goal %s", cfg.tokenEstimate, cfg.goalID)
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %q: %w", subject, err)
	}
	defer sub.Unsubscribe() //nolint:errcheck

	log.Printf("subscribed to %s", subject)

	// Block until ctx is cancelled or the NATS connection drops.
	select {
	case <-ctx.Done():
		return nil
	case <-nc.StatusChanged(nats.CLOSED, nats.DISCONNECTED):
		return errors.New("NATS connection closed")
	}
}

// isTerminator returns true for §6.5 terminators: zero bytes AND no headers.
// Regular chunks are JSON objects (non-empty); error-path messages carry
// Nats-Service-Error headers. The §6.4 ack chunk is also JSON and thus
// non-empty. This guard is both necessary and sufficient.
func isTerminator(msg *nats.Msg) bool {
	return len(msg.Data) == 0 && len(msg.Header) == 0
}

// accountTurn calls sesh-ops goal account with the configured token estimate.
// Failure is logged but never propagated to the caller — goal accounting is
// best-effort observability and must not disrupt the daemon's event loop.
//
// Bounded by accountTimeout so a hung sesh-ops can't pin the NATS handler
// goroutine and queue subsequent terminators behind it. The parent ctx
// chains in so daemon shutdown aborts any in-flight accounting call too.
func accountTurn(parentCtx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(parentCtx, accountTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, cfg.seshOpsBin,
		"--scope", cfg.scope,
		"--scope-id", cfg.scopeID,
		"goal", "account",
		cfg.goalID,
		strconv.Itoa(cfg.tokenEstimate),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("sesh-ops timed out after %s: %s", accountTimeout, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writePID creates ~/.cache/orch-goal-daemon/<goal-id>.pid with the daemon's PID.
func writePID(goalID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home dir: %w", err)
	}
	dir := filepath.Join(home, ".cache", "orch-goal-daemon")
	if err := os.MkdirAll(dir, pidDirMode); err != nil {
		return "", fmt.Errorf("mkdir %s: %w", dir, err)
	}
	path := filepath.Join(dir, goalID+".pid")
	if err := os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return path, nil
}

// removePID deletes the PID file on clean exit.
func removePID(path string) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("warn: could not remove PID file %s: %v", path, err)
	}
}

// backoff returns an exponential delay for attempt n (1-indexed), capped at maxBackoff.
func backoff(attempt int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempt-1))) * time.Second
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// sanitizeScopeID mirrors the shell: replace dots and hyphens with underscores.
func sanitizeScopeID(s string) string {
	return strings.NewReplacer(".", "_", "-", "_").Replace(s)
}

// firstNonEmpty returns the first non-empty string from vals.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
