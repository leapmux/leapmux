package crdt

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/util/drain"
)

// ErrBlankUserID is returned by Get for an empty user id. The registry key IS
// the tenancy axis, so a blank key is not a cache miss to fill -- it is a
// caller that lost track of whose CRDT it is asking for.
var ErrBlankUserID = errors.New("crdt: blank user id")

// ErrRegistryClosed is returned when a Get arrives after Shutdown began.
var ErrRegistryClosed = errors.New("crdt registry is closed")

// ManagerFactory builds a freshly-bootstrapped manager for a user.
type ManagerFactory func(ctx context.Context, owner userid.UserID) (*Manager, error)

// DefaultManagerIdleTTL is the default idle window after which the
// Registry's janitor evicts a per-user Manager. Picked to comfortably
// exceed the longest typical pause in human interaction (lunch break,
// short meeting) while still freeing memory on hubs that accumulate
// hundreds of seldom-revisited users over a uptime.
const DefaultManagerIdleTTL = 10 * time.Minute

const defaultLifecycleDrainTimeout = 15 * time.Second

// LifecycleDrain tracks one in-flight coalesced lifecycle-outbox drain for an
// owner. Multiple callers requesting a drain for the same user share one flight.
type LifecycleDrain struct {
	done chan struct{}
	err  error
}

// Done returns a channel that closes when the drain flight finishes.
func (d *LifecycleDrain) Done() <-chan struct{} { return d.done }

// Err returns the drain error after Done closes.
func (d *LifecycleDrain) Err() error { return d.err }

// wakeEntry tracks one per-user broadcast wake channel and its active waiter count.
type wakeEntry struct {
	ch      chan struct{}
	waiters int
}

// wakeChannels manages per-user one-shot wake channels with waiter reference
// counting and a shutdown latch so disconnecting waiters clean up map entries
// and closed registries never re-insert dead channels.
type wakeChannels struct {
	mu      sync.Mutex
	entries map[string]*wakeEntry
	closed  bool
}

func (w *wakeChannels) subscribe(userID string) (<-chan struct{}, func(), bool) {
	if userID == "" {
		return nil, func() {}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil, func() {}, false
	}
	if w.entries == nil {
		w.entries = make(map[string]*wakeEntry)
	}
	entry := w.entries[userID]
	if entry == nil {
		entry = &wakeEntry{ch: make(chan struct{})}
		w.entries[userID] = entry
	}
	entry.waiters++
	var once sync.Once
	release := func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if w.entries[userID] != entry {
				return
			}
			entry.waiters--
			if entry.waiters == 0 {
				delete(w.entries, userID)
			}
		})
	}
	return entry.ch, release, true
}

func (w *wakeChannels) signal(userID string) {
	if userID == "" {
		return
	}
	w.mu.Lock()
	entry := w.entries[userID]
	if entry != nil {
		delete(w.entries, userID)
		close(entry.ch)
	}
	w.mu.Unlock()
}

func (w *wakeChannels) closeAll() {
	w.mu.Lock()
	w.closed = true
	for userID, entry := range w.entries {
		close(entry.ch)
		delete(w.entries, userID)
	}
	w.mu.Unlock()
}

