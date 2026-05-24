package subtree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stubBin writes an executable shell stub under <dir>/<name> that
// appends its argv to <logFile> on every invocation and exits 0. Used
// to confirm the killer dispatches via the engine-native binary
// (tmux/cmux/zmx) without actually running it.
func stubBin(t *testing.T, dir, name, logFile string) {
	t.Helper()
	body := "#!/usr/bin/env bash\nprintf '" + name + ": %s\\n' \"$*\" >> \"" + logFile + "\"\nexit 0\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

// stubBinExit writes a stub that exits with the given code (and
// appends argv). Used for tests that need to assert the killer
// handles non-zero engine exits.
func stubBinExit(t *testing.T, dir, name, logFile string, exitCode int, stderrLine string) {
	t.Helper()
	body := "#!/usr/bin/env bash\nprintf '" + name + ": %s\\n' \"$*\" >> \"" + logFile + "\"\n"
	if stderrLine != "" {
		body += "echo \"" + stderrLine + "\" >&2\n"
	}
	body += "exit " + itoa(exitCode) + "\n"
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write stub %s: %v", name, err)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// withStubPath prepends dir to $PATH for the test's duration.
func withStubPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestTmuxWorkerKiller_TmuxNormalPath drives the killer against a
// fake tmux on $PATH that records its invocations. We assert: an
// engine-dispatched send-keys C-c happens, then a short wait, then
// kill-pane.
func TestTmuxWorkerKiller_TmuxNormalPath(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	stubBin(t, tmp, "tmux", logFile)
	withStubPath(t, tmp)

	k := &TmuxWorkerKiller{GracePeriod: 10 * time.Millisecond}
	err := k.Kill(context.Background(), "lead", &WorkerHandleRef{
		Executor: "tmux", PaneID: "%5",
		AbortKind: "tmux-send-keys", AbortVerb: "%5", AbortKeys: "C-c",
	})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	got := readFile(t, logFile)
	if !strings.Contains(got, "send-keys -t %5 C-c") {
		t.Errorf("expected send-keys call; got %s", got)
	}
	if !strings.Contains(got, "kill-pane -t %5") {
		t.Errorf("expected kill-pane call; got %s", got)
	}
	// Order check: send-keys must precede kill-pane.
	si := strings.Index(got, "send-keys")
	ki := strings.Index(got, "kill-pane")
	if si < 0 || ki < 0 || si > ki {
		t.Errorf("ordering wrong; got %s", got)
	}
}

// TestTmuxWorkerKiller_AlreadyDead asserts the idempotency contract:
// if the engine binary reports the target is gone, Kill returns nil
// (destroy is idempotent — already-dead is the desired post-state).
// Engine-level Kill swallows the non-zero exit internally, so we just
// confirm the killer sees the swallow as success.
func TestTmuxWorkerKiller_AlreadyDead(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	// Stub exits non-zero on every call (simulates "pane already gone"
	// for both send-keys and kill-pane). tmux.Handle.Kill swallows the
	// non-zero exit, so this should still return nil.
	stubBinExit(t, tmp, "tmux", logFile, 1, "can't find pane: %999")
	withStubPath(t, tmp)

	k := &TmuxWorkerKiller{GracePeriod: 1 * time.Millisecond}
	err := k.Kill(context.Background(), "ghost", &WorkerHandleRef{
		Executor: "tmux", PaneID: "%999",
		AbortKind: "tmux-send-keys", AbortVerb: "%999", AbortKeys: "C-c",
	})
	if err != nil {
		t.Fatalf("expected idempotent nil error; got %v", err)
	}
}

// TestTmuxWorkerKiller_NilHandle covers the legacy-spawn case: no
// cached handle means we don't know which pane to kill. Surface
// clearly rather than silently no-oping (which would orphan the
// pane).
func TestTmuxWorkerKiller_NilHandle(t *testing.T) {
	k := &TmuxWorkerKiller{}
	err := k.Kill(context.Background(), "ghost", nil)
	if err == nil {
		t.Fatal("expected error on nil handle; got nil")
	}
}

// TestTmuxWorkerKiller_CFExecutorUnsupported pins the not-yet-
// implemented message for CF backends so the operator knows what's
// missing rather than seeing a generic "kill failed".
func TestTmuxWorkerKiller_CFExecutorUnsupported(t *testing.T) {
	k := &TmuxWorkerKiller{}
	err := k.Kill(context.Background(), "x", &WorkerHandleRef{
		Executor: "cf-worker",
		PaneID:   "placeholder-locator", // PaneID empty is its own error.
	})
	if err == nil || !strings.Contains(err.Error(), "cf-worker") {
		t.Errorf("expected cf-worker not-supported error; got %v", err)
	}
}

// TestTmuxWorkerKiller_CmuxNormalPath confirms engine dispatch reaches
// the cmux binary with the right argv (send-key ctrl+c → close-surface).
// Pre-#210 this returned "not yet supported"; that gate is gone now.
func TestTmuxWorkerKiller_CmuxNormalPath(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "cmux.log")
	stubBin(t, tmp, "cmux", logFile)
	withStubPath(t, tmp)

	k := &TmuxWorkerKiller{GracePeriod: 10 * time.Millisecond}
	err := k.Kill(context.Background(), "cmux-worker", &WorkerHandleRef{
		Executor: "cmux", PaneID: "surface:30",
	})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	got := readFile(t, logFile)
	if !strings.Contains(got, "send-key --surface surface:30 ctrl+c") {
		t.Errorf("expected cmux send-key call; got %s", got)
	}
	if !strings.Contains(got, "close-surface --surface surface:30") {
		t.Errorf("expected cmux close-surface call; got %s", got)
	}
	si := strings.Index(got, "send-key")
	ki := strings.Index(got, "close-surface")
	if si < 0 || ki < 0 || si > ki {
		t.Errorf("ordering wrong; got %s", got)
	}
}

// TestTmuxWorkerKiller_ZmxNormalPath confirms engine dispatch reaches
// the zmx binary with the right argv (send \x03 → kill --force).
func TestTmuxWorkerKiller_ZmxNormalPath(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "zmx.log")
	stubBin(t, tmp, "zmx", logFile)
	withStubPath(t, tmp)

	k := &TmuxWorkerKiller{GracePeriod: 10 * time.Millisecond}
	err := k.Kill(context.Background(), "zmx-worker", &WorkerHandleRef{
		Executor: "zmx", PaneID: "session-alpha",
	})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	got := readFile(t, logFile)
	if !strings.Contains(got, "send session-alpha") {
		t.Errorf("expected zmx send call; got %s", got)
	}
	if !strings.Contains(got, "kill session-alpha --force") {
		t.Errorf("expected zmx kill --force call; got %s", got)
	}
	si := strings.Index(got, "send ")
	ki := strings.Index(got, "kill ")
	if si < 0 || ki < 0 || si > ki {
		t.Errorf("ordering wrong; got %s", got)
	}
}

// TestTmuxWorkerKiller_MissingLocator surfaces a clear error when the
// cached handle has no engine-native locator (PaneID empty). Previously
// only the tmux branch checked this; the new dispatcher requires a
// locator for every engine.
func TestTmuxWorkerKiller_MissingLocator(t *testing.T) {
	k := &TmuxWorkerKiller{}
	err := k.Kill(context.Background(), "no-locator", &WorkerHandleRef{
		Executor: "tmux",
		PaneID:   "",
	})
	if err == nil || !strings.Contains(err.Error(), "locator") {
		t.Errorf("expected locator-missing error; got %v", err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		// Stub may not have been called — treat as empty.
		return ""
	}
	return string(b)
}
