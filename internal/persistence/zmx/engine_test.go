package zmx

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/persistence"
)

func TestEngineName(t *testing.T) {
	if (&Engine{}).Name() != "zmx" {
		t.Errorf("Engine.Name() = %q, want zmx", (&Engine{}).Name())
	}
}

func TestHandleAccessors(t *testing.T) {
	h := NewHandle("worker-1", "worker-1", "/usr/local/bin/zmx")
	if h.ID() != "worker-1" {
		t.Errorf("ID()=%q want worker-1", h.ID())
	}
	if h.Locator() != "worker-1" {
		t.Errorf("Locator()=%q want worker-1", h.Locator())
	}
}

func TestHandleKillEmptySession(t *testing.T) {
	h := NewHandle("", "", "/usr/local/bin/zmx")
	if err := h.Kill(); err == nil {
		t.Error("Kill on empty session name should error")
	}
}

func TestHandleWaitEmptySession(t *testing.T) {
	h := NewHandle("", "", "/usr/local/bin/zmx")
	if err := h.Wait(context.Background()); err == nil {
		t.Error("Wait on empty session name should error")
	}
}

func TestHandleGracefulShutdownEmptySession(t *testing.T) {
	h := NewHandle("", "", "/usr/local/bin/zmx")
	if err := h.GracefulShutdown(context.Background()); err == nil {
		t.Error("GracefulShutdown on empty session name should error")
	}
}

// TestHandleGracefulShutdownDispatchesSend asserts the handle calls
// `zmx send <name> \x03` against the configured zmxBin.
func TestHandleGracefulShutdownDispatchesSend(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "zmx.log")
	// Write argv literally so we can assert it; \x03 is non-printable
	// so we use `od -c` to capture it.
	stubScript := "#!/usr/bin/env bash\n{ printf 'zmx: ' ; printf '%s ' \"$@\" ; printf '\\n' ; } >> \"" + logFile + "\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(tmp, "zmx"), []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	h := NewHandle("alpha", "alpha", filepath.Join(tmp, "zmx"))
	if err := h.GracefulShutdown(context.Background()); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
	b, err := os.ReadFile(logFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	got := string(b)
	// The send verb + session name are stable; the \x03 byte is
	// inherently non-printable, so we just confirm the prefix is right.
	if !strings.Contains(got, "zmx: send alpha ") {
		t.Errorf("expected zmx send call; got %q", got)
	}
}

// TestHandleGracefulShutdownIdempotent confirms a non-zero stub exit
// (mimicking "session already gone") is swallowed by the handle.
func TestHandleGracefulShutdownIdempotent(t *testing.T) {
	tmp := t.TempDir()
	stubScript := "#!/usr/bin/env bash\nexit 1\n"
	bin := filepath.Join(tmp, "zmx")
	if err := os.WriteFile(bin, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	h := NewHandle("ghost", "ghost", bin)
	if err := h.GracefulShutdown(context.Background()); err != nil {
		t.Errorf("GracefulShutdown should swallow non-zero exit; got %v", err)
	}
}

// Verify --position rejection produces a clear operator-facing error.
// zmx is sessions-only; an explicit non-right --position is a category
// error and must surface as such before any spawn work happens.
func TestEngineStartRejectsExplicitPosition(t *testing.T) {
	e := &Engine{zmxBin: "/nonexistent/zmx"}
	res, err := e.Start(persistence.StartSpec{
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Position: "left",
	})
	if err == nil {
		t.Fatal("--position=left with zmx should error")
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1", res.RC)
	}
	if !strings.Contains(err.Error(), "--position=left is not supported") {
		t.Errorf("error missing operator-facing guidance: %v", err)
	}
}

// Defaulted Position="right" is accepted silently — same forgiving
// shape as tmux's fallback-to-right. Operators passing the explicit
// non-right value get the rejection above; operators leaving the
// default in place don't get punished for the dispatcher's default
// behavior.
func TestEngineStartAllowsRightAsDefault(t *testing.T) {
	stub := writeStubZmx(t, stubScript{})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "zmx-default-pos",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Position: "right",
	})
	if err != nil {
		t.Fatalf("--position=right (default) should succeed, got %v", err)
	}
	if res.Handle == nil {
		t.Fatal("Handle should be non-nil on success")
	}
	if res.Handle.Locator() != "zmx-default-pos" {
		t.Errorf("Locator()=%q want zmx-default-pos", res.Handle.Locator())
	}
}