// Registry hands out per-user managers, lazy-loading on first
// reference. The registry is single-instance: this hub deployment
// owns every user's manager, and there is no leader election.
//
// A background janitor evicts managers that have no subscribers, no
// presence-tracked clients, and no submit activity within idleTTL.
// Evicted managers re-bootstrap from disk on the next Get().
type Registry struct {
	mu           sync.Mutex
	managers     map[string]*Manager
	layoutWakes  wakeChannels
	workerWakes  wakeChannels
	drains       map[string]*LifecycleDrain
	factory      ManagerFactory
	outbox       LifecycleOutboxReader
	drainTimeout time.Duration
	drainCtx     context.Context
	cancelDrains context.CancelFunc
	drainsWG     sync.WaitGroup
	logger       *slog.Logger
	idleTTL      time.Duration
	stopCh       chan struct{}
	doneCh       chan struct{}
	// closed refuses every later Get and StartLifecycleDrain. Shutdown sets it
	// under r.mu: a manager or drain started after the sweep snapshotted would
	// run past the store close with nothing stopping it.
	closed bool
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

// WithLifecycleOutbox attaches the lifecycle outbox reader used by
// StartLifecycleDrain and DrainLifecycle.
func WithLifecycleOutbox(reader LifecycleOutboxReader) RegistryOption {
	return func(r *Registry) { r.outbox = reader }
}

// NewRegistry returns an empty registry. Without options, the
// DefaultManagerIdleTTL janitor runs in the background; pass
// WithManagerIdleTTL(0) to disable.
func NewRegistry(factory ManagerFactory, logger *slog.Logger, opts ...RegistryOption) *Registry {
	if logger == nil {
		logger = slog.Default()
	}
	drainCtx, cancelDrains := context.WithCancel(context.Background())
	r := &Registry{
		managers:     map[string]*Manager{},
		drains:       map[string]*LifecycleDrain{},
		factory:      factory,
		drainTimeout: defaultLifecycleDrainTimeout,
		drainCtx:     drainCtx,
		cancelDrains: cancelDrains,
		logger:       logger,
		idleTTL:      DefaultManagerIdleTTL,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
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
	if r.closed {
		r.mu.Unlock()
		return nil, ErrRegistryClosed
	}
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
	if r.closed {
		// Shutdown won the race to the second lock: discard the manager this
		// Get built, so nothing stores a manager the sweep will never stop.
		go m.Discard()
		return nil, ErrRegistryClosed
	}
	if existing, ok := r.managers[userID]; ok {
		// Lost the race; discard the duplicate.
		go m.Discard()
		return existing, nil
	}
	m.setOnLayoutEvents(func() { r.SignalLayoutEvents(userID) })
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

// Loaded returns the manager already loaded for userID without bootstrapping
// one or stamping idle activity.
func (r *Registry) Loaded(userID string) (*Manager, bool) {
	if r == nil {
		return nil, false
	}
	return r.loaded(userID)
}

// loaded returns the manager already loaded for userID. It never bootstraps
// one and stamps no activity: a broadcast to a user's subscribers is not that
// user's activity.
func (r *Registry) loaded(userID string) (*Manager, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m, ok := r.managers[userID]
	return m, ok
}

// BroadcastWorkersChanged wakes any WorkerInventoryWake waiters for userID and
// tells every live /ws/userevents subscriber of userID whose credential holds
// worker:read that the user's worker list changed (see
// Manager.BroadcastWorkersChanged).
//
// It uses only a manager that is already loaded and never bootstraps one: the
// registry evicts only a manager with no subscriber (see SweepIdle), so a
// loaded-only lookup reaches every live subscriber, while WorkerInventoryWake
// waiters still wake even when no manager is loaded. A caller sends it after
// the change committed, never inside a store transaction.
func (r *Registry) BroadcastWorkersChanged(userID string) {
	if r == nil || userID == "" {
		return
	}
	r.mu.Lock()
	m := r.managers[userID]
	r.mu.Unlock()
	r.workerWakes.signal(userID)
	if m != nil {
		m.BroadcastWorkersChanged()
	}
}

// WorkerInventoryWake returns a channel that closes when
// BroadcastWorkersChanged runs for userID, along with an idempotent release
// function that cleans up the registration when the caller stops waiting.
// Capture the channel before reading worker inventory so a concurrent change
// cannot be missed, even before a Manager is loaded or across idle eviction.
// Returns (nil, noop) when r is nil, userID is blank, or Shutdown has begun.
func (r *Registry) WorkerInventoryWake(userID string) (<-chan struct{}, func()) {
	if r == nil {
		return nil, func() {}
	}
	ch, release, _ := r.workerWakes.subscribe(userID)
	return ch, release
}

// LayoutEventWake returns a channel that closes when layout facts or pending
// lifecycle mutations are signaled for userID, along with an idempotent release
// function that cleans up the registration when the caller stops waiting.
// Capture the channel before reading the layout log so a concurrent commit
// cannot be missed, even before a Manager is loaded or across idle eviction.
// Returns (nil, noop) when r is nil, userID is blank, or Shutdown has begun.
func (r *Registry) LayoutEventWake(userID string) (<-chan struct{}, func()) {
	if r == nil {
		return nil, func() {}
	}
	ch, release, _ := r.layoutWakes.subscribe(userID)
	return ch, release
}

// SignalLayoutEvents wakes every waiter watching userID's layout history.
func (r *Registry) SignalLayoutEvents(userID string) {
	if r == nil || userID == "" {
		return
	}
	r.layoutWakes.signal(userID)
}

// StartLifecycleDrain starts or joins the single in-flight multi-page
// lifecycle-outbox drain for userID. Shutdown cancels and joins all active
// drain goroutines before stopping managers.
func (r *Registry) StartLifecycleDrain(userID string) *LifecycleDrain {
	done := func(err error) *LifecycleDrain {
		d := &LifecycleDrain{done: make(chan struct{}), err: err}
		close(d.done)
		return d
	}
	if r == nil || r.outbox == nil || userID == "" {
		return done(nil)
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return done(ErrRegistryClosed)
	}
	if existing := r.drains[userID]; existing != nil {
		r.mu.Unlock()
		return existing
	}
	d := &LifecycleDrain{done: make(chan struct{})}
	r.drains[userID] = d
	r.drainsWG.Add(1)
	r.mu.Unlock()

	go func() {
		defer r.drainsWG.Done()
		work, cancel := context.WithTimeout(r.drainCtx, r.drainTimeout)
		defer cancel()
		d.err = r.runLifecycleDrain(work, userID)
		r.mu.Lock()
		delete(r.drains, userID)
		close(d.done)
		r.mu.Unlock()
	}()
	return d
}

// DrainLifecycle drains pending lifecycle-outbox rows for userID, coalescing
// concurrent callers onto one flight.
func (r *Registry) DrainLifecycle(ctx context.Context, userID string) error {
	if r == nil || r.outbox == nil || userID == "" {
		return nil
	}
	d := r.StartLifecycleDrain(userID)
	select {
	case <-d.Done():
		return d.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Registry) runLifecycleDrain(ctx context.Context, userID string) error {
	mgr, err := r.Get(ctx, userID)
	if err != nil {
		return err
	}
	var lastFirstID int64
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		firstID, listed, err := mgr.submitLifecyclePage(ctx, r.outbox)
		if err != nil {
			return err
		}
		if listed == 0 {
			return nil
		}
		if lastFirstID != 0 && firstID == lastFirstID {
			return nil
		}
		lastFirstID = firstID
	}
}

// Shutdown cancels active lifecycle drains, stops the janitor, and stops every
// manager under one deadline, waiting for each to finish.
//
// Active lifecycle drains are cancelled and joined before managers stop so no
// drain submits into a stopped manager or reads the store after Shutdown
// returns. The manager stops start before waiting on the janitor, one
// goroutine per manager. A manager still running at the deadline is counted in
// a Warn and abandoned to the process exit.
func (r *Registry) Shutdown(timeout time.Duration) {
	// Cancel active lifecycle drains first so none starts new work against
	// managers or the store.
	if r.cancelDrains != nil {
		r.cancelDrains()
	}

	// Stop the janitor next so it doesn't race the manager teardown.
	select {
	case <-r.stopCh:
		// already stopped
	default:
		close(r.stopCh)
	}

	r.mu.Lock()
	r.closed = true
	managers := make([]*Manager, 0, len(r.managers))
	for _, m := range r.managers {
		managers = append(managers, m)
	}
	r.managers = map[string]*Manager{}
	r.layoutWakes.closeAll()
	r.workerWakes.closeAll()
	r.mu.Unlock()

	deadline := time.Now().Add(timeout)

	drainsDone := make(chan struct{})
	go func() {
		r.drainsWG.Wait()
		close(drainsDone)
	}()
	drain.WaitBounded(drainsDone, time.Until(deadline),
		"crdt registry: lifecycle drains did not finish in the shutdown budget; abandoning them")

	var stops drain.Counter
	var running atomic.Int64
	running.Store(int64(len(managers)))
	for _, m := range managers {
		stops.Add()
		go func(m *Manager) {
			defer stops.Done()
			m.Stop()
			running.Add(-1)
		}(m)
	}

	drain.WaitBounded(r.doneCh, time.Until(deadline),
		"crdt registry: janitor did not stop in the shutdown budget; abandoning the sweep")
	if !stops.Wait(time.Until(deadline), "") {
		r.logger.Warn("crdt registry: managers still running at the shutdown deadline",
			"running", running.Load())
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
