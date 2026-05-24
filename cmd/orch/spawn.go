package main

import (
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

// runSpawn is `orch spawn`. Replaces bin/orch-spawn (~418 lines bash) +
// executors/tmux/spawn.sh (~160 lines bash) — closes #189 friction
// point #2. The two scripts collapse to one Go subcommand:
//
//	orch spawn <agent> [flags...]
//
// All flag parsing, pane spawning, readiness polling, slug labeling,
// alias-file maintenance, and shim launch happen in-process. The
// previous bash → bash env-var bridge is gone; the executors/ directory
// is gone with it.
//
// No Executor abstraction: per the 2026-05-23 design call, defer until
// WASM/CF lands a second backend.
func runSpawn(args []string) error {
	opts, err := parseSpawnArgs(args)
	if err != nil {
		return err
	}

	// Composition validation against orch-engines (the closed table).
	// Mirrors bin/orch-spawn's validate_composition() — refuse
	// non-default pairs at flag-parse, not at spawn time.
	if err := validateComposition(opts.Persistence, opts.Layout); err != nil {
		return err
	}

	// Role / class / bridge resolution. All three are derived from
	// outfit/cut/agent unless the operator passed an explicit override.
	opts.resolveRoleClassBridge()
	if err := opts.validateBridgeAgent(); err != nil {
		return err
	}

	// --worktree-from creates a sibling git worktree at the given sha
	// and uses it as cwd. Synthesises a slug when none given. Mutually
	// exclusive with --cwd and --sesh-session (checked inside).
	if err := opts.resolveWorktreeFrom(); err != nil {
		return err
	}

	// Slug regex enforcement + derivation from $SESH_SESSION when no
	// flag set one. Mirrors bin/orch-spawn lines 538-557.
	if err := opts.resolveSlug(); err != nil {
		return err
	}

	// --sesh-session shells out to `sesh worker-cwd <label>` and uses
	// the printed path as cwd. ORCH_SESH_BIN overrides the binary path.
	if err := opts.resolveSeshSession(); err != nil {
		return err
	}

	// --project resolution: zoxide → ~/projects/<name> fallback.
	if err := opts.resolveProject(); err != nil {
		return err
	}

	// Default cwd to current directory if none set yet.
	if opts.Cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("orch spawn: getwd: %w", err)
		}
		opts.Cwd = wd
	}

	// Reject cwd values that would break our shell-quoted WRAP.
	if err := opts.validateCwdShellSafe(); err != nil {
		return err
	}

	// Auto-add the sesh-goal accessory when the operator's parent shell
	// has an active SESH_GOAL_ID and --outfit is in effect.
	opts.maybeAddGoalAccessory()

	// --outfit runs `suit prepare` (claude only for now) and captures
	// the bundle path. Sets opts.Bundle.
	if err := opts.prepareBundle(); err != nil {
		return err
	}

	// Spawn the pane and run readiness verification.
	paneID, spawnRC, err := opts.spawnPane()
	if err != nil {
		return err
	}

	// Pre-pane failure (unknown agent caught before split). PANE empty,
	// no slug / shim work to do — exit with whatever rc the spawn
	// produced.
	if paneID == "" {
		if spawnRC != 0 {
			return &exitError{code: spawnRC}
		}
		return nil
	}

	// Slug labeling: pane title + alias file. Best-effort for the title;
	// alias-file errors are surfaced. Returns the resolved alias file so
	// the collision-check error can name it.
	if err := opts.labelSlug(paneID); err != nil {
		// Slug collision: stdout still carries the pane id (the pane is
		// real and the operator may want to clean it up).
		fmt.Println(paneID)
		fmt.Fprintln(os.Stderr, "orch spawn:", err)
		return &exitError{code: 1}
	}

	// Launch orch-agent-shim alongside the pane (unless --no-shim or
	// bridge=synadia-plugin).
	opts.maybeLaunchShim(paneID)

	// Emit the pane id (issue #185: always, even on verify failure).
	fmt.Println(paneID)
	if spawnRC != 0 {
		return &exitError{code: spawnRC}
	}
	return nil
}

