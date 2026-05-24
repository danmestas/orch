// Package mock provides a test double for instance.Handle. Live in a
// subpackage (rather than a _test.go file) so multiple test packages
// across internal/persistence, internal/layout, and downstream
// consumers can share a single mock.
package mock

import "sync"

// Handle is a configurable test double for instance.Handle.
//
//	h := &mock.Handle{IDValue: "alpha", LocatorValue: "%99"}
//	defer h.WaitDone() // unblock Wait() in teardown
type Handle struct {
	IDValue      string
	LocatorValue string

	// WaitErr is what Wait() returns once unblocked.
	WaitErr error

	// KillErr is what Kill() returns. Defaults to nil.
	KillErr error

	mu        sync.Mutex
	killed    bool
	waitCh    chan struct{}
	waitOnce  sync.Once
	waitCalls int
	killCalls int
}

// ID implements instance.Handle.
func (h *Handle) ID() string { return h.IDValue }

// Locator implements instance.Handle.
func (h *Handle) Locator() string { return h.LocatorValue }

// Wait implements instance.Handle. Blocks until WaitDone is called.
// Returns WaitErr.
func (h *Handle) Wait() error {
	h.ensureChan()
	h.mu.Lock()
	h.waitCalls++
	ch := h.waitCh
	h.mu.Unlock()
	<-ch
	return h.WaitErr
}

// Kill implements instance.Handle. Idempotent — second call no-ops.
func (h *Handle) Kill() error {
	h.mu.Lock()
	h.killCalls++
	if h.killed {
		h.mu.Unlock()
		return nil
	}
	h.killed = true
	h.mu.Unlock()
	// Closing waitCh as a side effect of Kill mirrors the real
	// tmuxHandle behavior: kill-pane causes Wait to observe pane
	// death and return.
	h.WaitDone()
	return h.KillErr
}

// WaitDone unblocks any in-flight Wait calls. Safe to call multiple
// times; second call is a no-op.
func (h *Handle) WaitDone() {
	h.ensureChan()
	h.waitOnce.Do(func() { close(h.waitCh) })
}

// WaitCalls reports how many goroutines have entered Wait. Useful in
// tests asserting watchdog wiring.
func (h *Handle) WaitCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.waitCalls
}

// KillCalls reports how many times Kill has been invoked. Useful for
// idempotency assertions.
func (h *Handle) KillCalls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.killCalls
}

func (h *Handle) ensureChan() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.waitCh == nil {
		h.waitCh = make(chan struct{})
	}
}
