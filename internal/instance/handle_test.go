package instance_test

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/danmestas/orch/internal/instance"
	"github.com/danmestas/orch/internal/instance/mock"
)

// TestMockHandleSatisfiesInterface is a compile-time assertion that
// the mock satisfies instance.Handle. If this file compiles, the
// interface contract is intact.
func TestMockHandleSatisfiesInterface(t *testing.T) {
	var _ instance.Handle = &mock.Handle{}
}

func TestMockHandleIDLocator(t *testing.T) {
	h := &mock.Handle{IDValue: "alpha", LocatorValue: "%64"}
	if got := h.ID(); got != "alpha" {
		t.Errorf("ID() = %q, want %q", got, "alpha")
	}
	if got := h.Locator(); got != "%64" {
		t.Errorf("Locator() = %q, want %q", got, "%64")
	}
}

func TestMockHandleWaitBlocks(t *testing.T) {
	h := &mock.Handle{}

	done := make(chan struct{})
	go func() {
		_ = h.Wait()
		close(done)
	}()

	// Give the goroutine a chance to enter Wait. 50ms is generous;
	// the test will still pass if scheduling is slow because the
	// select below treats "not done yet" as the success path.
	select {
	case <-done:
		t.Fatal("Wait returned before WaitDone was called")
	case <-time.After(50 * time.Millisecond):
	}

	h.WaitDone()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return within 1s of WaitDone")
	}
}

func TestMockHandleWaitErr(t *testing.T) {
	sentinel := errors.New("worker crashed")
	h := &mock.Handle{WaitErr: sentinel}

	got := make(chan error, 1)
	go func() { got <- h.Wait() }()

	h.WaitDone()
	select {
	case err := <-got:
		if !errors.Is(err, sentinel) {
			t.Errorf("Wait() = %v, want %v", err, sentinel)
		}
	case <-time.After(time.Second):
		t.Fatal("Wait did not return within 1s")
	}
}

func TestMockHandleKillIdempotent(t *testing.T) {
	h := &mock.Handle{}
	if err := h.Kill(); err != nil {
		t.Fatalf("first Kill: %v", err)
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("second Kill: %v", err)
	}
	if got := h.KillCalls(); got != 2 {
		t.Errorf("KillCalls = %d, want 2", got)
	}
}

func TestMockHandleKillUnblocksWait(t *testing.T) {
	h := &mock.Handle{}

	done := make(chan struct{})
	go func() {
		_ = h.Wait()
		close(done)
	}()

	// Settle: let Wait start before we Kill.
	time.Sleep(20 * time.Millisecond)

	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Wait did not return after Kill")
	}
}

func TestMockHandleConcurrentWaiters(t *testing.T) {
	h := &mock.Handle{}
	const N = 8
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func() {
			defer wg.Done()
			_ = h.Wait()
		}()
	}
	// Settle so all waiters are blocked.
	time.Sleep(50 * time.Millisecond)
	h.WaitDone()

	doneCh := make(chan struct{})
	go func() { wg.Wait(); close(doneCh) }()

	select {
	case <-doneCh:
	case <-time.After(time.Second):
		t.Fatal("not all waiters returned")
	}

	if got := h.WaitCalls(); got != N {
		t.Errorf("WaitCalls = %d, want %d", got, N)
	}
}