// spawnOpts is the parsed shape of `orch spawn` invocation. Mirrors the
// state bin/orch-spawn carried in shell variables. Field names match
// the bash names where reasonable.
type spawnOpts struct {
	// Positional + flags
	Agent           string
	Cwd             string
	cwdFromFlag     bool
	SeshSession     string
	Project         string
	WorktreeFrom    string
	worktreeFromSet bool
	Slug            string
	slugFromFlag    bool
	ForceSlug       bool
	Headless        bool
	NoFleet         bool
	Quiet           bool
	RoleOverride    string
	ClassOverride   string
	Position        string
	Verify          bool
	Outfit          string
	Cut             string
	Accessories     []string
	NoGoalAccessory bool
	NoShim          bool
	Executor        string
	Bridge          string
	Persistence     string
	Layout          string

	// Derived
	Role   string // worker | observer
	Class  string // active | observer
	Bundle string // suit-prepare bundle dir, if --outfit
}

var (
	slugRE          = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)
	identifierRE    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	cwdInjectionRE  = regexp.MustCompile("[\"'$\\\\`]")
	executorNameMsg = "must match [a-z][a-z0-9-]*"
)

// parseSpawnArgs parses orch-spawn-shaped argv and returns a populated
// spawnOpts. Mirrors bin/orch-spawn's flag-parsing while loop.
func parseSpawnArgs(args []string) (*spawnOpts, error) {
	opts := &spawnOpts{
		Position:    "right",
		Executor:    "tmux",
		Persistence: "tmux",
		Layout:      "tmux",
	}
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return nil, spawnUsageError()
	}
	opts.Agent = args[0]
	args = args[1:]

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// Helper for flags that take a value at args[i+1].
		next := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("orch spawn: %s requires a value", name)
			}
			v := args[i+1]
			i++
			return v, nil
		}
		switch arg {
		case "--cwd":
			v, err := next("--cwd")
			if err != nil {
				return nil, err
			}
			opts.Cwd = v
			opts.cwdFromFlag = true
		case "--sesh-session":
			v, err := next("--sesh-session")
			if err != nil {
				return nil, err
			}
			opts.SeshSession = v
		case "--project":
			v, err := next("--project")
			if err != nil {
				return nil, err
			}
			opts.Project = v
		case "--worktree-from":
			v, err := next("--worktree-from")
			if err != nil {
				return nil, err
			}
			opts.WorktreeFrom = v
			opts.worktreeFromSet = true
		case "--slug", "--instance-id":
			v, err := next(arg)
			if err != nil {
				return nil, err
			}
			opts.Slug = v
			opts.slugFromFlag = true
		case "--force-slug":
			opts.ForceSlug = true
		case "--headless":
			opts.Headless = true
		case "--no-fleet":
			opts.NoFleet = true
		case "--quiet":
			opts.Quiet = true
		case "--role":
			v, err := next("--role")
			if err != nil {
				return nil, err
			}
			if v != "worker" && v != "observer" {
				return nil, fmt.Errorf("orch spawn: --role must be worker or observer (got: %s)", v)
			}
			opts.RoleOverride = v
		case "--class":
			v, err := next("--class")
			if err != nil {
				return nil, err
			}
			opts.ClassOverride = v
		case "--position":
			v, err := next("--position")
			if err != nil {
				return nil, err
			}
			switch v {
			case "right", "left", "above", "below":
				opts.Position = v
			default:
				return nil, fmt.Errorf("orch spawn: --position must be right|left|above|below (got: %s)", v)
			}
		case "--verify":
			opts.Verify = true
		case "--outfit":
			v, err := next("--outfit")
			if err != nil {
				return nil, err
			}
			opts.Outfit = v
		case "--cut":
			v, err := next("--cut")
			if err != nil {
				return nil, err
			}
			opts.Cut = v
		case "--accessory":
			v, err := next("--accessory")
			if err != nil {
				return nil, err
			}
			opts.Accessories = append(opts.Accessories, v)
		case "--no-goal-accessory":
			opts.NoGoalAccessory = true
		case "--no-shim":
			opts.NoShim = true
		case "--bridge":
			v, err := next("--bridge")
			if err != nil {
				return nil, err
			}
			if v != "synadia-plugin" && v != "shim-adapter" {
				return nil, fmt.Errorf("orch spawn: --bridge must be synadia-plugin|shim-adapter (got: %s)", v)
			}
			opts.Bridge = v
		case "--executor":
			v, err := next("--executor")
			if err != nil {
				return nil, err
			}
			// Post-#189: only tmux is supported. The hybrid discovery
			// across in-tree / PATH / env-override was retired with the
			// bash dispatcher. A second executor (WASM/CF) will be
			// reintroduced via a proper Engine interface when it ships.
			if v != "tmux" {
				return nil, fmt.Errorf("orch spawn: --executor=%s is no longer supported (only tmux remains; WASM/CF executors will return with a proper Engine interface)", v)
			}
			opts.Executor = v
		case "--persistence":
			v, err := next("--persistence")
			if err != nil {
				return nil, err
			}
			if !identifierRE.MatchString(v) {
				return nil, fmt.Errorf("orch spawn: --persistence %s (got: %s)", executorNameMsg, v)
			}
			opts.Persistence = v
		case "--layout":
			v, err := next("--layout")
			if err != nil {
				return nil, err
			}
			if !identifierRE.MatchString(v) {
				return nil, fmt.Errorf("orch spawn: --layout %s (got: %s)", executorNameMsg, v)
			}
			opts.Layout = v
		default:
			// Handle --foo=bar forms for the few flags that bin/orch-spawn
			// also accepts that way (--bridge, --class).
			if v, ok := strings.CutPrefix(arg, "--bridge="); ok {
				if v != "synadia-plugin" && v != "shim-adapter" {
					return nil, fmt.Errorf("orch spawn: --bridge must be synadia-plugin|shim-adapter (got: %s)", v)
				}
				opts.Bridge = v
				continue
			}
			if v, ok := strings.CutPrefix(arg, "--class="); ok {
				opts.ClassOverride = v
				continue
			}
			return nil, fmt.Errorf("orch spawn: unknown flag: %s", arg)
		}
	}

	// --quiet: silence stderr by redirecting it to /dev/null. We do
	// this after parsing so the operator still sees parse-error
	// diagnostics (bin/orch-spawn does the same — quiet kicks in after
	// the parse loop).
	if opts.Quiet {
		if devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0); err == nil {
			os.Stderr = devNull
		}
	}

	// --class validation (after parsing so the error path stays clean).
	if opts.ClassOverride != "" && opts.ClassOverride != "active" && opts.ClassOverride != "observer" {
		return nil, &exitError{
			code: 2,
			msg:  fmt.Sprintf(`--class must be "active" or "observer", got %q`, opts.ClassOverride),
		}
	}

	return opts, nil
}