// Headless is accepted (and silently mapped to the same `zmx run -d`
// form orch always uses — zmx is detached-by-design from orch's
// perspective). Verifies the flag doesn't surface a rejection like
// cmux does.
func TestEngineStartAcceptsHeadless(t *testing.T) {
	stub := writeStubZmx(t, stubScript{})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "zmx-headless",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Headless: true,
	})
	if err != nil {
		t.Fatalf("--headless with zmx should succeed, got %v", err)
	}
	if res.RC != 0 {
		t.Errorf("rc=%d want 0", res.RC)
	}
}

// Wrap construction errors (unknown agent) surface from the engine's
// WrapFunc call after the engine-precondition checks. Mirrors tmux/
// cmux ordering.
func TestEngineStartSurfacesWrapError(t *testing.T) {
	stub := writeStubZmx(t, stubScript{})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "zmx-bad-wrap",
		Agent:    "unknown",
		WrapFunc: func() (string, error) { return "", os.ErrInvalid },
	})
	if err == nil {
		t.Fatal("WrapFunc error should bubble up from Start")
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1", res.RC)
	}
}

// zmx-binary-missing surfaces as a clear error, not a panic / silent
// fallthrough. Tests engine.resolveZmxBin's diagnostic.
func TestEngineStartRejectsMissingBinary(t *testing.T) {
	e := &Engine{zmxBin: "/definitely/not/a/real/path/zmx"}
	// resolveZmxBin uses the configured path verbatim — it doesn't
	// stat — so we have to drop zmxBin and rely on PATH. To keep the
	// test self-contained, point PATH at an empty temp dir.
	tmp := t.TempDir()
	t.Setenv("PATH", tmp)
	e.zmxBin = ""
	res, err := e.Start(persistence.StartSpec{
		Slug:     "irrelevant",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
	})
	if err == nil {
		t.Fatal("missing zmx binary should error")
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1", res.RC)
	}
	if !strings.Contains(err.Error(), "zmx not on PATH") {
		t.Errorf("error missing diagnostic guidance: %v", err)
	}
}

// Collision: a session of our chosen name already exists. Engine
// refuses with an operator-facing error rather than letting zmx's
// generic complaint bubble up.
func TestEngineStartDetectsSessionCollision(t *testing.T) {
	stub := writeStubZmx(t, stubScript{listOutput: "collide\n"})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "collide",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
	})
	if err == nil {
		t.Fatal("collision should produce an error")
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1", res.RC)
	}
	if !strings.Contains(err.Error(), "already live") {
		t.Errorf("error missing collision guidance: %v", err)
	}
}

// Verify polls zmx history until the marker appears. The stub script
// fakes a banner in scrollback after one poll attempt.
func TestEngineStartVerifySucceeds(t *testing.T) {
	stub := writeStubZmx(t, stubScript{historyOutput: "Welcome to Claude Code\nbla bla\n"})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "verify-ok",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Verify:   true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.RC != 0 {
		t.Errorf("rc=%d want 0", res.RC)
	}
}

// Verify with a missing marker times out and surfaces RC=1 (but no
// error) — the handle is still returned so the caller can clean up.
func TestEngineStartVerifyTimesOut(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping verify-timeout test in short mode")
	}
	// Shorten the timeout so the test runs fast.
	t.Setenv("ORCH_VERIFY_TIMEOUT", "1")
	stub := writeStubZmx(t, stubScript{historyOutput: "nothing-resembling-a-banner\n"})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "verify-timeout",
		Agent:    "claude",
		WrapFunc: func() (string, error) { return "true", nil },
		Verify:   true,
	})
	if err != nil {
		t.Fatalf("verify timeout should not error: %v", err)
	}
	if res.RC != 1 {
		t.Errorf("rc=%d want 1 on verify timeout", res.RC)
	}
	if res.Handle == nil {
		t.Error("handle should be non-nil even on verify timeout (caller may need to clean up)")
	}
}

