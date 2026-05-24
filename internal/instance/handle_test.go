package instance_test

import (
	"context"
	"errors"
	"testing"

	"github.com/danmestas/orch/internal/instance"
)

// mockHandle is a tiny in-test implementation that verifies the
// instance.Handle contract is satisfiable by something that isn't
// a real engine. Useful as a placeholder when wiring future callers
// (orch attach, orch kill).
type mockHandle struct {
	id      string
	locator string
	killed  bool
	waitErr error
}

func (m *mockHandle) ID() string                       { return m.id }
func (m *mockHandle) Locator() string                  { return m.locator }
func (m *mockHandle) Wait(ctx context.Context) error   { return m.waitErr }
func (m *mockHandle) Kill() error                      { m.killed = true; return nil }

func TestHandleInterfaceCompliance(t *testing.T) {
	// Compile-time: mockHandle satisfies instance.Handle.
	var _ instance.Handle = (*mockHandle)(nil)

	h := &mockHandle{id: "w1", locator: "%42"}
	if h.ID() != "w1" || h.Locator() != "%42" {
		t.Fatalf("accessor mismatch")
	}

	if err := h.Kill(); err != nil {
		t.Errorf("Kill err: %v", err)
	}
	if !h.killed {
		t.Error("Kill did not mark mock as killed")
	}
}

func TestHandleWaitCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	sentinel := errors.New("cancelled")
	h := &mockHandle{waitErr: sentinel}
	if err := h.Wait(ctx); !errors.Is(err, sentinel) {
		t.Errorf("Wait err = %v, want sentinel", err)
	}
}