// resolveRoleClassBridge derives the Role / Class / Bridge from the
// other flags. Mirrors bin/orch-spawn lines 404-453.
func (o *spawnOpts) resolveRoleClassBridge() {
	o.Role = o.RoleOverride
	if o.Role == "" {
		switch o.Outfit {
		case "stasi":
			o.Role = "observer"
		}
	}
	if o.Role == "" {
		switch {
		case o.Cut == "wait-watch", strings.HasPrefix(o.Cut, "spy-on-"):
			o.Role = "observer"
		}
	}
	if o.Role == "" {
		o.Role = "worker"
	}

	switch {
	case o.ClassOverride != "":
		o.Class = o.ClassOverride
	case o.Role == "observer":
		o.Class = "observer"
	default:
		o.Class = "active"
	}

	// Default bridge per agent.
	if o.Bridge == "" {
		if o.Agent == "claude" {
			o.Bridge = "synadia-plugin"
		} else {
			o.Bridge = "shim-adapter"
		}
	}
}

// validateBridgeAgent rejects --bridge=synadia-plugin for non-claude
// agents. Mirrors bin/orch-spawn lines 449-453: codex/pi/gemini get
// shim-adapter until Phase B's NATS<->ACP bridge lands.
func (o *spawnOpts) validateBridgeAgent() error {
	if o.Bridge == "synadia-plugin" && o.Agent != "claude" {
		fmt.Fprintln(os.Stderr, "orch spawn: --bridge=synadia-plugin is currently only supported for agent=claude (got:", o.Agent+")")
		fmt.Fprintln(os.Stderr, "orch spawn: codex/pi/gemini use --bridge=shim-adapter until Phase B's NATS↔ACP bridge lands")
		return &exitError{code: 1}
	}
	return nil
}

