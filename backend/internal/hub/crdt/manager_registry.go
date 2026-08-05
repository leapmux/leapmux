package crdt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"
)

// ErrBlankUserID is returned by Get for an empty user id. The registry key IS
// the tenancy axis, so a blank key is not a cache miss to fill -- it is a
// caller that lost track of whose CRDT it is asking for.
var ErrBlankUserID = errors.New("crdt: blank user id")

// ManagerFactory builds a freshly-bootstrapped manager for a user.
type ManagerFactory func(ctx context.Context, owner userid.UserID) (*Manager, error)

// DefaultManagerIdleTTL is the default idle window after which the
// Registry's janitor evicts a per-user Manager. Picked to comfortably
// exceed the longest typical pause in human interaction (lunch break,
// short meeting) while still freeing memory on hubs that accumulate
// hundreds of seldom-revisited users over a uptime.
const DefaultManagerIdleTTL = 10 * time.Minute

// Registry hands out per-user managers, lazy-loading on first
// reference. The registry is single-instance: this hub deployment
// owns every user's manager, and there is no leader election.
//
// A background janitor evicts managers that have no subscribers, no
// presence-tracked clients, and no submit activity within idleTTL.
// Evicted managers re-bootstrap from disk on the next Get().
type Registry struct {
	mu       sync.Mutex
	managers map[string]*Manager
	factory  ManagerFactory
	logger   *slog.Logger
	idleTTL  time.Duration
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// RegistryOption tunes optional Registry behavior at construction.
type RegistryOption func(*Registry)

// WithManagerIdleTTL overrides the idle window. <= 0 disables the
// janitor entirely (managers live forever, matching the pre-eviction
// behavior). Tests use this to disable eviction or shorten the window
// for deterministic exercise.
func WithManagerIdleTTL(d time.Duration) RegistryOption {
	return func(r *Registry) { r.idleTTL = d }
}

// NewRegistry returns an empty registry. Without options, the
// DefaultManagerIdleTTL janitor runs in the background; pass
// WithManagerIdleTTL(0) to disable.
func NewRegistry(factory ManagerFactory, logger *slog.Logger, opts ...RegistryOption) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Registry{
		managers: map[string]*Manager{},
		factory:  factory,
		logger:   logger,
		idleTTL:  DefaultManagerIdleTTL,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
	for _, opt := range opts {
		opt(r)
	}
	if r.idleTTL > 0 {
		go r.runJanitor()
	} else {
		close(r.doneCh)
	}
	return r
}

// Get returns the manager for a user, lazy-creating it on first
// reference. Cancels are propagated; partial bootstrap leaves no
// manager in the map.
//
// Get performs NO authorization on userID -- it is a lazy cache keyed by a
// string, not an access checkpoint. A manager it returns can read and write the
// user's whole CRDT, so every caller MUST gate user/workspace access at the op or
// workspace layer downstream (Submit's per-op authCheck, the per-user
// ListAccessible/Materialized filter). Do not add a caller that reads or mutates
// manager state from an unvalidated userID.
//
// The one thing it DOES refuse is a blank userID (ErrBlankUserID). That is not
// authorization, it is the floor under the tenancy axis: this key IS the
// tenant. It is what the factory builds the Manager from, and a SubmitInput
// names no tenant of its own -- whichever manager a submit is handed to is the
// one it commits into. Lazy-creating on "" would therefore mint a phantom
// manager owning a CRDT that belongs to nobody, and every submit routed to it
// would land there unopposed. Refusing here is what lets the rest of the
// package assume a real tenant on the manager side (processSubmit keeps a
// blank-tenant refusal of its own for a Manager built directly with
// NewManager, which does not come through here). Every
// production caller already holds a minted id (the three UserInfo mint sites
// refuse a blank, and WorkspaceService.drainLifecycleOutbox guards its own);
// the only one reading an unvalidated id is the worker tab-sync reconciler,
// which takes it from a workspace_tab_owned row and now surfaces a warning
// instead of writing tombstones into a phantom tenant.
// Get keeps a STRING parameter deliberately. It is the mint boundary: two of
// its callers read the id from a database column rather than from an
// authenticated identity, so the refusal belongs here, once, rather than
// dispersed to each caller. Everything downstream of the mint is typed.
func (r *Registry) Get(ctx context.Context, userID string) (*Manager, error) {
	owner, ok := userid.New(userID)
	if !ok {
		return nil, ErrBlankUserID
	}
	r.mu.Lock()
	if m, ok := r.managers[userID]; ok {
		// Stamp activity WHILE holding r.mu, before handing the manager out. A
		// Get is genuine activity (the caller is about to read or submit against
		// it), and stamping here serializes against SweepIdle, which decides and
		// deletes under the same r.mu: either this stamp lands first and the next
		// sweep sees the manager as fresh, or the sweep evicts first and this
		// lookup misses and lazy-creates a live one. Without the stamp, a Get that
		// returned a manager an instant before an idle sweep evicted and Stop()ed
		// it would hand the caller a stopped manager whose Submit blocks until the
		// caller's ctx cancels (a spurious DeadlineExceeded).
		m.touchActivity()
		r.mu.Unlock()
		return m, nil
	}
	r.mu.Unlock()

	m, err := r.factory(ctx, owner)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, ok := r.managers[userID]; ok {
		// Lost the race; stop the duplicate.
		go m.Stop()
		return existing, nil
	}
	r.managers[userID] = m
	go func() {
		runCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := m.Start(runCtx); err != nil && err != context.Canceled {
			r.logger.Error("manager exited", "user_id", userID, "err", err)
		}
	}()
	// Wait until the goroutine has started servicing the select loop before
	// handing the manager out. SeedStateForTest's post-Start branch routes a
	// seed through taskCh, which only the manager goroutine drains -- without
	// this wait, Get would return with the goroutine spawned but not yet
	// servicing, and a caller that won the race to read started()==closed
	// (set inside Start) could route through taskCh before the receiver
	// existed. The wait also guarantees any caller observing the manager
	// post-Get sees started() closed, so its SeedStateForTest takes the
	// goroutine-routed branch instead of the pre-Start direct-write branch
	// (which would race the goroutine's bare m.state reads).
	select {
	case <-m.started():
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return m, nil
}

// Shutdown stops every running manager and waits for them.
func (r *Registry) Shutdown(timeout time.Duration) {
	// Stop the janitor first so it doesn't race the manager teardown.
	select {
	case <-r.stopCh:
		// already stopped
	default:
		close(r.stopCh)
	}
	<-r.doneCh

	r.mu.Lock()
	managers := make([]*Manager, 0, len(r.managers))
	for _, m := range r.managers {
		managers = append(managers, m)
	}
	r.managers = map[string]*Manager{}
	r.mu.Unlock()

	done := make(chan struct{})
	go func() {
		for _, m := range managers {
			m.Stop()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// runJanitor periodically sweeps idle managers out of the registry.
// The sweep interval is one-third of idleTTL (floored at 30s) so a
// manager that became idle right after one sweep is evicted within
// idleTTL + interval and not later.
func (r *Registry) runJanitor() {
	defer close(r.doneCh)
	interval := r.idleTTL / 3
	if interval < 30*time.Second {
		interval = 30 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-r.stopCh:
			return
		case <-t.C:
			r.SweepIdle()
		}
	}
}

// SweepIdle runs one eviction pass over the registry. The background
// janitor calls this on a timer, but admin tooling and tests may
// invoke it directly to evict idle managers on demand.
//
// The Get()-then-evict race is closed by construction: Get stamps activity
// on the manager it returns WHILE holding r.mu, and SweepIdle reads that
// activity (via idleSince) and deletes under the same r.mu. So the two
// serialize -- either Get stamps first and this pass sees the manager as
// fresh (skipped), or this pass evicts first and Get's lookup misses and
// lazy-creates a live manager. A caller can therefore never be handed a
// manager this pass is about to Stop().
func (r *Registry) SweepIdle() {
	now := time.Now()
	r.mu.Lock()
	type evictTarget struct {
		userID string
		m      *Manager
	}
	var toEvict []evictTarget
	for userID, m := range r.managers {
		last, attached := m.idleSince()
		if attached {
			continue
		}
		if last.IsZero() || now.Sub(last) < r.idleTTL {
			continue
		}
		toEvict = append(toEvict, evictTarget{userID: userID, m: m})
		delete(r.managers, userID)
	}
	r.mu.Unlock()

	for _, t := range toEvict {
		r.logger.Info("evicting idle manager", "user_id", t.userID)
		t.m.Stop()
	}
}
