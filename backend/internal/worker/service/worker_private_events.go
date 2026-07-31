package service

import (
	"context"
	"sync"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// PrivateEventsBus is the worker-local pub/sub for E2EE-only tab events
// (TabRenamed, FileTabPathRegistered, FileTabPathRevoked). It mirrors the
// hub-side WorkspaceEventBus shape but lives on the worker so titles and
// paths never leave E2EE.
//
// Subscribers are keyed by OWNER, not by workspace. A worker stores no
// workspace id, so a per-workspace bus had nothing to key on -- and the
// per-workspace shape was actively wrong for the thing it fed: a client had
// to open one stream per (workspace, worker) pair derived from the tabs it
// could already see, which left a workspace with no tabs yet unsubscribed and
// its first tab's events undelivered.
//
// The owner axis is not tenancy bookkeeping the worker gate already covers --
// it is the same id-uniqueness requirement worker_file_tabs is keyed by (a
// FILE tab id is minted client-side and unique only within a user). Keying the
// bus the same way keeps "which rows may this subscriber see?" answered
// identically at the bus and at the store.
//
// Shutdown shape, and why it looks like this.
//
// `closed` is a plain bool under `mu`, NOT an atomic. An atomic read outside
// the lock is a check on state the lock protects, and the gap between the two
// is long enough to lose a whole Stop: a subscriber that read "open" and then
// blocked on the mutex would wake up after Stop had already emptied the map.
//
// `done` is what retires live subscribers, and no sender ever closes a
// subscriber channel. Closing them from Stop is the obvious implementation and
// it is unsafe: publish would race the close and panic with "send on closed
// channel" -- fatally, since one publish path (the orphan reconciler's
// RevokeRow, reached from a bare goroutine) has no recover above it. Closing a
// single `done` instead makes that panic unreachable by construction rather
// than merely excluded by a mutex.
type PrivateEventsBus struct {
	mu          sync.RWMutex
	subscribers map[string]map[string]chan *leapmuxv1.WorkerPrivateEvent
	closed      bool
	done        chan struct{}
	bufSize     int
}

// NewPrivateEventsBus returns a ready-to-use bus.
func NewPrivateEventsBus() *PrivateEventsBus {
	return &PrivateEventsBus{
		subscribers: make(map[string]map[string]chan *leapmuxv1.WorkerPrivateEvent),
		done:        make(chan struct{}),
		bufSize:     32,
	}
}

// Stop retires every live subscriber and stops accepting new ones. Idempotent.
func (b *PrivateEventsBus) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	b.closed = true
	// Wakes every SnapshotAndSubscribe loop, which then runs its own deferred
	// map cleanup. The map is deliberately NOT nilled: a subscriber released by
	// `done` still has to delete its own entry, and a nil map would make that
	// deferred write a panic.
	close(b.done)
}

// publish broadcasts evt to every subscriber owned by owner. An unminted
// owner reaches nobody: SnapshotAndSubscribe refuses to register one, so no
// key can exist for it.
func (b *PrivateEventsBus) publish(owner userid.UserID, evt *leapmuxv1.WorkerPrivateEvent) {
	if owner.IsZero() {
		return
	}
	// The sends happen UNDER the read lock rather than against a copied slice.
	// Every send is select+default, so it never waits on a consumer and the
	// hold is bounded by the subscriber count -- and holding it means a
	// subscriber cannot be retired between the copy and the send.
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subscribers[owner.String()] {
		select {
		case ch <- evt:
		default:
			// Drop on slow consumer. The next snapshot/full state will
			// allow the client to re-derive the missed change.
		}
	}
}

// PublishTabRenamed broadcasts a TabRenamed private event to owner's
// subscribers. originClientID is included so receivers can suppress echoes of
// their own renames.
func (b *PrivateEventsBus) PublishTabRenamed(owner userid.UserID, tabID string, tabType leapmuxv1.TabType, title, originClientID string) {
	b.publish(owner, &leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_TabRenamed{
			TabRenamed: &leapmuxv1.TabRenamed{
				TabId:          tabID,
				TabType:        tabType,
				Title:          title,
				OriginClientId: originClientID,
			},
		},
	})
}

// PublishFileTabPathRegistered broadcasts a FileTabPathRegistered event to
// owner's subscribers. The path is plaintext on the wire — the bus only
// carries E2EE-bound traffic, so callers must ensure the surrounding
// transport is E2EE.
//
// workingDir is the RESOLVED value the store persisted, not the one the client
// sent: a peer that groups the tab by what it hears here has to see the same
// dir the worker will answer branch-context questions with.
func (b *PrivateEventsBus) PublishFileTabPathRegistered(owner userid.UserID, tabID, filePath, workingDir string) {
	b.publish(owner, &leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_FileTabPathRegistered{
			FileTabPathRegistered: &leapmuxv1.FileTabPathRegistered{
				TabId:      tabID,
				FilePath:   filePath,
				WorkingDir: workingDir,
			},
		},
	})
}

