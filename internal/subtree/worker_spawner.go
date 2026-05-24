package subtree

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danmestas/orch/internal/spawnspec"
)

// OrchSpawnWorkerSpawner implements WorkerSpawner by shelling out to
// the `orch spawn` Go subcommand (cmd/orch/spawn.go — post-#189
// replacement for the retired bin/orch-spawn bash dispatcher).
// Translation rules:
//
//	Agent              → positional arg
//	Name               → --instance-id <name>
//	Cwd                → --cwd <path>
//	Session            → --sesh-session <label>
//	Tmux.Headless      → --headless
//	Tmux.Verify        → --verify
//	Tmux.Position      → --position <...>
//	Tmux.Role          → --role <...>
//	Tmux.NoShim        → --no-shim
//	Outfit.Bundle      → --outfit <name> --cut <cut> --accessory <a1>...
//	Outfit.{Name|Cut|Accessories} → --outfit/--cut/--accessory (explicit)
//	Env                → exported in the spawner's environment before exec
//
// `orch spawn` prints the spawned tmux pane id to stdout on success.
// We capture that and build a WorkerHandle around it. The shim
// publishes a richer handle to the bus, but for cache-purposes the
// {pane_id, name, agent} triple is what destroy + status need.
//
// Non-tmux executors (cf-worker, cf-durable-object) aren't supported by
// `orch spawn` yet — we surface a clear "not implemented for
// executor=X" error so the operator gets the signal instead of a
// silent failure.
type OrchSpawnWorkerSpawner struct {
	// BinPath is the absolute path to the `orch` binary. Empty falls
	// back to $PATH lookup at spawn time.
	BinPath string

	// ExtraEnv is appended to the child process's env (in addition
	// to the inherited environment + per-worker SpawnSpec.Env).
	ExtraEnv []string

	// Stderr receives orch-spawn's stderr (useful for verifying
	// spawn diagnostics in tests). Nil discards.
	Stderr *os.File

	// Now stamps WorkerHandle.CreatedAt. Test hook.
	Now func() time.Time
}

// NewOrchSpawnWorkerSpawner constructs the production spawner with
// stderr inherited and Now=time.Now.
func NewOrchSpawnWorkerSpawner() *OrchSpawnWorkerSpawner {
	return &OrchSpawnWorkerSpawner{Stderr: os.Stderr, Now: time.Now}
}

// Spawn implements WorkerSpawner. See type doc for the translation
// table. Returns an error wrapping the orch-spawn stderr when the
// dispatcher exits non-zero.
func (s *OrchSpawnWorkerSpawner) Spawn(ctx context.Context, spec spawnspec.SpawnSpec, sesh ResolvedSesh) (*spawnspec.WorkerHandle, error) {
	if spec.Tmux == nil {
		return nil, fmt.Errorf("subtree spawn %q: only executor=tmux is supported by orch spawn today; received cf-worker/cf-durable-object", spec.Name)
	}

	bin := s.BinPath
	if bin == "" {
		p, err := exec.LookPath("orch")
		if err != nil {
			return nil, fmt.Errorf("subtree spawn %q: 'orch' not on PATH: %w", spec.Name, err)
		}
		bin = p
	}

	args := []string{"spawn", string(spec.Agent)}
	args = append(args, "--instance-id", spec.Name, "--force-slug")
	if spec.Cwd != "" {
		args = append(args, "--cwd", spec.Cwd)
	}
	if spec.Session != "" {
		args = append(args, "--sesh-session", spec.Session)
	}
	if spec.Tmux.Headless {
		args = append(args, "--headless")
	}
	if spec.Tmux.Verify {
		args = append(args, "--verify")
	}
	if spec.Tmux.Position != "" {
		args = append(args, "--position", spec.Tmux.Position)
	}
	if spec.Tmux.Role != "" {
		args = append(args, "--role", spec.Tmux.Role)
	}
	if spec.Tmux.NoShim {
		args = append(args, "--no-shim")
	}
	if spec.Outfit != nil {
		switch {
		case spec.Outfit.Bundle != "":
			// Bundle shorthand: pass as --outfit (the bash side
			// already understands name/cut+accessory parsing via
			// its bundle expander).
			args = append(args, "--outfit", spec.Outfit.Bundle)
		default:
			if spec.Outfit.Name != "" {
				args = append(args, "--outfit", spec.Outfit.Name)
			}
			if spec.Outfit.Cut != "" {
				args = append(args, "--cut", spec.Outfit.Cut)
			}
			for _, a := range spec.Outfit.Accessories {
				args = append(args, "--accessory", a)
			}
		}
	}

	// Build the env for the child. SpawnSpec.Env entries override
	// inherited values; the resolved NATS URL is passed as
	// ORCH_NATS_URL so the shim attaches to the subtree's hub.
	env := os.Environ()
	if sesh.URL != "" {
		env = setEnv(env, "ORCH_NATS_URL", sesh.URL)
	}
	for k, v := range spec.Env {
		env = setEnv(env, k, v)
	}
	env = append(env, s.ExtraEnv...)

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = s.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("subtree spawn %q: orch spawn exit: %w", spec.Name, err)
	}

	pane := strings.TrimSpace(stdout.String())
	if pane == "" {
		return nil, errors.New("subtree spawn: orch spawn returned empty pane id (was --quiet set? or did the agent fail to start?)")
	}

	now := s.Now
	if now == nil {
		now = time.Now
	}
	handle := &spawnspec.WorkerHandle{
		SpecVersion: spawnspec.SpecVersion,
		Name:        spec.Name,
		Agent:       spec.Agent,
		Session:     spec.Session,
		CreatedAt:   now().UTC(),
		Executor:    "tmux",
		PaneID:      pane,
		Status:      "ready",
		Abort: &spawnspec.AbortBlock{
			Kind:   "tmux-send-keys",
			Target: pane,
			Keys:   "C-c",
		},
	}
	return handle, nil
}

// setEnv replaces an existing KEY=... entry in env or appends a new
// one. Last-write-wins is preserved (kept stable for tests).
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, e := range env {
		if strings.HasPrefix(e, prefix) {
			env[i] = key + "=" + value
			return env
		}
	}
	return append(env, key+"="+value)
}