// Verify with an unknown agent (no markers) treats as ready
// immediately — operator gets a stderr line but the spawn doesn't
// fail. Mirrors tmux's behavior for agents without a verify table
// entry.
func TestEngineStartVerifyUnknownAgent(t *testing.T) {
	stub := writeStubZmx(t, stubScript{})
	e := &Engine{zmxBin: stub}
	res, err := e.Start(persistence.StartSpec{
		Slug:     "verify-unknown",
		Agent:    "obscure",
		WrapFunc: func() (string, error) { return "true", nil },
		Verify:   true,
	})
	if err != nil {
		t.Fatalf("verify with unknown agent should not error: %v", err)
	}
	if res.RC != 0 {
		t.Errorf("rc=%d want 0 (unknown-agent verify treated as ready)", res.RC)
	}
}

// Kill is idempotent: calling on a session that doesn't exist returns
// nil, not an error. (Stub script exits 0 unconditionally.)
func TestHandleKillIdempotent(t *testing.T) {
	stub := writeStubZmx(t, stubScript{})
	h := NewHandle("ghost", "ghost", stub)
	if err := h.Kill(); err != nil {
		t.Errorf("Kill on (faked) missing session should be idempotent: %v", err)
	}
	// Second kill — also idempotent.
	if err := h.Kill(); err != nil {
		t.Errorf("second Kill should remain idempotent: %v", err)
	}
}

// Wait honors ctx cancellation. The stub returns the session name
// every poll so Wait would loop forever without cancellation.
func TestHandleWaitHonorsContext(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stub script is sh-based")
	}
	stub := writeStubZmx(t, stubScript{listOutput: "live-forever\n"})
	h := NewHandle("live-forever", "live-forever", stub)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := h.Wait(ctx)
	if err == nil {
		t.Fatal("expected ctx-cancellation error")
	}
}

// deriveSessionName: with a slug, return the slug; without, return a
// stable-prefix synthetic name.
func TestDeriveSessionName(t *testing.T) {
	if got := deriveSessionName("alpha"); got != "alpha" {
		t.Errorf("deriveSessionName(alpha)=%q want alpha", got)
	}
	got := deriveSessionName("")
	if !strings.HasPrefix(got, "orch-anon-") {
		t.Errorf("deriveSessionName(empty)=%q want orch-anon-* prefix", got)
	}
}

func TestVerifyMarkersCovered(t *testing.T) {
	for _, a := range []string{"claude", "pi", "codex", "gemini"} {
		if m := verifyMarkers(a); len(m) == 0 {
			t.Errorf("verifyMarkers(%q) returned no markers", a)
		}
	}
	if m := verifyMarkers("unknown-harness"); len(m) != 0 {
		t.Errorf("verifyMarkers(unknown-harness)=%v want empty", m)
	}
}

// Registration sanity: importing this package registers "zmx" with the
// persistence package. We tolerate the package being imported by other
// tests in the same `go test` run (registry persists), so we just
// assert "zmx" appears.
func TestEngineSelfRegisters(t *testing.T) {
	names := persistence.Registered()
	found := false
	for _, n := range names {
		if n == "zmx" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("zmx not in persistence.Registered() = %v", names)
	}
}

// stubScript controls what the fake zmx binary prints for each
// subcommand. Empty fields produce empty stdout (which the engine
// treats as "no sessions" / "empty scrollback" depending on the verb).
type stubScript struct {
	listOutput    string // stdout for `zmx list --short`
	historyOutput string // stdout for `zmx history <name>`
}

// writeStubZmx writes a temporary shell script that fakes zmx's CLI
// surface enough for the engine tests. The first arg selects the
// verb; we hand-route `list --short`, `history`, `run`, and `kill`.
// All other verbs exit 0 with no output.
//
// The stub honors --short by ignoring extra args (`zmx list --short`).
// `zmx run` and `zmx kill` exit 0 unconditionally so happy-path tests
// don't need to care about the args.
func writeStubZmx(t *testing.T, s stubScript) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "zmx")
	script := `#!/bin/sh
case "$1" in
  list)
    printf %s ` + shellSingleQuote(s.listOutput) + `
    ;;
  history)
    printf %s ` + shellSingleQuote(s.historyOutput) + `
    ;;
  run|kill)
    exit 0
    ;;
  *)
    exit 0
    ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}
	return path
}

// shellSingleQuote wraps s in single quotes, escaping embedded single
// quotes the POSIX-portable way. Keeps the stub's printf payload
// well-formed.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
