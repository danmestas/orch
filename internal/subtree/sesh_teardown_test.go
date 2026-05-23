package subtree

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeshDownTeardown_Normal(t *testing.T) {
	tmp := t.TempDir()
	logFile := filepath.Join(tmp, "sesh.log")
	fake := filepath.Join(tmp, "sesh")
	body := `#!/usr/bin/env bash
printf 'call: %s\n' "$*" > "$SESH_TEST_LOG"
exit 0
`
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	t.Setenv("SESH_TEST_LOG", logFile)
	td := &SeshDownTeardown{BinPath: fake}
	if err := td.Down(context.Background(), "fleet-x"); err != nil {
		t.Fatalf("Down: %v", err)
	}
	logged, _ := os.ReadFile(logFile)
	if !strings.Contains(string(logged), "down --session=fleet-x") {
		t.Errorf("unexpected call: %s", logged)
	}
}

func TestSeshDownTeardown_AlreadyDown(t *testing.T) {
	tmp := t.TempDir()
	fake := filepath.Join(tmp, "sesh-gone")
	body := `#!/usr/bin/env bash
echo "no such session" >&2
exit 1
`
	if err := os.WriteFile(fake, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake: %v", err)
	}
	td := &SeshDownTeardown{BinPath: fake}
	if err := td.Down(context.Background(), "ghost"); err != nil {
		t.Fatalf("expected idempotent nil on already-down; got %v", err)
	}
}

func TestSeshDownTeardown_EmptyLabel(t *testing.T) {
	td := &SeshDownTeardown{}
	if err := td.Down(context.Background(), ""); err == nil {
		t.Fatal("expected error on empty label")
	}
}
