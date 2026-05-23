package registry

import (
	"context"
	"maps"
	"sync"
	"time"
)

// LiveOptions configures a Live registry. Zero values pick defaults.
type LiveOptions struct {
	// RefreshInterval is how often Live re-runs Snapshot in the
	// background. Default 5s. Set to 0 to disable polling (Live then
	// only refreshes when triggered manually).
	RefreshInterval time.Duration

	// HeartbeatWindow is the alive threshold. Default DefaultHeartbeatWindow.
	HeartbeatWindow time.Duration

	// WatchBuffer is the per-subscriber event channel size. Slow
	// subscribers drop oldest. Default 256 (per proposal §"Watch semantics").
	WatchBuffer int
}

const (
	defaultRefreshInterval = 5 * time.Second
	defaultWatchBuffer     = 256
)

// Live is a continuously-refreshing Registry. Construct with NewLive,
// call Start once, Close when done.
type Live struct {
	opts    LiveOptions
	readers Readers

	mu         sync.RWMutex
	workers    []Worker
	byPane     map[string]Worker
	byName     map[string]Worker
	byInstance map[string]Worker
	lastErrs   Errors

	subsMu sync.Mutex
	subs   []chan Event

	closeOnce sync.Once
	closeCh   chan struct{}
}

// NewLive constructs a Live registry. Start must be called separately so
// the caller controls the lifetime context.
func NewLive(r Readers, opts LiveOptions) *Live {
	if opts.RefreshInterval == 0 {
		opts.RefreshInterval = defaultRefreshInterval
	}
	if opts.HeartbeatWindow <= 0 {
		opts.HeartbeatWindow = DefaultHeartbeatWindow
	}
	if opts.WatchBuffer <= 0 {
		opts.WatchBuffer = defaultWatchBuffer
	}
	return &Live{
		opts:       opts,
		readers:    r,
		byPane:     map[string]Worker{},
		byName:     map[string]Worker{},
		byInstance: map[string]Worker{},
		closeCh:    make(chan struct{}),
	}
}

// Start begins background refreshes. Returns after the first snapshot so
// Snapshot()/Lookup() are usable immediately.
func (l *Live) Start(ctx context.Context) error {
	if err := l.refresh(ctx); err != nil {
		return err
	}
	if l.opts.RefreshInterval > 0 {
		go l.loop(ctx)
	}
	return nil
}

func (l *Live) loop(ctx context.Context) {
	t := time.NewTicker(l.opts.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-l.closeCh:
			return
		case <-t.C:
			// Errors during refresh are exposed via LastErrors but don't
			// stop the loop — a transient NATS hiccup shouldn't tear
			// down the registry.
			_ = l.refresh(ctx)
		}
	}
}

// refresh runs one snapshot pass and diffs against current state.
func (l *Live) refresh(ctx context.Context) error {
	workers, errs := Snapshot(ctx, l.readers, l.opts.HeartbeatWindow)

	l.mu.Lock()
	old := l.byPane
	newByPane := make(map[string]Worker, len(workers))
	newByName := make(map[string]Worker, len(workers))
	newByInstance := make(map[string]Worker, len(workers))
	for _, w := range workers {
		newByPane[w.PaneID] = w
		if w.Name != "" {
			newByName[w.Name] = w
		}
		// Index by stable slug too so Lookup resolves --instance-id
		// values that don't match the display Name (e.g. when an alias
		// overrides). Proposal 0009 / issue #181.
		if w.InstanceID != "" {
			newByInstance[w.InstanceID] = w
		}
	}
	l.workers = workers
	l.byPane = newByPane
	l.byName = newByName
	l.byInstance = newByInstance
	l.lastErrs = errs
	l.mu.Unlock()

	// Diff and emit events. Done outside the lock so subscribers blocking
	// on their channel don't stall the refresh path.
	now := time.Now()
	for pane, w := range newByPane {
		if prev, ok := old[pane]; ok {
			if !workersEqual(prev, w) {
				l.emit(Event{Type: Updated, Worker: w, Timestamp: now})
			}
		} else {
			l.emit(Event{Type: Joined, Worker: w, Timestamp: now})
		}
	}
	for pane, w := range old {
		if _, ok := newByPane[pane]; !ok {
			l.emit(Event{Type: Departed, Worker: w, Timestamp: now})
		}
	}

	if errs.HasFatal() {
		return errs["agents"]
	}
	return nil
}

// Snapshot returns the most recent worker list. Cheap — returns a copy of
// a cached slice; no source reads.
func (l *Live) Snapshot() []Worker {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Worker, len(l.workers))
	copy(out, l.workers)
	return out
}

// Lookup resolves by name, instance-id slug, or pane id. Pane ids are
// recognised by the leading "%".
//
// Resolution order for non-pane keys: display Name first (operator's
// chosen handle: alias > slug > session > pct), then InstanceID (the
// stable slug from --instance-id) as a fallback so callers can pass
// either the alias-overridden name or the underlying slug and reach the
// same worker.
func (l *Live) Lookup(nameOrPane string) (Worker, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if len(nameOrPane) > 0 && nameOrPane[0] == '%' {
		w, ok := l.byPane[nameOrPane]
		return w, ok
	}
	if w, ok := l.byName[nameOrPane]; ok {
		return w, true
	}
	w, ok := l.byInstance[nameOrPane]
	return w, ok
}

// Watch returns a buffered channel of events. Slow consumers drop oldest.
// Channel closes when ctx is cancelled or the registry is closed.
func (l *Live) Watch(ctx context.Context) <-chan Event {
	ch := make(chan Event, l.opts.WatchBuffer)
	l.subsMu.Lock()
	l.subs = append(l.subs, ch)
	l.subsMu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-l.closeCh:
		}
		l.subsMu.Lock()
		for i, s := range l.subs {
			if s == ch {
				l.subs = append(l.subs[:i], l.subs[i+1:]...)
				break
			}
		}
		l.subsMu.Unlock()
		close(ch)
	}()
	return ch
}

// LastErrors returns the most recent per-source error map. Empty when
// every source succeeded.
func (l *Live) LastErrors() Errors {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make(Errors, len(l.lastErrs))
	maps.Copy(out, l.lastErrs)
	return out
}

// Close stops the refresh loop and closes all subscriber channels.
// Idempotent.
func (l *Live) Close() error {
	l.closeOnce.Do(func() { close(l.closeCh) })
	return nil
}

// emit fans out an event to all subscribers, dropping oldest on full
// channels (bounded buffer policy per proposal §"Watch semantics").
func (l *Live) emit(ev Event) {
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	for _, ch := range l.subs {
		select {
		case ch <- ev:
		default:
			// Drop oldest to make room — non-blocking even when a
			// subscriber stalls. Stderr warnings would be nice here but
			// adding logger plumbing for a rare condition isn't worth it.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- ev:
			default:
			}
		}
	}
}

// workersEqual compares the fields that should drive an Updated event.
// LastHB / Alive churn on every refresh and would generate noise, so we
// only emit Updated on Alive-state transitions (alive ↔ departed) or on
// changes to the identity / role / display fields.
func workersEqual(a, b Worker) bool {
	if a.Alive != b.Alive {
		return false
	}
	if a.Role != b.Role || a.Name != b.Name || a.Outfit != b.Outfit {
		return false
	}
	if a.Agent != b.Agent || a.Session != b.Session || a.CWD != b.CWD {
		return false
	}
	if a.Subjects != b.Subjects {
		return false
	}
	return true
}