// validateCwdShellSafe refuses cwd values containing characters that
// would let a path craft shell injection into the spawned pane. Mirrors
// bin/orch-spawn lines 649-661.
func (o *spawnOpts) validateCwdShellSafe() error {
	if cwdInjectionRE.MatchString(o.Cwd) {
		return fmt.Errorf("orch spawn: unsafe characters in cwd (%s) — refusing to build a shell-injection-prone WRAP", o.Cwd)
	}
	return nil
}

// resolveWorktreeFrom mirrors bin/orch-spawn lines 473-524.
func (o *spawnOpts) resolveWorktreeFrom() error {
	if !o.worktreeFromSet {
		return nil
	}
	if o.cwdFromFlag {
		return fmt.Errorf("orch spawn: --worktree-from and --cwd are mutually exclusive (got --worktree-from=%s, --cwd=%s)",
			o.WorktreeFrom, o.Cwd)
	}
	if o.SeshSession != "" {
		return fmt.Errorf("orch spawn: --worktree-from and --sesh-session are mutually exclusive (got --worktree-from=%s, --sesh-session=%s)",
			o.WorktreeFrom, o.SeshSession)
	}
	if _, err := exec.LookPath("git"); err != nil {
		return fmt.Errorf("orch spawn: --worktree-from needs git on PATH")
	}
	// Locate the parent repo via `git rev-parse --show-toplevel`.
	top, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil || strings.TrimSpace(string(top)) == "" {
		cwd, _ := os.Getwd()
		return fmt.Errorf("orch spawn: --worktree-from needs to be run inside a git repository (cwd: %s)", cwd)
	}
	parentRepo := strings.TrimSpace(string(top))

	// Verify the sha resolves to a commit.
	shaOut, err := exec.Command("git", "-C", parentRepo, "rev-parse", "--verify", o.WorktreeFrom+"^{commit}").Output()
	if err != nil {
		return fmt.Errorf("orch spawn: --worktree-from sha not found in repo at %s: %s", parentRepo, o.WorktreeFrom)
	}
	sha := strings.TrimSpace(string(shaOut))

	// Synthesise a slug if none given. Format: <sha7>-<4 hex>.
	if !o.slugFromFlag {
		sha7 := sha
		if len(sha7) > 7 {
			sha7 = sha7[:7]
		}
		o.Slug = fmt.Sprintf("%s-%04x", sha7, rand.Intn(0x10000))
		o.slugFromFlag = true
	}

	// Worktree root + dir.
	root := os.Getenv("ORCH_WORKTREE_ROOT")
	if root == "" {
		projectsRoot := os.Getenv("ORCH_PROJECTS_ROOT")
		if projectsRoot == "" {
			projectsRoot = filepath.Join(os.Getenv("HOME"), "projects")
		}
		root = filepath.Join(projectsRoot, filepath.Base(parentRepo)+"-worktrees")
	}
	wtDir := filepath.Join(root, o.Slug)
	if _, err := os.Stat(wtDir); err == nil {
		return fmt.Errorf("orch spawn: --worktree-from target dir already exists: %s (pick a different --slug or remove the dir)", wtDir)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("orch spawn: mkdir worktree root: %w", err)
	}
	addOut, err := exec.Command("git", "-C", parentRepo, "worktree", "add", wtDir, sha).CombinedOutput()
	if err != nil {
		return fmt.Errorf("orch spawn: git worktree add failed: %s", strings.TrimSpace(string(addOut)))
	}
	o.Cwd = wtDir
	return nil
}

