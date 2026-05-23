package subtree

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestLiveSeshResolver_Existing covers the simple-path: when
// Sesh.Existing is already populated, the resolver returns it
// verbatim. Env-var expansion happens at parse time (ResolveEnv); the
// resolver does a defensive second pass for callers that bypass.
func TestLiveSeshResolver_Existing(t *testing.T) {
	r := NewLiveSeshResolver()
	got, err := r.Resolve(context.Background(),
		SeshSection{Existing: "nats://example:4222"},
		"sub-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.URL != "nats://example:4222" {
		t.Errorf("URL = %q, want nats://example:4222", got.URL)
	}
	if got.WeSpawnedIt {
		t.Errorf("WeSpawnedIt = true, want false for existing")
	}
}

// TestLiveSeshResolver_ExistingEnvExpand covers the env-var path.
// We pre-set TEST_NATS_URL and pass `$TEST_NATS_URL` literally to
// confirm the defensive re-expand works.
func TestLiveSeshResolver_ExistingEnvExpand(t *testing.T) {
	t.Setenv("TEST_NATS_URL", "nats://from-env:4222")
	r := NewLiveSeshResolver()
	got, err := r.Resolve(context.Background(),
		SeshSection{Existing: "$TEST_NATS_URL"},
		"sub-a")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.URL != "nats://from-env:4222" {
		t.Errorf("URL = %q, want nats://from-env:4222", got.URL)
	}
}

// TestLiveSeshResolver_ExistingEmptyVar surfaces a clear error when
// the env var resolves to nothing — silent fallback to "" would
// otherwise produce a confusing NATS connect failure later.
func TestLiveSeshResolver_ExistingEmptyVar(t *testing.T) {
	t.Setenv("DEFINITELY_UNSET_XYZ_TEST", "")
	r := NewLiveSeshResolver()
	_, err := r.Resolve(context.Background(),
		SeshSection{Existing: "$DEFINITELY_UNSET_XYZ_TEST"},
		"sub-a")
	if err == nil {
		t.Fatal("expected error on empty-expand; got nil")
	}
}

// TestLiveSeshResolver_SpawnPolls covers the spawn-path's polling
// loop without actually invoking `sesh up`: we pre-create the session
// JSON in the expected location and use BinPath=/bin/true so the
// resolver's command exits immediately (we don't need a long-lived
// hub for the URL-discovery contract test).
func TestLiveSeshResolver_SpawnPolls(t *testing.T) {
	tmp := t.TempDir()
	sessionsDir := filepath.Join(tmp, ".sesh", "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, _ := json.Marshal(map[string]any{
		"pid":      12345,
		"scope":    "session",
		"nats_url": "nats://spawned:1234",
	})
	if err := os.WriteFile(filepath.Join(sessionsDir, "test-sub.json"), body, 0o644); err != nil {
		t.Fatalf("write session: %v", err)
	}
	r := &LiveSeshResolver{
		BinPath:     "/usr/bin/true", // any short-lived no-op
		SessionsDir: sessionsDir,
		Timeout:     2 * time.Second,
	}
	got, err := r.Resolve(context.Background(),
		SeshSection{Spawn: &SeshSpawn{Session: "test-sub"}},
		"test-sub")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.URL != "nats://spawned:1234" {
		t.Errorf("URL = %q, want nats://spawned:1234", got.URL)
	}
	if !got.WeSpawnedIt {
		t.Errorf("WeSpawnedIt = false, want true for spawn")
	}
}

// TestLiveSeshResolver_SpawnTimeout exercises the deadline path: no
// session JSON appears within the timeout, the resolver surfaces a
// clear error citing the path it was polling.
func TestLiveSeshResolver_SpawnTimeout(t *testing.T) {
	tmp := t.TempDir()
	r := &LiveSeshResolver{
		BinPath:     "/bin/true",
		SessionsDir: filepath.Join(tmp, ".sesh", "sessions"),
		Timeout:     150 * time.Millisecond,
	}
	_, err := r.Resolve(context.Background(),
		SeshSection{Spawn: &SeshSpawn{Session: "never-arrives"}},
		"never-arrives")
	if err == nil {
		t.Fatal("expected timeout error; got nil")
	}
}

// TestLiveSeshResolver_NeitherSet asserts the defensive branch (the
// validator catches this earlier in practice, but Apply re-validates
// so this code path must still degrade gracefully).
func TestLiveSeshResolver_NeitherSet(t *testing.T) {
	r := NewLiveSeshResolver()
	_, err := r.Resolve(context.Background(), SeshSection{}, "x")
	if err == nil {
		t.Fatal("expected error when neither existing nor spawn set")
	}
}
