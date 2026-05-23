package subtree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeTmux(t *testing.T, logFile string, scriptBody string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tmux")
	if err := os.WriteFile(path, []byte(scriptBody), 0o755); err != nil {
		t.Fatalf("write fake tmux: %v", err)
	}
	t.Setenv("TMUX_TEST_LOG", logFile)
	return path
}

// TestTmuxWorkerKiller_NormalPath drives the killer against a fake
// tmux that records its invocations. We assert: send-keys C-c
// happens, then a short wait, then kill-pane.
func TestTmuxWorkerKiller_NormalPath(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	bin := fakeTmux(t, logFile, `#!/usr/bin/env bash
printf 'call: %s\n' "$*" >> "$TMUX_TEST_LOG"
exit 0
`)
	k := &TmuxWorkerKiller{BinPath: bin, GracePeriod: 10 * time.Millisecond}
	err := k.Kill(context.Background(), "lead", &WorkerHandleRef{
		Executor: "tmux", PaneID: "%5",
		AbortKind: "tmux-send-keys", AbortVerb: "%5", AbortKeys: "C-c",
	})
	if err != nil {
		t.Fatalf("Kill: %v", err)
	}
	logged, _ := os.ReadFile(logFile)
	got := string(logged)
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

// TestTmuxWorkerKiller_AlreadyDead asserts the idempotency
// contract: if tmux reports the pane is gone, Kill returns nil
// (destroy is idempotent — already-dead is the desired post-state).
func TestTmuxWorkerKiller_AlreadyDead(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "tmux.log")
	bin := fakeTmux(t, logFile, `#!/usr/bin/env bash
if [[ "$1" == "kill-pane" ]]; then
  echo "can't find pane: %999" >&2
  exit 1
fi
exit 0
`)
	k := &TmuxWorkerKiller{BinPath: bin, GracePeriod: 1 * time.Millisecond}
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
	})
	if err == nil || !strings.Contains(err.Error(), "cf-worker") {
		t.Errorf("expected cf-worker not-supported error; got %v", err)
	}
}