// resolveSlug enforces the slug regex and derives one from $SESH_SESSION
// when no flag set one. Mirrors bin/orch-spawn lines 529-557.
func (o *spawnOpts) resolveSlug() error {
	if o.Slug != "" {
		if !slugRE.MatchString(o.Slug) {
			return fmt.Errorf("orch spawn: --slug must match [a-zA-Z0-9._-]+ (got: %s)", o.Slug)
		}
	}
	if o.Slug == "" {
		if env := os.Getenv("SESH_SESSION"); env != "" && slugRE.MatchString(env) {
			o.Slug = env
			o.slugFromFlag = true
		}
	}
	if o.Slug == "" {
		fmt.Fprintln(os.Stderr, "orch spawn: no --instance-id / --slug given and $SESH_SESSION is unset/invalid — falling back to pct-keyed identity. Migrate to stable slug identity per docs/migration/0009-instance-id.md before the next release deprecates pct-keyed subjects.")
	}
	return nil
}

// resolveSeshSession shells out to `sesh worker-cwd <label>`. Mirrors
// bin/orch-spawn lines 576-626. Mutually exclusive with --cwd.
func (o *spawnOpts) resolveSeshSession() error {
	if o.SeshSession == "" {
		return nil
	}
	if o.cwdFromFlag {
		return fmt.Errorf("orch spawn: --cwd and --sesh-session are mutually exclusive (got --cwd=%s, --sesh-session=%s)", o.Cwd, o.SeshSession)
	}
	binInput := os.Getenv("ORCH_SESH_BIN")
	if binInput == "" {
		binInput = "sesh"
	}
	seshBin, err := resolveSeshBin(binInput)
	if err != nil {
		return fmt.Errorf("orch spawn: --sesh-session needs the 'sesh' binary on PATH (or set ORCH_SESH_BIN=<absolute-path>) — got '%s' — install from https://github.com/danmestas/sesh", binInput)
	}
	out, err := exec.Command(seshBin, "worker-cwd", o.SeshSession).Output()
	if err != nil {
		stderrTail := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderrTail = strings.TrimSpace(string(ee.Stderr))
		}
		rc := -1
		if ee, ok := err.(*exec.ExitError); ok {
			rc = ee.ExitCode()
		}
		return fmt.Errorf("orch spawn: sesh worker-cwd %s failed (exit %d): %s", o.SeshSession, rc, stderrTail)
	}
	path := strings.TrimSpace(string(out))
	if path == "" {
		return fmt.Errorf("orch spawn: sesh worker-cwd %s returned empty stdout", o.SeshSession)
	}
	o.Cwd = path
	return nil
}

// resolveSeshBin matches the ORCH_SESH_BIN resolution rules in
// bin/orch-spawn lines 590-603: absolute path → verify executable;
// relative path with dir → resolve against pwd; bare name → command -v.
func resolveSeshBin(input string) (string, error) {
	if strings.HasPrefix(input, "/") {
		if isExecutable(input) {
			return input, nil
		}
		return "", fmt.Errorf("not executable: %s", input)
	}
	if strings.Contains(input, "/") {
		dir, base := filepath.Split(input)
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return "", err
		}
		resolved := filepath.Join(absDir, base)
		if isExecutable(resolved) {
			return resolved, nil
		}
		return "", fmt.Errorf("not executable: %s", resolved)
	}
	path, err := exec.LookPath(input)
	if err != nil {
		return "", err
	}
	return path, nil
}

// isExecutable returns true when path exists and has any execute bit set.
func isExecutable(path string) bool {
	st, err := os.Stat(path)
	if err != nil {
		return false
	}
	return st.Mode().Perm()&0o111 != 0
}

