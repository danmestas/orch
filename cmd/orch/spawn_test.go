package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSpawnArgsMissingAgent(t *testing.T) {
	_, err := parseSpawnArgs(nil)
	if err == nil {
		t.Fatal("want error on missing agent, got nil")
	}
	var ee *exitError
	if !errors.As(err, &ee) {
		t.Fatalf("want *exitError, got %T", err)
	}
	if !strings.Contains(ee.msg, "usage: orch spawn") {
		t.Errorf("want usage line, got: %q", ee.msg)
	}
}

func TestParseSpawnArgsFirstArgIsFlag(t *testing.T) {
	_, err := parseSpawnArgs([]string{"--cwd", "/tmp"})
	if err == nil {
		t.Fatal("want error when first arg is a flag")
	}
}

func TestParseSpawnArgsAgentAndFlags(t *testing.T) {
	o, err := parseSpawnArgs([]string{
		"claude",
		"--cwd", "/tmp",
		"--outfit", "engineer",
		"--cut", "focused",
		"--accessory", "sesh-goal",
		"--accessory", "extra",
		"--position", "below",
		"--verify",
		"--no-shim",
	})
	if err != nil {
		t.Fatal(err)
	}
	if o.Agent != "claude" {
		t.Errorf("agent=%q want claude", o.Agent)
	}
	if o.Cwd != "/tmp" || !o.cwdFromFlag {
		t.Errorf("cwd=%q cwdFromFlag=%v", o.Cwd, o.cwdFromFlag)
	}
	if o.Outfit != "engineer" || o.Cut != "focused" {
		t.Errorf("outfit/cut wrong: %q/%q", o.Outfit, o.Cut)
	}
	if len(o.Accessories) != 2 {
		t.Errorf("want 2 accessories, got %d", len(o.Accessories))
	}
	if o.Position != "below" {
		t.Errorf("position=%q want below", o.Position)
	}
	if !o.Verify || !o.NoShim {
		t.Errorf("verify/noshim not set")
	}
}

func TestParseSpawnArgsInstanceIdSynonymForSlug(t *testing.T) {
	o, err := parseSpawnArgs([]string{"claude", "--instance-id", "foo"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Slug != "foo" || !o.slugFromFlag {
		t.Errorf("--instance-id should set Slug=%q (slugFromFlag=%v)", o.Slug, o.slugFromFlag)
	}
}

func TestParseSpawnArgsInvalidPosition(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--position", "diagonal"})
	if err == nil || !strings.Contains(err.Error(), "right|left|above|below") {
		t.Fatalf("want position validation error, got %v", err)
	}
}

func TestParseSpawnArgsInvalidRole(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--role", "spy"})
	if err == nil || !strings.Contains(err.Error(), "worker or observer") {
		t.Fatalf("want role validation error, got %v", err)
	}
}

func TestParseSpawnArgsInvalidBridge(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--bridge", "carrier-pigeon"})
	if err == nil || !strings.Contains(err.Error(), "synadia-plugin|shim-adapter") {
		t.Fatalf("want bridge validation error, got %v", err)
	}
}

func TestParseSpawnArgsBridgeEqualsForm(t *testing.T) {
	o, err := parseSpawnArgs([]string{"claude", "--bridge=shim-adapter"})
	if err != nil {
		t.Fatal(err)
	}
	if o.Bridge != "shim-adapter" {
		t.Errorf("bridge=%q want shim-adapter", o.Bridge)
	}
}

func TestParseSpawnArgsInvalidClassExitCode2(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--class", "weird"})
	if err == nil {
		t.Fatal("want class validation error")
	}
	var ee *exitError
	if !errors.As(err, &ee) || ee.code != 2 {
		t.Fatalf("want exit code 2, got %v (%T)", err, err)
	}
}

func TestParseSpawnArgsExecutorRejectsNonTmux(t *testing.T) {
	for _, bad := range []string{"", "9bad", "BAD", "has space", "cf-worker", "wasm"} {
		if _, err := parseSpawnArgs([]string{"claude", "--executor", bad}); err == nil {
			t.Errorf("want error for executor=%q (only tmux supported post-#189)", bad)
		}
	}
	if _, err := parseSpawnArgs([]string{"claude", "--executor", "tmux"}); err != nil {
		t.Errorf("want no error for executor=tmux, got %v", err)
	}
}

func TestParseSpawnArgsUnknownFlag(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--no-such-flag"})
	if err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Fatalf("want unknown-flag error, got %v", err)
	}
}

func TestParseSpawnArgsFlagsRequireValue(t *testing.T) {
	_, err := parseSpawnArgs([]string{"claude", "--cwd"})
	if err == nil || !strings.Contains(err.Error(), "--cwd requires a value") {
		t.Fatalf("want value-required error, got %v", err)
	}
}

func TestResolveRoleClassBridgeStasiOutfit(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Outfit: "stasi"}
	o.resolveRoleClassBridge()
	if o.Role != "observer" {
		t.Errorf("stasi outfit should auto-role observer, got %q", o.Role)
	}
	if o.Class != "observer" {
		t.Errorf("observer role should derive class=observer, got %q", o.Class)
	}
}

