package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestShimLogs_MissingDirReturnsEmpty(t *testing.T) {
	l := NewShimLogs(filepath.Join(t.TempDir(), "nope"))
	out, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("missing dir should not error: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("want empty list, got %v", out)
	}
}

func TestShimLogs_ListsPctLogsOnly(t *testing.T) {
	dir := t.TempDir()
	mustCreateShimLog(t, filepath.Join(dir, "pct64.log"))
	mustCreateShimLog(t, filepath.Join(dir, "pct99.log"))
	mustCreateShimLog(t, filepath.Join(dir, "stray.txt"))    // ignored (not pct prefix)
	mustCreateShimLog(t, filepath.Join(dir, "pct64.notlog")) // ignored (not .log suffix)

	l := NewShimLogs(dir)
	out, err := l.List(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("want 2 logs, got %d: %v", len(out), out)
	}
}

func TestShimLogs_LogPathReturnsEmptyWhenAbsent(t *testing.T) {
	l := NewShimLogs(t.TempDir())
	if p := l.LogPath("%64"); p != "" {
		t.Errorf("missing log should return empty, got %q", p)
	}
}

func TestShimLogs_LogPathPctEncoding(t *testing.T) {
	dir := t.TempDir()
	mustCreateShimLog(t, filepath.Join(dir, "pct64.log"))
	l := NewShimLogs(dir)
	p := l.LogPath("%64")
	want := filepath.Join(dir, "pct64.log")
	if p != want {
		t.Errorf("got %q want %q", p, want)
	}
}

func mustCreateShimLog(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatalf("create %s: %v", p, err)
	}
}