// resolveProject maps --project <name> to a directory via zoxide query
// then ~/projects/<name> fallback. Mirrors bin/orch-spawn lines 633-647.
func (o *spawnOpts) resolveProject() error {
	if o.Project == "" || o.Cwd != "" {
		return nil
	}
	if _, err := exec.LookPath("zoxide"); err == nil {
		out, _ := exec.Command("zoxide", "query", o.Project).Output()
		if p := strings.TrimSpace(string(out)); p != "" {
			o.Cwd = p
			return nil
		}
	}
	root := os.Getenv("ORCH_PROJECTS_ROOT")
	if root == "" {
		root = filepath.Join(os.Getenv("HOME"), "projects")
	}
	fallback := filepath.Join(root, o.Project)
	if st, err := os.Stat(fallback); err == nil && st.IsDir() {
		o.Cwd = fallback
		return nil
	}
	return fmt.Errorf("orch spawn: cannot resolve --project %s — not in zoxide and not at %s", o.Project, fallback)
}

// maybeAddGoalAccessory adds "sesh-goal" to Accessories when the parent
// shell has SESH_GOAL_ID and --outfit is set. Mirrors bin/orch-spawn
// lines 668-677.
func (o *spawnOpts) maybeAddGoalAccessory() {
	if os.Getenv("SESH_GOAL_ID") == "" || o.Outfit == "" || o.NoGoalAccessory {
		return
	}
	for _, a := range o.Accessories {
		if a == "sesh-goal" {
			return
		}
	}
	o.Accessories = append(o.Accessories, "sesh-goal")
	fmt.Fprintln(os.Stderr, "orch spawn: SESH_GOAL_ID set in parent — auto-adding --accessory=sesh-goal (use --no-goal-accessory to opt out)")
}

// prepareBundle runs `suit prepare` (claude-only) and captures the bundle
// path. Mirrors bin/orch-spawn lines 683-699.
func (o *spawnOpts) prepareBundle() error {
	if o.Outfit == "" {
		return nil
	}
	if o.Agent != "claude" {
		return fmt.Errorf("orch spawn: --outfit currently only supported for agent=claude (saw: %s)", o.Agent)
	}
	if _, err := exec.LookPath("suit"); err != nil {
		return fmt.Errorf("orch spawn: suit not on PATH — install @agent-ops/suit")
	}
	base := []string{"prepare", "--outfit", o.Outfit, "--target", "claude-code"}
	if o.Cut != "" {
		base = append(base, "--cut", o.Cut)
	}
	for _, a := range o.Accessories {
		base = append(base, "--accessory", a)
	}
	// Prefer --quiet (suit v0.10+) for clean stdout=bundle path.
	args := append(append([]string(nil), base...), "--quiet")
	out, err := exec.Command("suit", args...).Output()
	bundle := strings.TrimSpace(string(out))
	if err != nil || bundle == "" {
		// Fall back to legacy capture: stdout's tail line is the path.
		out, err := exec.Command("suit", base...).Output()
		if err != nil {
			return fmt.Errorf("orch spawn: suit prepare failed (no bundle path returned)")
		}
		lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
		if len(lines) == 0 {
			return fmt.Errorf("orch spawn: suit prepare failed (no bundle path returned)")
		}
		bundle = strings.TrimSpace(lines[len(lines)-1])
	}
	if st, err := os.Stat(bundle); err != nil || !st.IsDir() {
		return fmt.Errorf("orch spawn: suit prepare failed (no bundle path returned)")
	}
	o.Bundle = bundle
	return nil
}

