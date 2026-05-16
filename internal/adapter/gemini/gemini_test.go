package gemini

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/shim"
)

// -----------------------------------------------------------------------------
// Test helpers: send-keys recorder + adapter factory.
// -----------------------------------------------------------------------------

// sendKeysRecorder is the test seam — captures every "send" call so
// assertions can inspect what would have been delivered to tmux.
type sendKeysRecorder struct {
	mu    sync.Mutex
	calls []sendKeysCall
}

type sendKeysCall struct{ Pane, Text string }

func (r *sendKeysRecorder) fn(pane, text string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sendKeysCall{Pane: pane, Text: text})
	return nil
}

func (r *sendKeysRecorder) snapshot() []sendKeysCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]sendKeysCall, len(r.calls))
	copy(out, r.calls)
	return out
}

func newTestAdapter(t *testing.T) (*Adapter, *sendKeysRecorder) {
	t.Helper()
	rec := &sendKeysRecorder{}
	a := New("%42")
	a.SendKeys = rec.fn
	a.StopMarkerDir = t.TempDir()
	a.NotifyMarkerDir = t.TempDir()
	return a, rec
}

// receiveChunk blocks until a chunk arrives or the timeout fires.
func receiveChunk(t *testing.T, ch <-chan shim.Chunk, timeout time.Duration) shim.Chunk {
	t.Helper()
	select {
	case c := <-ch:
		return c
	case <-time.After(timeout):
		t.Fatal("timeout waiting for chunk")
	}
	return shim.Chunk{}
}

// atomicWrite simulates the hook scripts' tmpfile-then-rename pattern,
// which surfaces as a single CREATE event on the destination path.
func atomicWrite(t *testing.T, path, content string) {
	t.Helper()
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

// -----------------------------------------------------------------------------
// Tests.
// -----------------------------------------------------------------------------

// TestAdapter_OnPrompt_SendsViaRecorder verifies the send-keys path.
func TestAdapter_OnPrompt_SendsViaRecorder(t *testing.T) {
	a, rec := newTestAdapter(t)
	defer a.Close()

	shimCtx, shimCancel := context.WithCancel(context.Background())
	defer shimCancel()
	if err := a.Start(shimCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	promptCtx, promptCancel := context.WithCancel(context.Background())
	defer promptCancel()
	if err := a.OnPrompt(promptCtx, "hello gemini"); err != nil {
		t.Fatalf("OnPrompt: %v", err)
	}
	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("expected 1 send-keys call, got %d", len(calls))
	}
	if calls[0].Pane != "%42" || calls[0].Text != "hello gemini" {
		t.Errorf("call mismatch: %+v", calls[0])
	}
}

// TestAdapter_AfterAgent_EmitsTerminator asserts that the stop marker
// (written by the AfterAgent hook) produces a Terminator chunk.
// IMPORTANT: gemini-cli uses "AfterAgent" NOT "Stop" — this test is the
// canonical regression guard for that quirk.
func TestAdapter_AfterAgent_EmitsTerminator(t *testing.T) {
	a, _ := newTestAdapter(t)
	defer a.Close()

	shimCtx, shimCancel := context.WithCancel(context.Background())
	defer shimCancel()
	if err := a.Start(shimCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Settle so fsnotify registers the directory watch.
	time.Sleep(50 * time.Millisecond)

	// Simulate the AfterAgent hook writing the stop marker.
	marker := filepath.Join(a.stopDir(), "%42.event")
	atomicWrite(t, marker, `{"event":"AfterAgent","harness":"gemini"}`)

	c := receiveChunk(t, a.Events(), 1*time.Second)
	if !c.Terminator {
		t.Errorf("AfterAgent: expected Terminator chunk, got %+v", c)
	}
}

// TestAdapter_Notification_EmitsQueryChunk verifies that the native
// Notification marker produces a Query chunk. Unlike codex/pi, gemini
// has a first-class Notification event, so no synthetic detection is
// needed — the hook writes the marker directly and the adapter emits
// the query chunk unconditionally.
func TestAdapter_Notification_EmitsQueryChunk(t *testing.T) {
	a, _ := newTestAdapter(t)
	defer a.Close()

	shimCtx, shimCancel := context.WithCancel(context.Background())
	defer shimCancel()
	if err := a.Start(shimCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Simulate the Notification hook writing the notify marker.
	marker := filepath.Join(a.notifyDir(), "%42.notify")
	atomicWrite(t, marker, "Gemini is waiting for your input")

	c := receiveChunk(t, a.Events(), 1*time.Second)
	if c.Type != shim.ChunkQuery {
		t.Fatalf("Notification: expected ChunkQuery, got type=%v", c.Type)
	}
	q, ok := c.Data.(shim.QueryData)
	if !ok {
		t.Fatalf("Notification: expected QueryData payload, got %T", c.Data)
	}
	if q.Prompt != "Gemini is waiting for your input" {
		t.Errorf("query prompt: got %q", q.Prompt)
	}
	if q.ID == "" {
		t.Error("query id should be non-empty")
	}
}

// TestAdapter_WatchersSurvivePromptCtxCancel verifies that cancelling
// the per-prompt context does NOT tear down the marker watcher — the
// watcher is bound to the shim's lifetime context, not the prompt's.
// This catches the regression where startWatcher was called with
// OnPrompt's ctx, dismantling the adapter after the first turn.
func TestAdapter_WatchersSurvivePromptCtxCancel(t *testing.T) {
	a, _ := newTestAdapter(t)
	defer a.Close()

	shimCtx, shimCancel := context.WithCancel(context.Background())
	defer shimCancel()
	if err := a.Start(shimCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	promptCtx, promptCancel := context.WithCancel(context.Background())
	if err := a.OnPrompt(promptCtx, "first"); err != nil {
		t.Fatalf("OnPrompt: %v", err)
	}
	promptCancel() // simulate end-of-turn cleanup
	// Allow any (incorrect) goroutine teardown to complete.
	time.Sleep(60 * time.Millisecond)

	// The marker watcher must still be live after the prompt ctx cancel.
	marker := filepath.Join(a.stopDir(), "%42.event")
	atomicWrite(t, marker, `{"event":"AfterAgent"}`)

	c := receiveChunk(t, a.Events(), 1*time.Second)
	if !c.Terminator {
		t.Errorf("watcher torn down after prompt ctx cancel: got %+v", c)
	}
}

// TestAdapter_Close_Idempotent verifies that calling Close twice does
// not panic and that the events channel is closed afterward.
func TestAdapter_Close_Idempotent(t *testing.T) {
	a, _ := newTestAdapter(t)
	if err := a.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := a.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	// Events channel MUST be closed so the shim's eventPump exits.
	select {
	case _, ok := <-a.Events():
		if ok {
			t.Error("expected closed events channel after Close")
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Events() did not close after Close()")
	}
}

// TestAdapter_EmptyNotifyMarker_Skipped verifies that an empty notify
// marker file does not produce a chunk (defensive: hook write failure).
func TestAdapter_EmptyNotifyMarker_Skipped(t *testing.T) {
	a, _ := newTestAdapter(t)
	defer a.Close()

	shimCtx, shimCancel := context.WithCancel(context.Background())
	defer shimCancel()
	if err := a.Start(shimCtx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	marker := filepath.Join(a.notifyDir(), "%42.notify")
	atomicWrite(t, marker, "")

	// No chunk should arrive within a short window.
	select {
	case c := <-a.Events():
		t.Errorf("expected no chunk for empty notify marker, got %+v", c)
	case <-time.After(200 * time.Millisecond):
		// Correct: nothing emitted.
	}
}