// PublishFileTabPathRevoked broadcasts a FileTabPathRevoked event to owner's
// subscribers.
func (b *PrivateEventsBus) PublishFileTabPathRevoked(owner userid.UserID, tabID string) {
	b.publish(owner, &leapmuxv1.WorkerPrivateEvent{
		Event: &leapmuxv1.WorkerPrivateEvent_FileTabPathRevoked{
			FileTabPathRevoked: &leapmuxv1.FileTabPathRevoked{
				TabId: tabID,
			},
		},
	})
}

// SnapshotAndSubscribe registers the subscriber under the bus mutex, takes the
// snapshot after releasing it, then streams that snapshot before any live event.
// This is the bootstrap-replay pattern the CRDT plan requires for
// FileTabPathRegistered events: the worker registers the live subscriber, reads
// the caller's `worker_file_tabs` rows, and sends them as
// `FileTabPathRegistered` events ahead of any live traffic.
//
// Register-then-snapshot, not snapshot-then-register: registering first means an
// event published during the read is already queued for this subscriber, so the
// window cannot drop one. The cost is that an event can appear in both the
// snapshot and the live stream, which is harmless because the client's applies
// are keyed by tab id and idempotent.
//
// snapshotFn receives the owner and returns the events that should be
// sent before the live stream. It runs with the bus mutex RELEASED -- the
// registration above is what makes that lossless, since any event published
// meanwhile already lands in this subscriber's channel -- so it may do real
// I/O; the production one queries SQLite. It
// must not call back into the bus, which would re-enter the lock it just left.
//
// An unminted owner is refused rather than registered under a blank key: it
// would receive nothing (publish refuses the same id) while still holding a
// buffered channel and a map entry for the life of the stream.
func (b *PrivateEventsBus) SnapshotAndSubscribe(
	ctx context.Context,
	owner userid.UserID,
	snapshotFn func(owner userid.UserID) []*leapmuxv1.WorkerPrivateEvent,
	sendFn func(*leapmuxv1.WorkerPrivateEvent) error,
) error {
	if owner.IsZero() {
		return nil
	}
	ownerKey := owner.String()
	subID := id.Generate()
	ch := make(chan *leapmuxv1.WorkerPrivateEvent, b.bufSize)

	b.mu.Lock()
	// Re-checked HERE, under the lock, not before it. The pre-lock read this
	// replaced could observe "open", lose the CPU, and resume after Stop had
	// finished -- registering a subscriber nothing would ever wake.
	if b.closed {
		b.mu.Unlock()
		return nil
	}
	byID, ok := b.subscribers[ownerKey]
	if !ok {
		byID = make(map[string]chan *leapmuxv1.WorkerPrivateEvent)
		b.subscribers[ownerKey] = byID
	}
	byID[subID] = ch
	b.mu.Unlock()

	// Snapshot AFTER releasing the lock. The registration above is what makes
	// that safe: every event published from here on lands in ch, so nothing can
	// slip between the snapshot and the subscription -- at worst an event appears
	// in both, and the client's applies are idempotent (register/revoke by tab id).
	//
	// It used to run under the WRITE lock, and the only production snapshotFn
	// issues a SQLite query on a context with no deadline. Every publisher takes
	// the read lock, so one subscriber's query stalled every other client's tab
	// renames and file-tab events on that worker for its duration -- bounded only
	// by the connection pool and a 60s busy timeout. The sibling decision in
	// crdt.Manager backgrounds an analogous DB lookup for exactly this reason.
	var snapshot []*leapmuxv1.WorkerPrivateEvent
	if snapshotFn != nil {
		snapshot = snapshotFn(owner)
	}

	defer func() {
		b.mu.Lock()
		if byID, ok := b.subscribers[ownerKey]; ok {
			delete(byID, subID)
			if len(byID) == 0 {
				delete(b.subscribers, ownerKey)
			}
		}
		b.mu.Unlock()
	}()

	for _, evt := range snapshot {
		if err := sendFn(evt); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-b.done:
			// Bus stopped. Nothing closes `ch` -- see the note on the struct --
			// so this is the only shutdown signal.
			return nil
		case evt := <-ch:
			if err := sendFn(evt); err != nil {
				return err
			}
		}
	}
}