func TestResolveRoleClassBridgeWaitWatchCut(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Cut: "wait-watch"}
	o.resolveRoleClassBridge()
	if o.Role != "observer" {
		t.Errorf("wait-watch cut should auto-role observer, got %q", o.Role)
	}
}

func TestResolveRoleClassBridgeSpyOnCut(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Cut: "spy-on-engineer"}
	o.resolveRoleClassBridge()
	if o.Role != "observer" {
		t.Errorf("spy-on-* cut should auto-role observer, got %q", o.Role)
	}
}

func TestResolveRoleClassBridgeExplicitOverridesAuto(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Outfit: "stasi", RoleOverride: "worker"}
	o.resolveRoleClassBridge()
	if o.Role != "worker" {
		t.Errorf("explicit --role worker should win, got %q", o.Role)
	}
	// Class defaults to active when role=worker and no --class override.
	if o.Class != "active" {
		t.Errorf("worker role default class=active, got %q", o.Class)
	}
}

func TestResolveRoleClassBridgeExplicitClassOverridesDerived(t *testing.T) {
	o := &spawnOpts{Agent: "claude", Outfit: "stasi", ClassOverride: "active"}
	o.resolveRoleClassBridge()
	if o.Class != "active" {
		t.Errorf("explicit --class active should win over derived observer, got %q", o.Class)
	}
}

func TestResolveRoleClassBridgeDefaultBridgePerAgent(t *testing.T) {
	tests := []struct{ agent, want string }{
		{"claude", "synadia-plugin"},
		{"codex", "shim-adapter"},
		{"pi", "shim-adapter"},
		{"gemini", "shim-adapter"},
	}
	for _, tt := range tests {
		o := &spawnOpts{Agent: tt.agent}
		o.resolveRoleClassBridge()
		if o.Bridge != tt.want {
			t.Errorf("agent=%s bridge=%q want %q", tt.agent, o.Bridge, tt.want)
		}
	}
}

func TestValidateCwdShellSafeRejectsInjection(t *testing.T) {
	for _, bad := range []string{
		`/foo"bar`,
		`/foo$bar`,
		"/foo`bar`",
		`/foo\bar`,
		"/foo'bar",
	} {
		o := &spawnOpts{Cwd: bad}
		if err := o.validateCwdShellSafe(); err == nil {
			t.Errorf("want shell-safety reject for %q", bad)
		}
	}
}

func TestValidateCwdShellSafeAllowsNormalPaths(t *testing.T) {
	for _, ok := range []string{
		"/tmp/foo",
		"/home/user/projects/orch",
		"/path with spaces is fine",
	} {
		o := &spawnOpts{Cwd: ok}
		if err := o.validateCwdShellSafe(); err != nil {
			t.Errorf("want no error for %q, got %v", ok, err)
		}
	}
}

func TestResolveSlugRejectsBadCharacters(t *testing.T) {
	o := &spawnOpts{Slug: "a/b"}
	if err := o.resolveSlug(); err == nil {
		t.Fatal("want error for slug with slash")
	}
}

func TestResolveSlugAllowsValidShapes(t *testing.T) {
	for _, ok := range []string{"abc", "abc-123", "abc.def", "abc_def", "Abc-Def-1"} {
		o := &spawnOpts{Slug: ok}
		if err := o.resolveSlug(); err != nil {
			t.Errorf("want no error for slug=%q, got %v", ok, err)
		}
	}
}

