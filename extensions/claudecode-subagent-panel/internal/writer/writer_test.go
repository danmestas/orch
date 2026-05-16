package writer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAppendCreatesDirAndFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subagents")
	w := New(dir)
	defer func() { _ = w.Close() }()

	if err := w.Append("pct37", []byte(`{"hello":"world"}`+"\n")); err != nil {
		t.Fatalf("Append: %v", err)
	}
	path := filepath.Join(dir, "agent-pct37.jsonl")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != `{"hello":"world"}`+"\n" {
		t.Errorf("file content unexpected: %q", b)
	}
}

func TestAppendIsAppendNotTruncate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subagents")
	w := New(dir)
	defer func() { _ = w.Close() }()
	for _, line := range []string{`{"a":1}`, `{"a":2}`, `{"a":3}`} {
		if err := w.Append("pct37", []byte(line+"\n")); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	b, _ := os.ReadFile(filepath.Join(dir, "agent-pct37.jsonl"))
	want := `{"a":1}` + "\n" + `{"a":2}` + "\n" + `{"a":3}` + "\n"
	if string(b) != want {
		t.Errorf("expected appended content, got %q", b)
	}
}

func TestRestartReopensAndAppends(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subagents")
	w := New(dir)
	if err := w.Append("pct37", []byte("first\n")); err != nil {
		t.Fatalf("Append1: %v", err)
	}
	_ = w.Close()

	w2 := New(dir)
	defer func() { _ = w2.Close() }()
	if err := w2.Append("pct37", []byte("second\n")); err != nil {
		t.Fatalf("Append2: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "agent-pct37.jsonl"))
	if string(b) != "first\nsecond\n" {
		t.Errorf("expected idempotent append across restart, got %q", b)
	}
}

func TestSwapTargetClosesHandles(t *testing.T) {
	base := t.TempDir()
	dir1 := filepath.Join(base, "s1")
	dir2 := filepath.Join(base, "s2")
	w := New(dir1)
	defer func() { _ = w.Close() }()
	_ = w.Append("pct37", []byte("a\n"))
	if err := w.SwapTarget(dir2); err != nil {
		t.Fatalf("SwapTarget: %v", err)
	}
	_ = w.Append("pct37", []byte("b\n"))
	b1, _ := os.ReadFile(filepath.Join(dir1, "agent-pct37.jsonl"))
	b2, _ := os.ReadFile(filepath.Join(dir2, "agent-pct37.jsonl"))
	if string(b1) != "a\n" {
		t.Errorf("dir1 content: got %q, want a\\n", b1)
	}
	if string(b2) != "b\n" {
		t.Errorf("dir2 content: got %q, want b\\n", b2)
	}
}

func TestSweepRemovesOnlyOurFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subagents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Plant a real CC file that must NOT be touched.
	real := filepath.Join(dir, "agent-a1b2c3.jsonl")
	if err := os.WriteFile(real, []byte("real\n"), 0o644); err != nil {
		t.Fatalf("WriteFile real: %v", err)
	}
	w := New(dir)
	_ = w.Append("pct37", []byte("synth\n"))
	_ = w.Close()

	removed := w.Sweep()
	if len(removed) != 1 {
		t.Errorf("expected 1 file swept, got %d (%v)", len(removed), removed)
	}
	if _, err := os.Stat(real); err != nil {
		t.Errorf("real file should have survived sweep: %v", err)
	}
}

func TestEncodePane(t *testing.T) {
	cases := map[string]string{
		"%37":   "pct37",
		"%pp@7": "pctpp-7",
		"":      "pct",
		"%101":  "pct101",
		"%1.2":  "pct1-2",
	}
	for in, want := range cases {
		if got := EncodePane(in); got != want {
			t.Errorf("EncodePane(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsSyntheticPath(t *testing.T) {
	if !IsSyntheticPath("/a/b/subagents/agent-pct37.jsonl") {
		t.Errorf("expected synthetic")
	}
	if IsSyntheticPath("/a/b/subagents/agent-a1b2c3.jsonl") {
		t.Errorf("real CC file should not match synthetic")
	}
	if IsSyntheticPath("/a/b/subagents/agent-pct37.meta.json") {
		t.Errorf("meta files should not match")
	}
}
