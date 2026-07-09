package crdt

import (
	"context"
	"fmt"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"google.golang.org/protobuf/types/known/emptypb"
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
	// The shared event pointer is safe to fan out: subscribers treat
	// it as read-only and the WS writer marshals on its own thread.
	for ref := range res.AffectedEntities {
		v := visible(ref)
		if v.preVisible == v.postVisible {
			continue
		}
		if !v.preVisible && v.postVisible {
			if evt := materialized(ref); evt != nil {
				_ = sub.Send(evt)
			}
		} else if v.preVisible && !v.postVisible {
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
// avoid holding the manager write lock across `m.auth.CanReadWorkspace`
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
	// Serialize the whole read-ACL-then-apply against CommitWorkspaceAccessTransition
	// under the same lock it uses. Without this, an expand that resolved read access
	// for a subscriber could re-add that subscriber to the workspace's filter AFTER
	// a concurrent unshare of the same workspace committed and revoked them -- a
	// lost-update TOCTOU that leaves a revoked reader receiving the workspace's ops,
	// and which the transition's post-commit re-classify (already run) never
	// reconverges. aclTransitionMu is the lock the transition holds across its own
	// DB commit and is NOT m.projection, so serializing here does not block broadcasts.
	// Lock order aclTransitionMu -> subscribers is consistent with the transition's
	// aclTransitionMu -> projection -> subscribers, so no inversion is introduced.
	m.aclTransitionMu.Lock()
	defer m.aclTransitionMu.Unlock()
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

	// Resolve read access for every candidate. The batch-capable checker (the
	// production crdtAuthChecker) loads the workspace once and does a single grant
	// lookup for all candidate users, instead of a per-subscriber workspace-load +
	// grant round-trip. A checker without the batch capability falls back to the
	// per-candidate check; a nil checker allows every may-see candidate. Keyed by
	// subscriber pointer so the MutateEach membership test below is O(1), not an
	// O(subscribers x allowed) linear scan.
	allowed := make(map[*Subscriber]struct{}, len(candidates))
	batcher, canBatch := m.auth.(workspaceReaderBatch)
	switch {
	case m.auth == nil:
		for _, c := range candidates {
			allowed[c.sub] = struct{}{}
		}
	case canBatch:
		userIDs := make([]string, 0, len(candidates))
		seen := make(map[string]struct{}, len(candidates))
		for _, c := range candidates {
			if _, dup := seen[c.userID]; dup {
				continue
			}
			seen[c.userID] = struct{}{}
			userIDs = append(userIDs, c.userID)
		}
		readable, err := batcher.CanReadWorkspaceForUsers(ctx, m.orgID, workspaceID, userIDs)
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
	default:
		for _, c := range candidates {
			readable, err := m.auth.CanReadWorkspace(ctx, m.orgID, workspaceID, c.userID)
			if err != nil {
				// Surface a transient lookup failure so the caller
				// (workspace-create expansion) retries instead of dropping the
				// seed, matching the batch path above.
				return fmt.Errorf("resolve workspace read access for %s: %w", workspaceID, err)
			}
			if readable {
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

// CommitWorkspaceAccessTransition applies a workspace ACL change to both the DB
// and the in-memory subscriber projections, WITHOUT holding the org-wide
// projection lock across the DB commit -- so a share/unshare no longer stalls the
// org's op-broadcast pipeline for the transaction's duration. allowedUserIDs must
// be the complete post-commit reader set.
//
// The lock-free-across-commit safety rests on phase ordering, not on one big
// lock:
//   - REVOKES are applied to in-memory filters BEFORE the commit, so a user
//     losing access stops receiving this workspace's ops before the unshare is
//     even durable -- no broadcast can deliver to a just-revoked reader. If the
//     commit then fails, the revokes are rolled back (the change never persisted,
//     so those readers keep their access).
//   - GRANTS are applied AFTER the commit, so a broadcast can never deliver this
//     workspace's ops to a grant that never persisted; the grantee catches up via
//     the grant projection. The post-commit pass re-classifies both directions,
//     which also revokes any already-registered subscriber whose visibility this
//     transition flipped. A NEW org-events subscriber cannot slip in with a stale
//     pre-commit filter: SubscribeWithACL serializes its resolve+register under
//     this same aclTransitionMu, so it either registered (pre-commit filter) BEFORE
//     this transition -- caught by the passes here as an already-registered
//     subscriber -- or resolves AFTER this transition releases the lock, reading the
//     post-commit ACL directly.
//
// aclTransitionMu serializes the whole three-phase sequence so two transitions of
// the same workspace cannot interleave; it is not m.projection, so holding it
// across the commit does not block broadcasts.
func (m *Manager) CommitWorkspaceAccessTransition(
	workspaceID string,
	allowedUserIDs map[string]struct{},
	commit func() error,
) error {
	if workspaceID == "" {
		return fmt.Errorf("workspace ID is required")
	}
	isAllowed := func(sub *Subscriber) bool {
		if !subscriberMaySeeWorkspace(sub, workspaceID) {
			return false
		}
		_, allowed := allowedUserIDs[sub.UserID]
		return allowed
	}

	m.aclTransitionMu.Lock()
	defer m.aclTransitionMu.Unlock()

	// Phase 1 -- revokes, before the commit (applyGrants=false).
	m.projection.Lock()
	revoked := m.applyWorkspaceVisibilityLocked(workspaceID, isAllowed, false)
	m.projection.Unlock()

	// Phase 2 -- the DB commit, with NO manager lock held.
	if err := commit(); err != nil {
		// The ACL change did not persist: undo the premature Phase 1 revokes so
		// those readers keep their access.
		m.projection.Lock()
		m.regrantWorkspaceLocked(workspaceID, revoked)
		m.projection.Unlock()
		return err
	}

	// Phase 3 -- grants (and straggler revokes), after the commit (applyGrants=true).
	m.projection.Lock()
	m.applyWorkspaceVisibilityLocked(workspaceID, isAllowed, true)
	m.projection.Unlock()
	return nil
}

// applyWorkspaceVisibilityLocked publishes the minimum grant/revoke plan for one
// workspace and returns the subscribers it revoked (so a failed ACL commit can
// undo exactly them). The caller must hold projection so filter mutation and
// event publication are atomic with respect to subscriptions and CRDT broadcasts.
//
// applyGrants gates the grant half of the plan: CommitWorkspaceAccessTransition's
// pre-commit revoke pass passes false (apply only revokes -- a grant must never be
// published for an ACL change that has not yet persisted), and its post-commit
// pass passes true (grants, plus any straggler revoke). Revokes are always applied.
func (m *Manager) applyWorkspaceVisibilityLocked(workspaceID string, isAllowed func(*Subscriber) bool, applyGrants bool) []*Subscriber {
	// Classify each subscriber's transition ONCE into a grant/revoke plan: a
	// subscriber needs a grant when it should now see the workspace but its filter
	// does not yet admit it, and a revoke in the opposite case; an unchanged
	// subscriber is skipped. Keyed by subscriber pointer so the MutateEach apply
	// below is an O(1) membership test. The plan still describes the right
	// subscribers even though the controller lock is released between this classify
	// (ForEachLocked) pass and the apply (MutateEach) pass: MutateEach visits only
	// LIVE subscribers, so one that unsubscribed in the gap is silently skipped
	// (Remove takes only the controller lock, NOT projection), and no NEW subscriber
	// can appear (Add runs under the projection lock the caller holds) -- one that
	// somehow did would be absent from the plan and skipped anyway. The verdict
	// cannot drift either: isAllowed(sub) is a pure function of the subscriber's
	// immutable scope/requested fields and the fixed workspaceID/isAllowed argument,
	// and the apply only sets or deletes the single workspaceID key, so a concurrent
	// single-key filter mutation in the gap (contractSubscribersForWorkspace, which
	// holds neither projection nor aclTransitionMu) cannot corrupt the outcome.
	// (Mirrors the classify-then-apply shape of ExpandSubscribersForWorkspace above.)
	grants := map[*Subscriber]struct{}{}
	revokes := map[*Subscriber]struct{}{}
	needsWorkspaceUniverse := false
	m.subscribers.ForEachLocked(func(sub *Subscriber) {
		allowed := isAllowed(sub)
		if sub.Filter.IsAllowed(workspaceID) == allowed {
			return
		}
		if allowed {
			grants[sub] = struct{}{}
			return
		}
		revokes[sub] = struct{}{}
		needsWorkspaceUniverse = needsWorkspaceUniverse || sub.Filter.WorkspaceIDs == nil
	})
	if !applyGrants {
		// Defer grants to the post-commit pass; apply only revokes now.
		grants = nil
	}
	if len(grants) == 0 && len(revokes) == 0 {
		return nil
	}

	var workspaceIDs []string
	// Hold m.mu.RLock across the filter conversion below (the MutateEach) so the
	// workspace-universe snapshot and the see-all -> explicit materialization are
	// atomic with respect to a concurrent workspace create (MutateInternal takes
	// m.mu.Lock, not m.projection, on a request goroutine). Releasing the lock
	// before the conversion would let a workspace committed in the window be
	// dropped from a revoked see-all subscriber's now-explicit filter and never
	// re-admitted -- that workspace's own ExpandSubscribersForWorkspace skips a
	// still-see-all subscriber. Lock order stays projection > m.mu > controller:
	// the MutateEach callback takes no m.mu, and the production Send is a
	// non-blocking buffered-channel push, so the widened read hold cannot stall
	// under the controller lock.
	m.mu.RLock()
	defer m.mu.RUnlock()
	if needsWorkspaceUniverse {
		workspaceIDs = make([]string, 0, len(m.state.GetWorkspaces()))
		for id := range m.state.GetWorkspaces() {
			workspaceIDs = append(workspaceIDs, id)
		}
	}
	var grantEvent *MarshaledEvent
	if len(grants) > 0 {
		grantEvent = m.workspaceGrantEventLocked(workspaceID)
	}
	var revokeEvent *MarshaledEvent
	if len(revokes) > 0 {
		revokeEvent = NewMarshaledEvent(&leapmuxv1.WatchOrgEvent{
			Event: &leapmuxv1.WatchOrgEvent_WorkspaceProjection{
				WorkspaceProjection: &leapmuxv1.WorkspaceProjectionChanged{
					WorkspaceId: workspaceID,
					Change: &leapmuxv1.WorkspaceProjectionChanged_Revoked{
						Revoked: &emptypb.Empty{},
					},
				},
			},
		})
	}

	// Correctness depends on the Subscriber.Send contract being upheld strictly:
	// Send MUST return promptly (non-blocking) and MUST NOT re-enter the
	// SubscriberController or the manager's projection lock. This MutateEach is the
	// only place Send runs while the SubscriberController's exclusive (write) lock
	// is held; broadcastTo / broadcastBatch instead fan out over the lock-free
	// subscriber Snapshot -- but they still Send while holding m.projection, so the
	// same contract is load-bearing there too. The production Send (ws_orgevents.go)
	// is a non-blocking buffered-channel push whose full-buffer drop path cancels
	// the subscriber's ctx and returns ErrSubscriberSlow -- it does NOT
	// synchronously Remove/unsub the subscriber (that fires later via the WS
	// handler's deferred unsub). A blocking Send would stall every subscriber under
	// whichever lock is held; a Send that called back into the controller
	// (Add/Remove/MutateEach) or re-took m.projection would deadlock.
	var revoked []*Subscriber
	m.subscribers.MutateEach(func(sub *Subscriber) {
		if _, ok := grants[sub]; ok {
			if sub.Filter.WorkspaceIDs != nil {
				sub.Filter.WorkspaceIDs[workspaceID] = true
			}
			_ = sub.Send(grantEvent)
			return
		}
		if _, ok := revokes[sub]; !ok {
			return
		}
		if sub.Filter.WorkspaceIDs == nil {
			sub.Filter.WorkspaceIDs = make(map[string]bool, len(workspaceIDs))
			for _, id := range workspaceIDs {
				sub.Filter.WorkspaceIDs[id] = true
			}
		}
		delete(sub.Filter.WorkspaceIDs, workspaceID)
		_ = sub.Send(revokeEvent)
		revoked = append(revoked, sub)
	})
	return revoked
}

// workspaceGrantEventLocked builds the WorkspaceProjectionChanged_Granted event
// carrying the full current projection of workspaceID, so a newly-admitted
// subscriber materializes the workspace it just gained. Caller holds m.mu (read).
func (m *Manager) workspaceGrantEventLocked(workspaceID string) *MarshaledEvent {
	projection := m.materializedLocked(SubscriberFilter{WorkspaceIDs: map[string]bool{workspaceID: true}})
	return NewMarshaledEvent(&leapmuxv1.WatchOrgEvent{
		Event: &leapmuxv1.WatchOrgEvent_WorkspaceProjection{
			WorkspaceProjection: &leapmuxv1.WorkspaceProjectionChanged{
				WorkspaceId: workspaceID,
				Change: &leapmuxv1.WorkspaceProjectionChanged_Granted{
					Granted: projection,
				},
			},
		},
	})
}

// regrantWorkspaceLocked re-admits workspaceID into the filters of exactly the
// given subscribers and re-sends them the workspace projection. Used ONLY to roll
// back a pre-commit revoke pass when the ACL commit fails: the change never
// persisted, so those subscribers keep their access. A subscriber that Phase 1
// converted from see-all (nil filter) to explicit stays explicit here (re-admitted
// via the explicit map) -- functionally equivalent, and the same end state a
// committed revoke would leave; a subscriber that has since unsubscribed is a
// harmless no-op (MutateEach only visits live subscribers). Caller holds
// projection.
func (m *Manager) regrantWorkspaceLocked(workspaceID string, subs []*Subscriber) {
	if len(subs) == 0 {
		return
	}
	target := make(map[*Subscriber]struct{}, len(subs))
	for _, s := range subs {
		target[s] = struct{}{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	grantEvent := m.workspaceGrantEventLocked(workspaceID)
	m.subscribers.MutateEach(func(sub *Subscriber) {
		if _, ok := target[sub]; !ok {
			return
		}
		if sub.Filter.WorkspaceIDs != nil {
			sub.Filter.WorkspaceIDs[workspaceID] = true
		}
		_ = sub.Send(grantEvent)
	})
}

// contractSubscribersForWorkspace removes `workspaceID` from every subscriber's
// Filter. Used by the lifecycle-create rollback (undo the optimistic expand when
// the seed batch is rejected) and by lifecycle-delete (drop a deleted workspace
// from long-lived filters), so a stray filter entry can't point at a workspace
// that no longer exists in `m.state`.
//
// This is the ONE filter-mutator that takes only the SubscriberController lock
// (via MutateEach) and NOT aclTransitionMu, unlike ExpandSubscribersForWorkspace
// and CommitWorkspaceAccessTransition. That is safe because it only ever DELETES a
// single key: MutateEach serializes the map writes themselves, and a concurrent
// CommitWorkspaceAccessTransition's classify-then-apply tolerates exactly this --
// applyWorkspaceVisibilityLocked only sets or deletes the same single workspaceID
// key and its isAllowed verdict is a pure function of immutable subscriber fields,
// so an interleaved single-key delete here cannot corrupt its outcome (see the
// classify/apply comment there). Note the safety is NOT the service layer's
// per-workspace sharingLocks: the outbox drain processes ALL of the org's pending
// create/delete rows and CreateWorkspace/RenameWorkspace take no sharingLock, so a
// contract for workspace V can run under an UNRELATED workspace's lock and is not
// guaranteed a disjoint key. (To make the serialization uniform rather than resting
// on that single-key argument, this could route through aclTransitionMu like the
// other two; callers hold no manager lock here, so aclTransitionMu -> subscribers
// preserves the documented lock order.)
func (m *Manager) contractSubscribersForWorkspace(workspaceID string) {
	if workspaceID == "" {
		return
	}
	m.subscribers.MutateEach(func(sub *Subscriber) {
		if sub.Filter.WorkspaceIDs == nil {
			return
		}
		delete(sub.Filter.WorkspaceIDs, workspaceID)
	})
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
