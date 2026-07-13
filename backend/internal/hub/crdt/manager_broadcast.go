package crdt

import (
	"context"
	"fmt"
	"log/slog"

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
// A shared `*MarshaledEvent` wrapper is built per entity transition and reused
// across every subscriber that needs the same direction, so its lazy `Bytes()`
// cache marshals the proto once for all WS writers instead of N times. Both the
// EntityRemoved wrappers (pure metadata) and the costlier EntityMaterialized
// wrappers (a deep clone of live state) are built lazily and memoized, so each
// is paid at most once per ref and only when some subscriber actually
// transitions across visibility in that direction. For the common in-place edit
// (stable visibility for every subscriber) neither is built at all.
//
// Per-subscriber visibility is computed inline from each subscriber's filter
// (see broadcastBatchToSubscriber) rather than prebuilt into a per-subscriber
// map, keeping the hot path free of an O(affected-entities) map allocation per
// subscriber.
func (m *Manager) broadcastBatch(batch *leapmuxv1.OpBatch, res ValidationResult) {
	m.projection.Lock()
	defer m.projection.Unlock()
	subs := m.snapshotSubs()
	if len(subs) == 0 {
		return
	}
	atHLC := lastBatchHLC(batch)

	// Memoize the per-subscriber batch event by the subscriber's visibility
	// bitmask over the workspaces this batch touches. A subscriber's visible-op
	// subset is a pure function of its IsAllowed verdict over those workspaces
	// (each op is kept iff its target's Pre/Post pass the filter), so two
	// subscribers with identical verdicts get a byte-identical filtered batch and
	// can share one MarshaledEvent -- marshaling the proto once for all of them
	// instead of once per subscriber (the sibling materialized/removed events are
	// already shared this way; the batch frame was the last per-subscriber marshal).
	// Disabled (nil cache) when the batch touches more distinct workspaces than a
	// uint64 mask can key -- vanishingly rare, since a batch normally targets a
	// single workspace's tree -- in which case each subscriber builds its own event.
	wsKeys := batchWorkspaceKeys(batch, res)
	var batchEventCache map[uint64]*MarshaledEvent
	if len(wsKeys) <= 64 {
		batchEventCache = make(map[uint64]*MarshaledEvent)
	}

	// EntityRemoved events are sent only to a subscriber that transitions OUT of
	// visibility (pre && !post) -- rare for the common in-place edit. Build them
	// lazily and memoize (nil results included) exactly like materialized below,
	// so one wrapper is shared across every subscriber that transitions the same
	// entity out and nothing is built for a stable-visibility batch. A ref with
	// Pre == "" resolves to nil (IsAllowed("") is false for every filter, so no
	// subscriber had the entity visible before the batch and the pre && !post
	// caller condition can never fire for it).
	removedCache := map[EntityRef]*MarshaledEvent{}
	removed := func(ref EntityRef) *MarshaledEvent {
		if evt, built := removedCache[ref]; built {
			return evt
		}
		var evt *MarshaledEvent
		if res.AffectedEntities[ref].Pre != "" {
			evt = NewMarshaledEvent(buildEntityRemovedEvent(ref, atHLC))
		}
		removedCache[ref] = evt
		return evt
	}

	// EntityMaterialized events deep-clone live state and are sent only to a
	// subscriber that transitions INTO visibility (!pre && post) -- rare for the
	// common in-place edit. Build them lazily and memoize (nil results included),
	// so the clone happens at most once per ref and only when a subscriber needs
	// it. Re-taking m.mu.RLock per first build is safe: the RLock is mutually
	// exclusive with every writer of m.state's maps -- the manager goroutine's own
	// commit swap (m.mu.Lock) and MutateInternal (m.mu.Lock, from the lifecycle-
	// outbox consumer goroutine) -- so each read sees an un-torn map. And the only
	// cross-goroutine writer, MutateInternal, mutates only the Workspaces map,
	// which buildEntityMaterializedEvent never reads (it reads Tabs/Nodes/
	// FloatingWindows), so per-ref materializations stay mutually consistent even
	// across a concurrent lifecycle create/delete. buildEntityMaterializedEvent
	// requires that read lock held.
	materializedCache := map[EntityRef]*MarshaledEvent{}
	materialized := func(ref EntityRef) *MarshaledEvent {
		if evt, built := materializedCache[ref]; built {
			return evt
		}
		m.mu.RLock()
		event := buildEntityMaterializedEvent(m.state, ref, atHLC)
		m.mu.RUnlock()
		var evt *MarshaledEvent
		if event != nil {
			evt = NewMarshaledEvent(event)
		}
		materializedCache[ref] = evt
		return evt
	}

	for _, sub := range subs {
		m.broadcastBatchToSubscriber(sub, batch, res, wsKeys, batchEventCache, materialized, removed)
	}
}

// batchWorkspaceKeys returns, in first-seen order, the distinct non-empty
// workspace ids whose visibility gates any op in the batch (the Pre/Post of
// each op target's transition). A subscriber's visible-op subset depends only on
// its IsAllowed verdict over exactly these ids -- IsAllowed("") is always false,
// so the empty workspace needs no bit -- which is what makes the visibility
// bitmask a sound cache key. The order fixes each id's bit position.
func batchWorkspaceKeys(batch *leapmuxv1.OpBatch, res ValidationResult) []string {
	var keys []string
	seen := make(map[string]struct{})
	add := func(ws string) {
		if ws == "" {
			return
		}
		if _, ok := seen[ws]; ok {
			return
		}
		seen[ws] = struct{}{}
		keys = append(keys, ws)
	}
	for _, op := range batch.GetOps() {
		trans := res.AffectedEntities[OpTarget(op)]
		add(trans.Pre)
		add(trans.Post)
	}
	return keys
}

// subscriberWorkspaceMask packs a subscriber's IsAllowed verdict over wsKeys
// into a bitmask (bit i set iff wsKeys[i] is visible to the subscriber). Two
// subscribers with the same mask see the same batch ops. Caller guarantees
// len(wsKeys) <= 64.
func subscriberWorkspaceMask(sub *Subscriber, wsKeys []string) uint64 {
	var mask uint64
	for i, ws := range wsKeys {
		if sub.Filter.IsAllowed(ws) {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// batchVisibleOpsEvent filters batch to the ops the given visibility verdict
// keeps and wraps them in a MarshaledEvent, or returns nil when none are
// visible. Split out of broadcastBatchToSubscriber so the event can be built
// once per distinct visibility bitmask and shared across subscribers.
func batchVisibleOpsEvent(batch *leapmuxv1.OpBatch, visible func(EntityRef) subscriberVisibility) *MarshaledEvent {
	// Lazy-allocate the visibleOps slice: subscribers with tight filters often
	// see zero ops in a batch, so allocating only on first append keeps the
	// all-filtered-out case at zero allocations.
	var visibleOps []*leapmuxv1.OrgOp
	for _, op := range batch.GetOps() {
		ref := OpTarget(op)
		v := visible(ref)
		var keep bool
		if ref.Kind == EntityKindWorkspaceRoot {
			// WorkspaceRoot: send if either pre or post visible (register lives on
			// WorkspaceContentsRecord, no EntityMaterialized arm).
			keep = v.preVisible || v.postVisible
		} else {
			// Other entities: send only if BOTH pre and post visible (stable
			// visibility). Becoming-visible / becoming-hidden are handled by
			// EntityMaterialized / EntityRemoved.
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
	if len(visibleOps) == 0 {
		return nil
	}
	// Sending the filtered subset as a single WatchOrgEvent_Batch avoids leaking
	// ops affecting workspaces the subscriber can't see while still delivering the
	// batch atomically to the frontend.
	return NewMarshaledEvent(&leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_Batch{
			Batch: &leapmuxv1.OpBatch{
				BatchId: batch.GetBatchId(),
				Ops:     visibleOps,
			},
		},
	})
}

// subscriberVisibility carries the pre/post-batch visibility flags an
// entity has for a given subscriber filter, computed inline per subscriber
// in broadcastBatchToSubscriber.
type subscriberVisibility struct {
	preVisible  bool
	postVisible bool
}

func (m *Manager) broadcastBatchToSubscriber(
	sub *Subscriber,
	batch *leapmuxv1.OpBatch,
	res ValidationResult,
	wsKeys []string,
	batchEventCache map[uint64]*MarshaledEvent,
	materialized func(EntityRef) *MarshaledEvent,
	removed func(EntityRef) *MarshaledEvent,
) {
	// visible computes a ref's pre/post visibility for this subscriber inline
	// from its filter, avoiding a prebuilt O(affected-entities) map per
	// subscriber: IsAllowed is a single cheap lookup, and for a nil/all-workspaces
	// filter it reduces to "workspace id non-empty" -- the same value the old
	// shared map held. A ref absent from AffectedEntities yields the zero
	// transition (Pre == Post == ""), i.e. not visible either side, matching the
	// old map's zero value for a missing key.
	visible := func(ref EntityRef) subscriberVisibility {
		trans := res.AffectedEntities[ref]
		return subscriberVisibility{
			preVisible:  sub.Filter.IsAllowed(trans.Pre),
			postVisible: sub.Filter.IsAllowed(trans.Post),
		}
	}

	// Batch event: the filtered op subset a subscriber sees is determined by its
	// visibility verdict over the batch's workspaces, so memoize the resulting
	// MarshaledEvent by that verdict's bitmask (when enabled). Subscribers with an
	// identical mask share one event -- and thus one proto marshal -- instead of
	// each building and marshaling their own. A cache miss builds the event (nil
	// when the subscriber sees no ops, cached so a same-mask peer skips the
	// rebuild); a hit reuses it. The shared *MarshaledEvent is read-only to every
	// subscriber (only its sync.Once-guarded Bytes() is called), so fanning it out
	// is safe -- identical to how materialized/removed events are already shared.
	if batchEventCache != nil {
		mask := subscriberWorkspaceMask(sub, wsKeys)
		evt, built := batchEventCache[mask]
		if !built {
			evt = batchVisibleOpsEvent(batch, visible)
			batchEventCache[mask] = evt
		}
		if evt != nil {
			_ = sub.Send(evt)
		}
	} else if evt := batchVisibleOpsEvent(batch, visible); evt != nil {
		_ = sub.Send(evt)
	}

	// Emit one EntityMaterialized / EntityRemoved per affected entity.
	// Range over key AND transition so the per-(subscriber × entity) visibility
	// test does not re-hash the key the range just handed it (the `visible`
	// closure above re-looks-up AffectedEntities[ref]; inlining the value here
	// skips S×E redundant lookups across a broadcast).
	// The shared event pointer is safe to fan out: subscribers treat
	// it as read-only and the WS writer marshals on its own thread.
	for ref, trans := range res.AffectedEntities {
		preVisible := sub.Filter.IsAllowed(trans.Pre)
		postVisible := sub.Filter.IsAllowed(trans.Post)
		if preVisible == postVisible {
			continue
		}
		if !preVisible && postVisible {
			if evt := materialized(ref); evt != nil {
				_ = sub.Send(evt)
			}
		} else if preVisible && !postVisible {
			if evt := removed(ref); evt != nil {
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
	m.projection.Lock()
	defer m.projection.Unlock()
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
// workspace to that subscriber's Filter without crossing its immutable
// WorkspaceScopeID. Idempotent — calling on an already-allowed
// subscriber is a no-op.
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
// avoid holding the manager write lock across `m.auth.CanAccessWorkspace`
// — which may be DB-backed and would stall every concurrent
// submit/commit — the call is staged: snapshot the subscriber set
// under RLock, evaluate the ACL outside any manager lock, then
// briefly take the write lock to insert the allowed entries. The
// subscriber set is keyed by pointer, so a subscriber that
// unsubscribed between the snapshot and the write is detected via the
// membership re-check.
func (m *Manager) ExpandSubscribersForWorkspace(ctx context.Context, workspaceID string) error {
	if workspaceID == "" {
		return nil
	}
	// Serialize the whole read-ACL-then-apply against SubscribeWithACL's
	// resolve+register under the same lock it uses. Without this, a subscriber
	// that resolved its filter before this workspace's create committed but
	// registered after this expand ran would be missed by both (the expand
	// only visits already-registered subscribers) and never see the workspace
	// until reconnect. subscribeExpandMu is NOT m.projection, so serializing
	// here does not block broadcasts. Lock order subscribeExpandMu ->
	// subscribers is consistent with SubscribeWithACL's subscribeExpandMu ->
	// projection -> m.mu, so no inversion is introduced.
	m.subscribeExpandMu.Lock()
	defer m.subscribeExpandMu.Unlock()
	type candidate struct {
		sub    *Subscriber
		userID string
	}
	var candidates []candidate
	m.subscribers.ForEachLocked(func(sub *Subscriber) {
		if !subscriberMaySeeWorkspace(sub, workspaceID) {
			return
		}
		if sub.Filter.IsAllowed(workspaceID) {
			return
		}
		candidates = append(candidates, candidate{sub: sub, userID: sub.UserID})
	})

	// Resolve read access once per DISTINCT candidate user, then map the
	// answers back onto subscribers. accessWorkspaceForUsers owns the
	// batch-capable-vs-per-user dispatch and its "propagate the lookup error,
	// never fold to deny" contract, so this path cannot drift from the scoped
	// checker's own batch forwarding (it used to re-implement both arms here).
	// The batch-capable checker (the production crdtAuthChecker) loads the
	// workspace once for all candidate users instead of a per-subscriber
	// round-trip; a nil checker allows every may-see candidate. `allowed` is
	// keyed by subscriber pointer so the MutateEach membership test below is
	// O(1), not an O(subscribers x allowed) linear scan.
	allowed := make(map[*Subscriber]struct{}, len(candidates))
	if m.auth == nil {
		for _, c := range candidates {
			allowed[c.sub] = struct{}{}
		}
	} else {
		userIDs := make([]string, 0, len(candidates))
		seen := make(map[string]struct{}, len(candidates))
		for _, c := range candidates {
			if _, dup := seen[c.userID]; dup {
				continue
			}
			seen[c.userID] = struct{}{}
			userIDs = append(userIDs, c.userID)
		}
		readable, err := accessWorkspaceForUsers(ctx, m.auth, m.orgID, workspaceID, userIDs)
		if err != nil {
			// Surface the lookup failure so the caller (workspace-create) can retry
			// instead of treating a transient DB error as "nobody may read" and
			// silently dropping the new workspace's seed broadcast.
			return fmt.Errorf("resolve workspace read access for %s: %w", workspaceID, err)
		}
		for _, c := range candidates {
			if readable[c.userID] {
				allowed[c.sub] = struct{}{}
			}
		}
	}

	if len(allowed) == 0 {
		return nil
	}

	// Mutate under the exclusive controller lock so the map updates cannot race
	// lock-free broadcasts. MutateEach publishes deep replacement snapshots
	// before releasing the lock.
	m.subscribers.MutateEach(func(sub *Subscriber) {
		if _, found := allowed[sub]; !found {
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
	return nil
}

// contractSubscribersForWorkspace removes `workspaceID` from every subscriber's
// Filter. Used by the lifecycle-create rollback (undo the optimistic expand when
// the seed batch is rejected) and by lifecycle-delete (drop a deleted workspace
// from long-lived filters), so a stray filter entry can't point at a workspace
// that no longer exists in `m.state`.
//
// It takes subscribeExpandMu -- the SAME lock ExpandSubscribersForWorkspace and
// SubscribeWithACL hold -- so the contract is serialized against BOTH the
// expand pass and a newly-registering subscriber. Two races would otherwise be
// open. The single-key delete itself cannot corrupt a concurrent expand's
// outcome (MutateEach serializes the map writes, and a create and a delete of
// the SAME workspace never overlap: the workspace must exist before it can be
// deleted, and SubmitLifecycle drains the outbox sequentially under
// lifecycleMu). The one that DOES need this lock is the phantom-key race against
// a subscriber whose SubscribeWithACL.resolve() read the pre-delete ACL (W still
// present) but which registers after this contract ran: without shared
// serialization it would keep W as a stale filter key no pass ever removes.
// Holding subscribeExpandMu closes both against SubscribeWithACL's resolve+
// register and against the expand pass, so the serialization is uniform rather
// than resting on the single-key argument alone.
//
// Lock order holds: both callers (applyLifecycleCreate's rollback, which runs
// only after ExpandSubscribersForWorkspace has released subscribeExpandMu, and
// applyLifecycleDelete) hold lifecycleMu and no subscribeExpandMu here, so this
// takes the documented lifecycleMu -> subscribeExpandMu -> subscribers edge that
// ExpandSubscribersForWorkspace already takes; nothing acquires lifecycleMu
// while holding subscribeExpandMu.
func (m *Manager) contractSubscribersForWorkspace(workspaceID string) {
	if workspaceID == "" {
		return
	}
	m.subscribeExpandMu.Lock()
	defer m.subscribeExpandMu.Unlock()
	m.subscribers.MutateEach(func(sub *Subscriber) {
		if sub.Filter.WorkspaceIDs == nil {
			return
		}
		delete(sub.Filter.WorkspaceIDs, workspaceID)
	})
}

// ContractSubscribersForWorkspaceForTest exposes contractSubscribersForWorkspace
// to the package's external tests so they can assert its serialization against
// SubscribeWithACL directly, without staging a delete through the lifecycle
// outbox. Production callers reach it via the lifecycle apply path only.
func (m *Manager) ContractSubscribersForWorkspaceForTest(workspaceID string) {
	m.contractSubscribersForWorkspace(workspaceID)
}

// BroadcastWorkspaceCreated / Renamed / Deleted are called by the
// lifecycle outbox consumer.
//
// This public entry point is for callers that wire it DIRECTLY without staging
// an outbox row (tests, ad-hoc broadcasts): it expands each subscriber's filter
// to admit the workspace, then broadcasts the event. The production outbox path
// (applyLifecycleCreate) already expanded and gated its seed batch on the ACL,
// so it calls broadcastWorkspaceCreatedEvent directly rather than paying the
// read-ACL lookup a second time here.
func (m *Manager) BroadcastWorkspaceCreated(ctx context.Context, workspaceID, title, rootNodeID string) {
	// Best-effort idempotent expand for a direct caller (no outbox row to retry).
	// A transient failure is logged, not fatal -- the event is still worth
	// broadcasting to whoever already admits the workspace.
	if err := m.ExpandSubscribersForWorkspace(ctx, workspaceID); err != nil {
		slog.Warn("crdt: expand subscribers on workspace-created broadcast failed",
			"workspace", workspaceID, "error", err)
	}
	m.broadcastWorkspaceCreatedEvent(workspaceID, title, rootNodeID)
}

// broadcastWorkspaceCreatedEvent fans out the WorkspaceCreated event to
// subscribers that already admit workspaceID. The caller MUST have already
// expanded the subscriber filters (applyLifecycleCreate does so before its seed
// batch; BroadcastWorkspaceCreated does so inline) -- this does not re-issue the
// read-ACL lookup.
func (m *Manager) broadcastWorkspaceCreatedEvent(workspaceID, title, rootNodeID string) {
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
