package crdt

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// snapshotSubs returns the current subscriber slice. Safe to call
// without holding any lock — the slice is owned by SubscriberController's
// snapshot publisher and replaced (not mutated) on every Add/Remove.
func (m *Manager) snapshotSubs() []*Subscriber {
	return m.subscribers.Snapshot()
}

// broadcastBatch fan-outs ops + materialized/removed events per
// subscriber based on the visibility transition rules.
//
// The per-entity EntityMaterialized / EntityRemoved events are built
// ONCE here and the same `*MarshaledEvent` wrapper is shared across
// every subscriber that needs the same transition direction. The
// wrapper's lazy `Bytes()` cache ensures the proto.Marshal cost is
// paid once per shared event across all WS writers — without it, N
// subscribers re-marshal the same bytes N times and the broadcast-
// dedupe win is undone on the wire.
//
// Per-subscriber visibility classification (`visMap`) is also
// memoized: every all-workspaces subscriber shares one allocation,
// since their filter rule reduces to "non-empty workspace passes".
// Per-workspace filters still allocate their own map (one per
// subscriber) — the contents depend on the filter set.
func (m *Manager) broadcastBatch(batch *leapmuxv1.OpBatch, res ValidationResult) {
	subs := m.snapshotSubs()
	if len(subs) == 0 {
		return
	}
	atHLC := lastBatchHLC(batch)
	materialized := map[EntityRef]*MarshaledEvent{}
	removed := map[EntityRef]*MarshaledEvent{}
	// m.state reads still need the RLock — entity events project from
	// the live state and the manager goroutine concurrently mutates it
	// under m.mu.Lock() in commit. The subscriber slice is owned by the
	// snapshot publisher (no lock needed for the iteration above/below).
	//
	// Skip building materialized/removed when no subscriber can act on
	// them: Pre=="" means no subscriber had the entity visible before
	// (IsAllowed("") is false for every filter), so the EntityRemoved
	// path is unreachable; symmetrically Post=="" means no subscriber
	// can see it after, so EntityMaterialized is unreachable.
	m.mu.RLock()
	for ref, trans := range res.AffectedEntities {
		if trans.Post != "" {
			if evt := buildEntityMaterializedEvent(m.state, ref, atHLC); evt != nil {
				materialized[ref] = NewMarshaledEvent(evt)
			}
		}
		if trans.Pre != "" {
			removed[ref] = NewMarshaledEvent(buildEntityRemovedEvent(ref, atHLC))
		}
	}
	m.mu.RUnlock()

	// Pre-compute the visibility map shared by every all-workspaces
	// subscriber. For the empty-filter case, IsAllowed reduces to
	// "workspace id is non-empty", so the same answer applies to every
	// subscriber in that group.
	allVis := make(map[EntityRef]subscriberVisibility, len(res.AffectedEntities))
	for ref, trans := range res.AffectedEntities {
		allVis[ref] = subscriberVisibility{
			preVisible:  trans.Pre != "",
			postVisible: trans.Post != "",
		}
	}

	for _, sub := range subs {
		var visMap map[EntityRef]subscriberVisibility
		if len(sub.Filter.WorkspaceIDs) == 0 {
			visMap = allVis
		} else {
			visMap = make(map[EntityRef]subscriberVisibility, len(res.AffectedEntities))
			for ref, trans := range res.AffectedEntities {
				visMap[ref] = subscriberVisibility{
					preVisible:  sub.Filter.IsAllowed(trans.Pre),
					postVisible: sub.Filter.IsAllowed(trans.Post),
				}
			}
		}
		m.broadcastBatchToSubscriber(sub, batch, res, visMap, materialized, removed)
	}
}

// subscriberVisibility carries the pre/post-batch visibility flags an
// entity has for a given subscriber filter. Pulled out as a named type
// so the shared all-workspaces map can be reused across subscribers
// (see broadcastBatch).
type subscriberVisibility struct {
	preVisible  bool
	postVisible bool
}