func TestResolveSlugDerivesFromSeshSession(t *testing.T) {
	t.Setenv("SESH_SESSION", "my-session")
	o := &spawnOpts{}
	if err := o.resolveSlug(); err != nil {
		t.Fatal(err)
	}
	if o.Slug != "my-session" {
		t.Errorf("slug=%q want my-session", o.Slug)
	}
}

func TestResolveSlugIgnoresInvalidSeshSession(t *testing.T) {
	t.Setenv("SESH_SESSION", "bad/session")
	o := &spawnOpts{}
	if err := o.resolveSlug(); err != nil {
		t.Fatal(err)
	}
	if o.Slug != "" {
		t.Errorf("invalid SESH_SESSION should leave slug empty, got %q", o.Slug)
	}
}

func TestGoalExportsEmptyWithoutSeshGoalID(t *testing.T) {
	t.Setenv("SESH_GOAL_ID", "")
	if got := goalExports(); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestGoalExportsIncludesAvailableVars(t *testing.T) {
	t.Setenv("SESH_GOAL_ID", "g123")
	t.Setenv("SESH_GOAL_SCOPE", "proj")
	t.Setenv("SESH_GOAL_SCOPE_ID", "0a1b2c3d")
	got := goalExports()
	for _, want := range []string{"SESH_GOAL_ID=", "SESH_GOAL_SCOPE=", "SESH_GOAL_SCOPE_ID="} {
		if !strings.Contains(got, want) {
			t.Errorf("goalExports missing %q in %q", want, got)
		}
	}
}

func TestSlugExportsEmpty(t *testing.T) {
	if got := slugExports(""); got != "" {
		t.Errorf("want empty, got %q", got)
	}
}

func TestSlugExportsHasInstanceID(t *testing.T) {
	got := slugExports("my-slug")
	if !strings.Contains(got, "ORCH_INSTANCE_ID=") {
		t.Errorf("want ORCH_INSTANCE_ID=, got %q", got)
	}
	if !strings.Contains(got, "my-slug") {
		t.Errorf("want slug value, got %q", got)
	}
}

func TestClaudeWrapNoBundleHasFleetPrompt(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1") // strip the read; tail
	o := &spawnOpts{Agent: "claude", Cwd: "/tmp", Bridge: "shim-adapter"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrap, `claude --dangerously-skip-permissions`) {
		t.Errorf("want claude --dangerously-skip-permissions in wrap, got %q", wrap)
	}
	if !strings.Contains(wrap, "/h/.cache/orch-fleet-prompt.md") {
		t.Errorf("want fleet prompt path, got %q", wrap)
	}
}

func TestClaudeWrapNoFleetDropsFleetFlag(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "claude", Cwd: "/tmp", Bridge: "shim-adapter", NoFleet: true}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrap, "orch-fleet-prompt.md") {
		t.Errorf("no-fleet should drop fleet prompt, got %q", wrap)
	}
}

func TestClaudeWrapSynadiaPluginAppendsLoadFlag(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "claude", Cwd: "/tmp", Bridge: "synadia-plugin"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrap, "--dangerously-load-development-channels") {
		t.Errorf("synadia-plugin should append load flag, got %q", wrap)
	}
	if !strings.Contains(wrap, "plugin:nats-channel@synadia-plugins") {
		t.Errorf("want plugin identifier, got %q", wrap)
	}
}

func TestClaudeWrapShimAdapterDoesNotAppendLoadFlag(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "claude", Cwd: "/tmp", Bridge: "shim-adapter"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrap, "--dangerously-load-development-channels") {
		t.Errorf("shim-adapter should NOT load synadia plugin, got %q", wrap)
	}
}

func TestPiWrapShape(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "pi", Cwd: "/tmp"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"PI_TELEMETRY=0", "pi --offline", "/h/.cache/orch-fleet-prompt.md"} {
		if !strings.Contains(wrap, want) {
			t.Errorf("pi wrap missing %q in %q", want, wrap)
		}
	}
}

func TestGeminiWrapShape(t *testing.T) {
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "gemini", Cwd: "/tmp"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrap, "gemini --yolo --skip-trust") {
		t.Errorf("gemini wrap missing flags, got %q", wrap)
	}
}

