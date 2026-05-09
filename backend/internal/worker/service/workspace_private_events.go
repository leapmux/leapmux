package service

import (
	"context"
	"sync"
	"sync/atomic"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
)

// PrivateEventsBus is the worker-local pub/sub for E2EE-only workspace
// events (currently TabRenamed). It mirrors the hub-side
// WorkspaceEventBus shape but lives on the worker so titles never leave
// E2EE.
//
// Subscribers are keyed by workspace_id. The bus does no per-user
// filtering — the calling site is expected to be inside a worker handler
// that already verified the caller's access via WorkspaceAuthorizer.
type PrivateEventsBus struct {
	mu          sync.RWMutex
	subscribers map[string]map[string]chan *leapmuxv1.WorkspacePrivateEvent
	closed      atomic.Bool
	bufSize     int
}

// NewPrivateEventsBus returns a ready-to-use bus.
func NewPrivateEventsBus() *PrivateEventsBus {
	return &PrivateEventsBus{
		subscribers: make(map[string]map[string]chan *leapmuxv1.WorkspacePrivateEvent),
		bufSize:     32,
	}
}

// Stop closes every subscriber channel and stops accepting new ones.
func (b *PrivateEventsBus) Stop() {
	if !b.closed.CompareAndSwap(false, true) {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, byID := range b.subscribers {
		for _, ch := range byID {
			close(ch)
		}
	}
	b.subscribers = nil
}

// Subscribe registers a subscriber for workspaceID and blocks until ctx
// is cancelled. sendFn is called for every event; an error from sendFn
// ends the subscription with that error.
//
// Thin wrapper over SnapshotAndSubscribe with no snapshot — kept for
// the common no-bootstrap case so call sites don't pass a nil
// snapshotFn explicitly.
func (b *PrivateEventsBus) Subscribe(ctx context.Context, workspaceID string, sendFn func(*leapmuxv1.WorkspacePrivateEvent) error) error {
	return b.SnapshotAndSubscribe(ctx, workspaceID, nil, sendFn)
}

// publish broadcasts evt to every subscriber of workspaceID.
func (b *PrivateEventsBus) publish(workspaceID string, evt *leapmuxv1.WorkspacePrivateEvent) {
	if b.closed.Load() {
		return
	}
	b.mu.RLock()
	byID := b.subscribers[workspaceID]
	subs := make([]chan *leapmuxv1.WorkspacePrivateEvent, 0, len(byID))
	for _, ch := range byID {
		subs = append(subs, ch)
	}
	b.mu.RUnlock()
	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
			// Drop on slow consumer. The next snapshot/full state will
			// allow the client to re-derive the missed change.
		}
	}
}

// PublishTabRenamed broadcasts a TabRenamed private event for
// workspaceID. originClientID is included so receivers can suppress
// echoes of their own renames.
func (b *PrivateEventsBus) PublishTabRenamed(workspaceID, tabID string, tabType leapmuxv1.TabType, title, originClientID string) {
	b.publish(workspaceID, &leapmuxv1.WorkspacePrivateEvent{
		Event: &leapmuxv1.WorkspacePrivateEvent_TabRenamed{
			TabRenamed: &leapmuxv1.TabRenamed{
				TabId:          tabID,
				TabType:        tabType,
				Title:          title,
				OriginClientId: originClientID,
			},
		},
	})
}

// PublishFileTabPathRegistered broadcasts a FileTabPathRegistered
// event for workspaceID. The path is plaintext on the wire — the bus
// only carries E2EE-bound traffic, so callers must ensure the
// surrounding transport is E2EE.
func (b *PrivateEventsBus) PublishFileTabPathRegistered(workspaceID, tabID, filePath string) {
	b.publish(workspaceID, &leapmuxv1.WorkspacePrivateEvent{
		Event: &leapmuxv1.WorkspacePrivateEvent_FileTabPathRegistered{
			FileTabPathRegistered: &leapmuxv1.FileTabPathRegistered{
				TabId:       tabID,
				WorkspaceId: workspaceID,
				FilePath:    filePath,
			},
		},
	})
}

// PublishFileTabPathRevoked broadcasts a FileTabPathRevoked event for
// workspaceID.
func (b *PrivateEventsBus) PublishFileTabPathRevoked(workspaceID, tabID string) {
	b.publish(workspaceID, &leapmuxv1.WorkspacePrivateEvent{
		Event: &leapmuxv1.WorkspacePrivateEvent_FileTabPathRevoked{
			FileTabPathRevoked: &leapmuxv1.FileTabPathRevoked{
				TabId: tabID,
			},
		},
	})
}

// SnapshotAndSubscribe takes the snapshot under the bus mutex,
// registers the subscriber atomically, then streams the snapshot
// before any live events. This is the bootstrap-replay pattern the
// CRDT plan requires for FileTabPathRegistered events: the worker
// snapshots `worker_file_tabs` for the workspace, atomically
// registers the live subscriber, then sends the snapshot as
// `FileTabPathRegistered` events ahead of any live traffic.
//
// snapshotFn receives the workspace_id and returns the events that
// should be sent before the live stream. It is called under the bus
// mutex; it must return quickly and must not call back into the bus.
func (b *PrivateEventsBus) SnapshotAndSubscribe(
	ctx context.Context,
	workspaceID string,
	snapshotFn func(workspaceID string) []*leapmuxv1.WorkspacePrivateEvent,
	sendFn func(*leapmuxv1.WorkspacePrivateEvent) error,
) error {
	if b.closed.Load() {
		return nil
	}
	subID := id.Generate()
	ch := make(chan *leapmuxv1.WorkspacePrivateEvent, b.bufSize)

	b.mu.Lock()
	byID, ok := b.subscribers[workspaceID]
	if !ok {
		byID = make(map[string]chan *leapmuxv1.WorkspacePrivateEvent)
		b.subscribers[workspaceID] = byID
	}
	byID[subID] = ch
	var snapshot []*leapmuxv1.WorkspacePrivateEvent
	if snapshotFn != nil {
		snapshot = snapshotFn(workspaceID)
	}
	b.mu.Unlock()

	defer func() {
		b.mu.Lock()
		if byID, ok := b.subscribers[workspaceID]; ok {
			delete(byID, subID)
			if len(byID) == 0 {
				delete(b.subscribers, workspaceID)
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
		case evt, ok := <-ch:
			if !ok {
				return nil
			}
			if err := sendFn(evt); err != nil {
				return err
			}
		}
	}
}