func (m *Manager) broadcastBatchToSubscriber(
	sub *Subscriber,
	batch *leapmuxv1.OpBatch,
	res ValidationResult,
	visMap map[EntityRef]subscriberVisibility,
	materialized map[EntityRef]*MarshaledEvent,
	removed map[EntityRef]*MarshaledEvent,
) {

	// Filter the batch to ops the subscriber can see. Per-op rules:
	//   - WorkspaceRoot: send if either pre or post visible (register
	//     lives on WorkspaceContentsRecord, no EntityMaterialized arm).
	//   - Other entities: send only if BOTH pre and post visible
	//     (stable visibility). Becoming-visible / becoming-hidden are
	//     handled by EntityMaterialized / EntityRemoved below.
	//
	// Sending the filtered subset as a single WatchOrgEvent_Batch
	// avoids leaking ops affecting workspaces the subscriber can't see
	// while still delivering the batch atomically to the frontend.
	// Lazy-allocate the visibleOps slice: subscribers with tight filters
	// often see zero ops in a batch, and broadcastBatchToSubscriber runs
	// per-subscriber on every commit. Allocating only on first append
	// keeps the all-filtered-out case at zero allocations.
	var visibleOps []*leapmuxv1.OrgOp
	for _, op := range batch.GetOps() {
		ref := OpTarget(op)
		v := visMap[ref]
		var keep bool
		if ref.Kind == EntityKindWorkspaceRoot {
			keep = v.preVisible || v.postVisible
		} else {
			keep = v.preVisible && v.postVisible
		}
		if !keep {
			continue
		}
		if visibleOps == nil {
			visibleOps = make([]*leapmuxv1.OrgOp, 0, len(batch.GetOps()))
		}
		visibleOps = append(visibleOps, op)
	}
	if len(visibleOps) > 0 {
		// The batch event is per-subscriber-unique (visibleOps depends on
		// the subscriber's filter) so each gets a fresh MarshaledEvent.
		_ = sub.Send(NewMarshaledEvent(&leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_Batch{
				Batch: &leapmuxv1.OpBatch{
					BatchId: batch.GetBatchId(),
					Ops:     visibleOps,
				},
			},
		}))
	}

	// Emit one EntityMaterialized / EntityRemoved per affected entity.
	// The shared event pointer is safe to fan out: subscribers treat
	// it as read-only and the WS writer marshals on its own thread.
	for ref := range res.AffectedEntities {
		v := visMap[ref]
		if v.preVisible == v.postVisible {
			continue
		}
		if !v.preVisible && v.postVisible {
			if evt := materialized[ref]; evt != nil {
				_ = sub.Send(evt)
			}
		} else if v.preVisible && !v.postVisible {
			if evt := removed[ref]; evt != nil {
				_ = sub.Send(evt)
			}
		}
	}
}

// buildEntityMaterializedEvent constructs the EntityMaterialized event
// for a single ref against `state`. Caller MUST hold m.mu (read lock is
// enough). Returns nil when the ref doesn't resolve to a live record.
func buildEntityMaterializedEvent(state *leapmuxv1.OrgCrdtState, ref EntityRef, atHLC *leapmuxv1.HLC) *leapmuxv1.WatchOrgEvent {
	switch ref.Kind {
	case EntityKindTab:
		t := state.GetTabs()[ref.TabID]
		if t == nil {
			return nil
		}
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityMaterialized{
				EntityMaterialized: &leapmuxv1.EntityMaterialized{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityMaterialized_Tab{Tab: cloneTab(t)},
				},
			},
		}
	case EntityKindFloatingWindow:
		fw := state.GetFloatingWindows()[ref.WindowID]
		if fw == nil {
			return nil
		}
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityMaterialized{
				EntityMaterialized: &leapmuxv1.EntityMaterialized{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityMaterialized_FloatingWindow{FloatingWindow: cloneFloatingWindow(fw)},
				},
			},
		}
	case EntityKindNode:
		n := state.GetNodes()[ref.NodeID]
		if n == nil {
			return nil
		}
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityMaterialized{
				EntityMaterialized: &leapmuxv1.EntityMaterialized{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityMaterialized_Node{Node: cloneNode(n)},
				},
			},
		}
	}
	return nil
}