func TestUnknownAgentRejected(t *testing.T) {
	o := &spawnOpts{Agent: "bogus", Cwd: "/tmp"}
	_, err := o.buildWrap()
	if err == nil || !strings.Contains(err.Error(), "unknown agent") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}

func TestPauseOnExitTailEnabledByDefault(t *testing.T) {
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "")
	o := &spawnOpts{Agent: "gemini", Cwd: "/tmp"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(wrap, "exited — press enter") || !strings.Contains(wrap, "exec $SHELL -l") {
		t.Errorf("default wrap should include pause-and-shell tail, got %q", wrap)
	}
}

func TestPauseOnExitTailDisabledWhenEnvSet(t *testing.T) {
	t.Setenv("ORCH_NO_PAUSE_ON_EXIT", "1")
	o := &spawnOpts{Agent: "gemini", Cwd: "/tmp"}
	wrap, err := o.buildWrap()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(wrap, "exited — press enter") {
		t.Errorf("ORCH_NO_PAUSE_ON_EXIT=1 should drop tail, got %q", wrap)
	}
}

func TestLabelSlugWritesAliasEntry(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "orch-aliases")
	t.Setenv("ORCH_ALIASES_FILE", aliasFile)

	o := &spawnOpts{Slug: "my-worker"}
	if err := o.labelSlug("%64"); err != nil {
		t.Fatal(err)
	}
	got := readAliasEntry(aliasFile, "my-worker")
	if got != "%64" {
		t.Errorf("want %q got %q", "%64", got)
	}
}

func TestLabelSlugCollisionRequiresForce(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "orch-aliases")
	t.Setenv("ORCH_ALIASES_FILE", aliasFile)

	// Pre-seed the alias file.
	if err := os.WriteFile(aliasFile, []byte("my-worker=%10\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	o := &spawnOpts{Slug: "my-worker"}
	err := o.labelSlug("%64")
	if err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("want collision error, got %v", err)
	}

	// With --force-slug, the alias takes over.
	o.ForceSlug = true
	if err := o.labelSlug("%64"); err != nil {
		t.Fatalf("--force-slug should accept, got %v", err)
	}
	if got := readAliasEntry(aliasFile, "my-worker"); got != "%64" {
		t.Errorf("force-slug should rewrite alias, got %q", got)
	}
}

func TestLabelSlugIdempotentSamePane(t *testing.T) {
	dir := t.TempDir()
	aliasFile := filepath.Join(dir, "orch-aliases")
	t.Setenv("ORCH_ALIASES_FILE", aliasFile)
	if err := os.WriteFile(aliasFile, []byte("my-worker=%64\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	o := &spawnOpts{Slug: "my-worker"}
	// Same pane → no collision error.
	if err := o.labelSlug("%64"); err != nil {
		t.Fatalf("same-pane idempotent label should succeed, got %v", err)
	}
}

func TestLabelSlugEmptyIsNoOp(t *testing.T) {
	o := &spawnOpts{}
	if err := o.labelSlug("%64"); err != nil {
		t.Fatalf("empty slug should be a no-op, got %v", err)
	}
}

func TestWriteAliasEntryStripsPriorSlug(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "aliases")
	if err := os.WriteFile(path, []byte("foo=%1\nbar=%2\nfoo=%3\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeAliasEntry(path, "foo", "%99"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	got := strings.Split(strings.TrimSpace(string(b)), "\n")
	// Want: bar=%2 (kept), foo=%99 (replacement). No %1, no %3.
	if len(got) != 2 || got[0] != "bar=%2" || got[1] != "foo=%99" {
		t.Errorf("aliases corrupted: %v", got)
	}
}

func TestParseSpawnArgsSlugFromInstanceIdSetsFlag(t *testing.T) {
	o, _ := parseSpawnArgs([]string{"claude", "--instance-id", "x"})
	if !o.slugFromFlag {
		t.Error("--instance-id should set slugFromFlag")
	}
}

func TestParseSpawnArgsPersistenceLayoutNameValidation(t *testing.T) {
	if _, err := parseSpawnArgs([]string{"claude", "--persistence", "TMUX"}); err == nil {
		t.Error("uppercase persistence should reject")
	}
	if _, err := parseSpawnArgs([]string{"claude", "--layout", "9bad"}); err == nil {
		t.Error("layout starting with digit should reject")
	}
}