// validateComposition checks (persistence, layout) against the closed
// registry owned by orch-engines. Mirrors bin/orch-spawn lines 355-402.
func validateComposition(persistence, layout string) error {
	// Discovery order: ORCH_ENGINES_BIN → orch-engines on PATH → go run
	// fallback. When none resolve, only the default (tmux, tmux) pair
	// is accepted.
	var binPath string
	if v := os.Getenv("ORCH_ENGINES_BIN"); v != "" && isExecutable(v) {
		binPath = v
	} else if p, err := exec.LookPath("orch-engines"); err == nil {
		binPath = p
	}
	if binPath != "" {
		out, err := exec.Command(binPath, "validate", persistence, layout).CombinedOutput()
		if err != nil {
			return &exitError{code: 1, msg: strings.TrimSpace(string(out))}
		}
		return nil
	}

	// Dev fallback: `go run ./cmd/orch-engines validate p l` from the
	// repo root. We locate the repo root the same way bin/orch does —
	// walk up from the binary's own location looking for go.mod.
	if root := findRepoRoot(); root != "" {
		if _, err := os.Stat(filepath.Join(root, "cmd", "orch-engines", "main.go")); err == nil {
			if _, err := exec.LookPath("go"); err == nil {
				cmd := exec.Command("go", "run", "./cmd/orch-engines", "validate", persistence, layout)
				cmd.Dir = root
				out, err := cmd.CombinedOutput()
				if err != nil {
					return &exitError{code: 1, msg: strings.TrimSpace(string(out))}
				}
				return nil
			}
		}
	}

	// Final fallback: only the default pair is accepted without
	// validation.
	if persistence == "tmux" && layout == "tmux" {
		return nil
	}
	return fmt.Errorf("orch spawn: orch-engines binary not found and no go toolchain available; cannot validate composition (persistence=%s layout=%s). Install orch via npm install @agent-ops/orch or set ORCH_ENGINES_BIN", persistence, layout)
}

// findRepoRoot walks up from os.Executable's location until go.mod
// appears. Empty result when not found.
func findRepoRoot() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	dir := filepath.Dir(exe)
	for i := 0; i < 10; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
	return ""
}

// spawnUsageError returns the orch-spawn usage error matching the bash
// usage line exactly.
func spawnUsageError() error {
	return &exitError{
		code: 1,
		msg:  "usage: orch spawn <agent> [--cwd p] [--sesh-session label] [--project n] [--worktree-from sha] [--slug name] [--instance-id name] [--force-slug] [--headless] [--no-fleet] [--quiet] [--role worker|observer] [--class active|observer] [--position right|left|above|below] [--verify] [--outfit X] [--cut Y] [--accessory A] [--no-shim] [--bridge synadia-plugin|shim-adapter] [--executor <name>] [--persistence <name>] [--layout <name>]    env: [ORCH_SESH_BIN=<abs-path>] [ORCH_PROJECTS_ROOT=<dir>] [ORCH_VERIFY_TIMEOUT=<sec>] [ORCH_VERIFY_BACKOFF=1,2,4,8] [ORCH_HEADLESS_SESSION=<name>] [ORCH_WORKTREE_ROOT=<dir>] [ORCH_ALIASES_FILE=<path>] [ORCH_ENGINES_BIN=<abs-path>]",
	}
}

// goalExports builds the shell fragment that exports SESH_GOAL_* vars
// into the spawned pane's WRAP. Empty when no SESH_GOAL_ID is set.
func goalExports() string {
	if os.Getenv("SESH_GOAL_ID") == "" {
		return ""
	}
	var b strings.Builder
	pairs := []struct{ name, value string }{
		{"SESH_GOAL_ID", os.Getenv("SESH_GOAL_ID")},
		{"SESH_GOAL_SCOPE", os.Getenv("SESH_GOAL_SCOPE")},
		{"SESH_GOAL_SCOPE_ID", os.Getenv("SESH_GOAL_SCOPE_ID")},
	}
	for _, p := range pairs {
		if p.value == "" {
			continue
		}
		fmt.Fprintf(&b, " export %s=%s;", p.name, shellQuote(p.value))
	}
	return b.String()
}

// slugExports builds the shell fragment that exports ORCH_INSTANCE_ID
// into the spawned pane. Empty when no slug resolved.
func slugExports(slug string) string {
	if slug == "" {
		return ""
	}
	return fmt.Sprintf(" export ORCH_INSTANCE_ID=%s;", shellQuote(slug))
}

// fleetPromptPath returns the absolute path to the cached fleet doctrine
// (~/.cache/orch-fleet-prompt.md). Used by the WRAP builders.
func fleetPromptPath() string {
	return filepath.Join(os.Getenv("HOME"), ".cache", "orch-fleet-prompt.md")
}