// buildEntityRemovedEvent constructs the EntityRemoved wrapper for a
// ref. Unlike Materialized, this is pure metadata (no state lookup) so
// it never returns nil except for unrecognised kinds.
func buildEntityRemovedEvent(ref EntityRef, atHLC *leapmuxv1.HLC) *leapmuxv1.WatchOrgEvent {
	switch ref.Kind {
	case EntityKindTab:
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabType: ref.TabType, TabId: ref.TabID}},
				},
			},
		}
	case EntityKindFloatingWindow:
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_WindowId{WindowId: ref.WindowID},
				},
			},
		}
	case EntityKindNode:
		return &leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_NodeId{NodeId: ref.NodeID},
				},
			},
		}
	}
	return nil
}

func lastBatchHLC(batch *leapmuxv1.OpBatch) *leapmuxv1.HLC {
	ops := batch.GetOps()
	if len(ops) == 0 {
		return nil
	}
	// HLCs within a batch are minted by sequential Clock.Tick calls
	// inside a single now snapshot, so they share physical_ms and have
	// strictly increasing logicals — the last op carries the max.
	return HLCClone(ops[len(ops)-1].GetCanonicalHlc())
}

func (m *Manager) broadcastPresence(workspaceID, activeClientID string) {
	m.broadcastTo(workspaceID, &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{
				WorkspaceId:    workspaceID,
				ActiveClientId: activeClientID,
				UpdatedAt:      timestamppb.New(m.now()),
			},
		},
	})
}

// broadcastTo sends `evt` to every current subscriber whose Filter
// admits `workspaceID`. The MarshaledEvent wrapper is built once so
// every subscriber receives the same proto bytes; subscribers that
// can't see the workspace are skipped. No-op when there are no
// subscribers.
func (m *Manager) broadcastTo(workspaceID string, evt *leapmuxv1.WatchOrgEvent) {
	subs := m.snapshotSubs()
	if len(subs) == 0 {
		return
	}
	me := NewMarshaledEvent(evt)
	for _, sub := range subs {
		if !sub.Filter.IsAllowed(workspaceID) {
			continue
		}
		_ = sub.Send(me)
	}
}

