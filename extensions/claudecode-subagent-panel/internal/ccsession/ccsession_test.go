package ccsession

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFindMostRecentNoProjectsDir(t *testing.T) {
	_, err := FindMostRecent(filepath.Join(t.TempDir(), "does-not-exist"))
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession, got %v", err)
	}
}

func TestFindMostRecentEmpty(t *testing.T) {
	dir := t.TempDir()
	_, err := FindMostRecent(dir)
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession for empty dir, got %v", err)
	}
}

func TestFindMostRecentPicksNewest(t *testing.T) {
	root := t.TempDir()

	// Build: projects/<cwd-enc>/<uuid>/some.jsonl × 2 dirs with
	// different mtimes. We use distinct valid-looking session uuids.
	mk := func(proj, sess string, jsonlMtime time.Time) string {
		p := filepath.Join(root, proj, sess)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		jsonl := filepath.Join(p, "transcript.jsonl")
		if err := os.WriteFile(jsonl, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if err := os.Chtimes(jsonl, jsonlMtime, jsonlMtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
		return p
	}

	now := time.Now()
	old := mk("-Users-op-foo", "11111111-1111-4111-8111-111111111111", now.Add(-1*time.Hour))
	new := mk("-Users-op-bar", "22222222-2222-4222-8222-222222222222", now)

	got, err := FindMostRecent(root)
	if err != nil {
		t.Fatalf("FindMostRecent: %v", err)
	}
	if got.Dir != new {
		t.Errorf("got Dir=%s, want %s (older was %s)", got.Dir, new, old)
	}
	if got.UUID != "22222222-2222-4222-8222-222222222222" {
		t.Errorf("UUID mismatch: %s", got.UUID)
	}
	if got.CWD != "/Users/op/bar" {
		t.Errorf("CWD decoding wrong: got %q", got.CWD)
	}
	if got.SubagentsDir != filepath.Join(new, "subagents") {
		t.Errorf("SubagentsDir wrong: %s", got.SubagentsDir)
	}
}

func TestFindMostRecentSkipsNonUUIDSessions(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-op-foo")
	bad := filepath.Join(proj, "not-a-uuid")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, "x.jsonl"), []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := FindMostRecent(root)
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession when only non-uuid dirs exist, got %v", err)
	}
}

func TestFindMostRecentSkipsDirsWithoutJSONLs(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-op-foo")
	sess := filepath.Join(proj, "33333333-3333-4333-8333-333333333333")
	if err := os.MkdirAll(sess, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	_, err := FindMostRecent(root)
	if !errors.Is(err, ErrNoSession) {
		t.Errorf("expected ErrNoSession when no jsonl files exist, got %v", err)
	}
}

func TestDecodeCWD(t *testing.T) {
	cases := map[string]string{
		"-Users-op-foo":      "/Users/op/foo",
		"":                   "",
		"no-leading-slash":   "",
		"-private-tmp-thing": "/private/tmp/thing",
	}
	for in, want := range cases {
		if got := decodeCWD(in); got != want {
			t.Errorf("decodeCWD(%q) = %q, want %q", in, got, want)
		}
	}
}
