package registry

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOperatorReader_MissingReturnsEmpty(t *testing.T) {
	o := NewOperatorReader(filepath.Join(t.TempDir(), "nope.json"))
	pane, err := o.OperatorPane(context.Background())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if pane != "" {
		t.Errorf("want empty pane, got %q", pane)
	}
}

func TestOperatorReader_ParsesWellFormedClaim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "op.json")
	body := `{"pane_id":"%17","claimed_at":"2026-05-17T12:00:00Z"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	o := NewOperatorReader(path)
	pane, err := o.OperatorPane(context.Background())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if pane != "%17" {
		t.Errorf("got %q want %%17", pane)
	}
}

func TestOperatorReader_CorruptFileErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "op.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	o := NewOperatorReader(path)
	_, err := o.OperatorPane(context.Background())
	if err == nil {
		t.Errorf("corrupt JSON should surface error")
	}
}