// ExpandSubscribersForWorkspace re-checks the read ACL against
// `workspaceID` for every current subscriber and, on a hit, adds the
// workspace to that subscriber's Filter. Idempotent — calling on an
// already-allowed subscriber is a no-op.
//
// Why this needs to run BEFORE the lifecycle seed batch broadcasts:
// the new workspace is by definition not in any existing subscriber's
// Filter (Filter was computed at subscribe time over the user's
// then-accessible workspaces). Without pre-expansion, the seed
// `SetNodeRegister` (root LEAF) and `SetWorkspaceRootNode` ops fall
// into the broadcast filter's "neither pre nor post visible" arm and
// are silently dropped. Subscribers then observe the eventual
// `WorkspaceCreated` event but never the entities backing the
// workspace's tree — `seedTabIntoNewWorkspace` polls
// `state.workspaces[wsID].rootNodeId` forever and the agent tab the
// user just opened never lands in the CRDT projection.
//
// Locking discipline: this helper is called from the lifecycle-outbox
// consumer goroutine (`applyLifecycleCreate` runs on the
// workspace_service request handler, NOT the manager goroutine). To
// avoid holding the manager write lock across `m.auth.CanReadWorkspace`
// — which may be DB-backed and would stall every concurrent
// submit/commit — the call is staged: snapshot the subscriber set
// under RLock, evaluate the ACL outside any manager lock, then
// briefly take the write lock to insert the allowed entries. The
// subscriber set is keyed by pointer, so a subscriber that
// unsubscribed between the snapshot and the write is detected via the
// membership re-check.
func (m *Manager) ExpandSubscribersForWorkspace(ctx context.Context, workspaceID string) {
	if workspaceID == "" {
		return
	}
	type candidate struct {
		sub    *Subscriber
		userID string
	}
	var candidates []candidate
	m.subscribers.ForEachLocked(func(sub *Subscriber) {
		if sub.Filter.IsAllowed(workspaceID) {
			return
		}
		candidates = append(candidates, candidate{sub: sub, userID: sub.UserID})
	})

	allowed := make([]*Subscriber, 0, len(candidates))
	for _, c := range candidates {
		if m.auth != nil && !m.auth.CanReadWorkspace(ctx, m.orgID, workspaceID, c.userID) {
			continue
		}
		allowed = append(allowed, c.sub)
	}

	if len(allowed) == 0 {
		return
	}

	// Holding the subscribers read lock through the filter mutation
	// keeps a concurrent Remove from racing with the IsAllowed
	// re-check below — the subscriber pointer is stable while we
	// hold the lock, so any race resolves either "removed before we
	// write" (the unsubscribe wins and the filter mutation is
	// observed only by a pointer no longer in the set) or "we write
	// before remove" (the unsubscribe drops a now-expanded filter).
	m.subscribers.ForEachLocked(func(sub *Subscriber) {
		var found bool
		for _, s := range allowed {
			if s == sub {
				found = true
				break
			}
		}
		if !found {
			return
		}
		if sub.Filter.IsAllowed(workspaceID) {
			return
		}
		if sub.Filter.WorkspaceIDs == nil {
			sub.Filter.WorkspaceIDs = map[string]bool{}
		}
		sub.Filter.WorkspaceIDs[workspaceID] = true
	})
}

// contractSubscribersForWorkspace removes `workspaceID` from every
// subscriber's Filter. Used by the lifecycle-create rollback path so
// a rejected seed batch doesn't leave stray filter entries that point
// at a workspace which no longer exists in `m.state`.
func (m *Manager) contractSubscribersForWorkspace(workspaceID string) {
	if workspaceID == "" {
		return
	}
	m.subscribers.ForEachLocked(func(sub *Subscriber) {
		if sub.Filter.WorkspaceIDs == nil {
			return
		}
		delete(sub.Filter.WorkspaceIDs, workspaceID)
	})
}

// BroadcastWorkspaceCreated / Renamed / Deleted are called by the
// lifecycle outbox consumer.
//
// The filter-expansion on Create is normally already done by
// `applyLifecycleCreate` (before the seed batch broadcasts). The
// idempotent re-expand here covers any callers that wire
// `BroadcastWorkspaceCreated` directly without staging an outbox row.
func (m *Manager) BroadcastWorkspaceCreated(ctx context.Context, workspaceID, title, rootNodeID string) {
	m.ExpandSubscribersForWorkspace(ctx, workspaceID)
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Created{
			Created: &leapmuxv1.WorkspaceCreated{WorkspaceId: workspaceID, Title: title, RootNodeId: rootNodeID},
		},
	})
}

func (m *Manager) BroadcastWorkspaceRenamed(workspaceID, title string) {
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Renamed{
			Renamed: &leapmuxv1.WorkspaceRenamed{WorkspaceId: workspaceID, Title: title},
		},
	})
}

func (m *Manager) BroadcastWorkspaceDeleted(workspaceID string, workerIDs []string) {
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Deleted{
			Deleted: &leapmuxv1.WorkspaceDeleted{WorkspaceId: workspaceID, WorkerIds: workerIDs},
		},
	})
}

// broadcastWorkspaceLifecycle fans out `evt` to subscribers that admit
// `workspaceID`. Thin wrapper preserved as a name-readable call site
// in the Created/Renamed/Deleted helpers.
func (m *Manager) broadcastWorkspaceLifecycle(workspaceID string, evt *leapmuxv1.WatchOrgEvent) {
	m.broadcastTo(workspaceID, evt)
}
