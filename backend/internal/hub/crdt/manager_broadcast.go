package crdt

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

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
// (see batchFanout.sendTo) rather than prebuilt into a per-subscriber
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
	//
	// buildEntityRemovedEvent returns nil for an EntityKind with no Removed arm
	// (notably EntityKindWorkspaceRoot — the proto EntityRemoved oneof has no
	// workspace variant). A TombstoneWorkspace op no longer reaches this path:
	// IsTombstoneOp includes it, so validate.go's post-pin records a stable
	// {Pre: wsID, Post: wsID} transition (no OUT) and the op is delivered as
	// the raw op via the Batch frame's EntityKindWorkspaceRoot arm (preVisible
	// || postVisible). The `event != nil` guard mirrors materialized() below
	// and is retained as defense-in-depth: if a future change re-introduces an
	// EntityKindWorkspaceRoot OUT transition, the guard makes buildEntityRemovedEvent's
	// nil a nil wrapper the caller skips instead of shipping a non-nil
	// MarshaledEvent around a nil proto that marshals to a 0-byte WS frame
	// (proto.Marshal(nil) → []byte{}).
	removedCache := map[EntityRef]*MarshaledEvent{}
	removed := func(ref EntityRef) *MarshaledEvent {
		if evt, built := removedCache[ref]; built {
			return evt
		}
		var evt *MarshaledEvent
		if res.AffectedEntities[ref].Pre != "" {
			if event := buildEntityRemovedEvent(ref, atHLC); event != nil {
				evt = NewMarshaledEvent(event)
			}
		}
		removedCache[ref] = evt
		return evt
	}

	// EntityMaterialized events deep-clone live state and are sent only to a
	// subscriber that transitions INTO visibility (!pre && post) -- rare for the
	// common in-place edit. Build them lazily and memoize (nil results included),
	// so the clone happens at most once per ref and only when a subscriber needs
	// it. Re-taking m.mu.RLock per first build is safe: the manager goroutine is
	// the sole writer of m.state, and its commit swap (m.state = working) takes
	// m.mu.Lock, so each RLock read sees an un-torn map. (Workspace lifecycle
	// create/delete now flow through SubmitInternal as
	// SetWorkspaceRegisterOp / TombstoneWorkspaceOp on the manager goroutine,
	// so there is no cross-goroutine writer of m.state.)
	// buildEntityMaterializedEvent requires that read lock held.
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

	// Ordering + atHLC are subscriber-independent, so pay for them once. atHLC
	// (the batch's last-op canonical HLC) is hoisted onto the fanout so sendTo /
	// emitCatchUpFrames read f.atHLC instead of recomputing lastBatchHLC once
	// per subscriber.
	fan := &batchFanout{
		batch:           batch,
		res:             res,
		wsKeys:          wsKeys,
		refs:            orderedAffectedRefs(res.AffectedEntities),
		atHLC:           atHLC,
		batchEventCache: batchEventCache,
		materialized:    materialized,
		removed:         removed,
	}
	for _, sub := range subs {
		fan.sendTo(sub)
	}
}

// affectedRef pairs a ref with its transition so the per-subscriber
// materialized/removed passes never re-look-up AffectedEntities.
type affectedRef struct {
	ref   EntityRef
	trans EntityWorkspaceTransition
}

// materializeRank orders an EntityKind by what a client's projection needs
// FIRST. A tab's tile_id names a node, and a floating window's root_node_id
// names one too, so nodes lead and tabs trail. Within a kind the order is
// irrelevant: every node is installed before any tab reads one.
func materializeRank(k EntityKind) int {
	switch k {
	case EntityKindNode:
		return 0
	case EntityKindFloatingWindow:
		return 1
	default:
		return 2
	}
}

// orderedAffectedRefs flattens AffectedEntities into a deterministic slice.
//
// Go map iteration is randomized, and frame order is a WIRE CONTRACT here (see
// batchFanout.sendTo), so the randomization has to be removed rather
// than merely tolerated: a cross-workspace move materializes both a node and
// the tab anchored to it, and delivering the tab first reopens exactly the
// window this ordering exists to close.
func orderedAffectedRefs(affected map[EntityRef]EntityWorkspaceTransition) []affectedRef {
	out := make([]affectedRef, 0, len(affected))
	for ref, trans := range affected {
		out = append(out, affectedRef{ref: ref, trans: trans})
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := materializeRank(out[i].ref.Kind), materializeRank(out[j].ref.Kind)
		if ri != rj {
			return ri < rj
		}
		a, b := out[i].ref, out[j].ref
		if a.NodeID != b.NodeID {
			return a.NodeID < b.NodeID
		}
		if a.WindowID != b.WindowID {
			return a.WindowID < b.WindowID
		}
		if a.TabID != b.TabID {
			return a.TabID < b.TabID
		}
		if a.TabType != b.TabType {
			return a.TabType < b.TabType
		}
		// WorkspaceID last, because it is the ONLY identity a WorkspaceRoot ref
		// carries (see EntityRef.HasIdentity): without it two such refs compare
		// equal and sort.Slice -- which is not stable -- orders them arbitrarily
		// per call. No frame is emitted for that kind today, so nothing observes
		// the randomness yet; this keeps the comparator's order total by
		// construction rather than by the accident of which kinds have arms.
		return a.WorkspaceID < b.WorkspaceID
	})
	return out
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

// subscriberVisibility carries the pre/post-batch visibility flags an
// entity has for a given subscriber filter.
type subscriberVisibility struct {
	preVisible  bool
	postVisible bool
}

// visibilityFor is the SINGLE constructor for a subscriberVisibility verdict
// from a transition + filter. Both catch-up paths (broadcast's sendTo and
// resume's buildResumeDelta) call it, so the "how pre/post map to visibility"
// rule lives in one place and cannot drift between the two paths.
//
// ref is needed because the PRE side is a per-ENTITY question on the resume
// path ("does the client hold a record for this entity?"), not a per-workspace
// one. See visibilityScope.
func visibilityFor(scope visibilityScope, ref EntityRef, trans EntityWorkspaceTransition) subscriberVisibility {
	return subscriberVisibility{
		preVisible:  scope.preVisible(ref, trans.Pre),
		postVisible: scope.postAllowed(trans.Post),
	}
}

// visibilityScope answers "could this subscriber see workspace X" for the two
// SIDES of a transition, which are not always the same question.
//
// A live broadcast evaluates a batch against the ACL as it stood when that batch
// committed, so both sides use one filter. A resume replays gap batches against
// the ACL as it stands NOW, which silently loses an entire class of transition:
// a workspace DELETED during the gap is gone from the current allowed set, so
// its tombstone batch -- pinned {Pre: wsID, Post: wsID} -- reads as invisible on
// both sides. No ops ship, no EntityRemoved is emitted, and the client keeps
// that workspace's nodes, tabs and windows forever (and now writes them into its
// checkpoint). The live path escapes this only because
// contractSubscribersForWorkspace runs AFTER the tombstone broadcast.
//
// `departed` restores the pre-side to its cursor-era answer. It is sound because
// a workspace can leave an owner's set by exactly one route -- deletion (access
// is owner-only; there is no sharing to revoke) -- so "was visible, is not now"
// and "was deleted" are the same set.
//
// But a workspace-level answer is only correct for an entity's FIRST sighting in
// the tail. The real predicate the pre side needs is per-ENTITY -- "does the
// client hold a record for this ref right now?" -- and the two diverge as soon
// as an entity is born inside a departed workspace DURING the gap: its creation
// emits nothing (the workspace is not visible on the post side), yet a later
// escape to a visible workspace reads {Pre: departed} as visible and ships raw
// ops onto a record the client never received. `held` closes that by tracking
// the answer entity by entity as the replay advances.
//
// The seed stays workspace-level and stays correct, because the tail is complete
// from the cursor forward: at an entity's first sighting it has not moved since
// the cursor, so its Pre IS its cursor-era workspace -- and a workspace that
// held an entity at cursor time necessarily existed then, so the seed can never
// be asked about a workspace born inside the gap.
type visibilityScope struct {
	filter SubscriberFilter
	// departed widens the PRE side only, and only for a first sighting. Nil on
	// the live path, where the batch is already being evaluated against its own
	// era's ACL.
	departed map[string]bool
	// held is the per-entity pre-side answer, updated by observe() after each
	// replayed batch. Nil on the live path, which has no gap to reconstruct.
	held map[EntityRef]bool
}

// liveScope is the broadcast path's scope: one filter, both sides, no replay
// state.
func liveScope(filter SubscriberFilter) visibilityScope {
	return visibilityScope{filter: filter}
}

// resumeScope is the replay path's scope. departed may be nil (nothing left the
// ACL during the gap); held always starts empty and fills in as observe() runs.
func resumeScope(filter SubscriberFilter, departed map[string]bool) visibilityScope {
	return visibilityScope{filter: filter, departed: departed, held: map[EntityRef]bool{}}
}

// preVisible answers the PRE side for one entity: on the live path a plain
// filter test, on the replay path the client's tracked holding, falling back to
// the cursor-era workspace answer the first time a ref is seen.
func (s visibilityScope) preVisible(ref EntityRef, pre string) bool {
	if s.held != nil {
		if h, seen := s.held[ref]; seen {
			return h
		}
	}
	return s.filter.IsAllowed(pre) || (pre != "" && s.departed[pre])
}

func (s visibilityScope) postAllowed(ws string) bool {
	return s.filter.IsAllowed(ws)
}

// observe records what the subscriber holds AFTER a replayed batch: exactly the
// refs whose post side was visible. Call once per batch, after emitCatchUpFrames
// has classified it -- mutating mid-batch would make the materialized pass and
// the batch-frame pass disagree about the same ref. No-op on the live path.
//
// Pointer receiver because this method MUTATES the scope. It happens to work on
// a value receiver today only because `held` is a map, so the copy aliases the
// same backing store -- an accident that would silently swallow the write the
// moment any non-map field is added here.
func (s *visibilityScope) observe(refs []affectedRef) {
	if s.held == nil {
		return
	}
	for _, a := range refs {
		s.held[a.ref] = s.postAllowed(a.trans.Post)
	}
}

// opVisibleForSubscriber reports whether the raw op on `ref` should be
// delivered as part of the (filtered) batch frame for a subscriber with the
// given pre/post visibility verdict. WorkspaceRoot is special-cased (send if
// EITHER side visible — the register lives on WorkspaceContentsRecord, which has
// no EntityMaterialized arm); every other entity is delivered as a raw op only
// when BOTH sides are visible (stable visibility), so becoming-visible and
// becoming-hidden transitions are handled by EntityMaterialized / EntityRemoved
// instead of raw move ops that would leak pre-state.
//
// This is the SINGLE source of the stable-visibility rule: both catch-up paths
// reach it through emitCatchUpFrames -> filterVisibleOps, so they cannot drift
// on which ops ship in the batch frame.
func opVisibleForSubscriber(ref EntityRef, v subscriberVisibility) bool {
	if ref.Kind == EntityKindWorkspaceRoot {
		return v.preVisible || v.postVisible
	}
	return v.preVisible && v.postVisible
}

// isMaterializedTransition reports the becoming-visible classification
// (!pre && post): the entity ENTERED the subscriber's allowed set this batch,
// so it is delivered as an EntityMaterialized frame (its full record) rather
// than the raw move op (which would carry pre-state from a hidden workspace).
// Shared by batchFanout.sendTo and buildResumeDelta.
func isMaterializedTransition(v subscriberVisibility) bool {
	return !v.preVisible && v.postVisible
}

// isRemovedTransition reports the becoming-hidden classification (pre && !post):
// the entity LEFT the subscriber's allowed set this batch, so it is delivered
// as an EntityRemoved frame so the client evicts it. Shared by
// batchFanout.sendTo and buildResumeDelta.
func isRemovedTransition(v subscriberVisibility) bool {
	return v.preVisible && !v.postVisible
}

// batchFanout holds everything one batch broadcast keeps constant across
// subscribers, so the per-subscriber send takes only the subscriber. Built once
// per broadcastBatch; every field is read-only to sendTo except batchEventCache,
// which memoizes across subscribers by design and is safe because broadcastBatch
// holds the projection lock for the whole fan-out.
type batchFanout struct {
	batch           *leapmuxv1.OpBatch
	res             ValidationResult
	wsKeys          []string
	refs            []affectedRef
	atHLC           *leapmuxv1.HLC
	batchEventCache map[uint64]*MarshaledEvent
	// Lazily built once per broadcast; filter-independent, so unlike
	// batchEventCache it needs no per-mask key. Guarded by the projection lock
	// broadcastBatch holds for the whole fan-out, same as batchEventCache.
	batchEndEvent *MarshaledEvent
	materialized  func(EntityRef) *MarshaledEvent
	removed       func(EntityRef) *MarshaledEvent
}

// batchEventFor returns this subscriber's batch frame, shared with same-mask
// peers when the cache is enabled.
//
// The nil-cache arm is a SOUNDNESS gate, not an allocation one, and must not be
// "simplified" away: subscriberWorkspaceMask packs bit i per key, and in Go a
// uint64 shift of >= 64 evaluates to 0, so past 64 workspace keys two
// subscribers whose verdicts differ only above bit 63 collapse onto one mask and
// would be handed each other's filtered ops. broadcastBatch leaves the cache nil
// in that case, which this arm keeps honouring by also skipping the mask.
func (f *batchFanout) batchEventFor(sub *Subscriber, visible func() *leapmuxv1.OpBatch) *MarshaledEvent {
	if f.batchEventCache == nil {
		return batchEventFromOps(visible())
	}
	mask := subscriberWorkspaceMask(sub, f.wsKeys)
	evt, built := f.batchEventCache[mask]
	if !built {
		// Only a cache MISS pays for the filter. Two subscribers with the same
		// visibility mask see the same ops by construction, so the second one
		// never runs the O(ops) scan.
		evt = batchEventFromOps(visible())
		f.batchEventCache[mask] = evt
	}
	return evt
}

// batchEnd returns this batch's shared end-of-sequence frame, built once per
// broadcast. It carries only the batch's atHLC, so it is byte-identical for
// every subscriber regardless of filter — unlike the batch frame, it needs no
// mask key.
func (f *batchFanout) batchEnd() *MarshaledEvent {
	if f.batchEndEvent == nil {
		f.batchEndEvent = NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
			Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{AtHlc: f.atHLC}},
		})
	}
	return f.batchEndEvent
}

// batchEventFromOps wraps an already-filtered op subset as a wire frame, or
// returns nil when the subscriber sees none of the batch's ops.
func batchEventFromOps(visible *leapmuxv1.OpBatch) *MarshaledEvent {
	if visible == nil {
		return nil
	}
	return NewMarshaledEvent(&leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Batch{Batch: visible},
	})
}

func (f *batchFanout) sendTo(sub *Subscriber) {
	// FRAME ORDER IS A CONTRACT — owned by emitCatchUpFrames (materialized →
	// batch → removed). This adapter supplies live-state record lookup and
	// fans each frame through the MarshaledEvent memoization caches.
	atHLC := f.atHLC
	// Resume catch-up ownership: batches at or below the register-time
	// high-water ship in the ResumeDelta. Skipping them here prevents
	// dual-delivery when commitState advanced MaxHlc before until was
	// sampled under the projection hold (see SubscribeWithACL).
	if sub.resumeSuppressThrough != nil && atHLC != nil && HLCCmp(atHLC, sub.resumeSuppressThrough) <= 0 {
		return
	}
	emitCatchUpFrames(
		catchUpBatch{
			refs:        f.refs,
			batch:       f.batch,
			transitions: f.res.AffectedEntities,
			atHLC:       atHLC,
		},
		liveScope(sub.Filter),
		f.materializedPayload,
		liveCatchUpSink{fan: f, sub: sub},
	)
}

// materializedPayload unwraps the fanout's memoized EntityMaterialized wrapper
// into the bare payload emitCatchUpFrames' thunk protocol asks for.
//
// A METHOD, not a closure built inside sendTo: it captures nothing beyond the
// receiver, and sendTo runs once per subscriber per batch on the broadcast
// fan-out, so building it there allocated one escaping closure per subscriber
// per batch -- and liveCatchUpSink never calls it, because every payload it
// sends is already a cross-subscriber MarshaledEvent (see the sink's own note).
// The planner still needs SOMETHING here: it is what keeps one definition of
// each frame's content shared by both catch-up paths, which is the drift
// protection catchUpSink's doc exists to explain. Cheap to pass, free to ignore.
//
// Borrow-only: the returned pointer is the inner proto of a memo shared across
// the whole fan-out, so callers must not mutate it.
func (f *batchFanout) materializedPayload(ref EntityRef) *leapmuxv1.EntityMaterialized {
	evt := f.materialized(ref)
	if evt == nil || evt.Event == nil {
		return nil
	}
	return evt.Event.GetEntityMaterialized()
}

// buildEntityMaterializedEvent constructs the EntityMaterialized event
// for a single ref against `state`. Caller MUST hold m.mu (read lock is
// enough). Returns nil when the ref doesn't resolve to a live,
// non-tombstoned record (same eligibility as the commit-time snapshot
// encoder -- see liveRecordSnapshot).
func buildEntityMaterializedEvent(state *leapmuxv1.UserCrdtState, ref EntityRef, atHLC *leapmuxv1.HLC) *leapmuxv1.WatchUserEvent {
	em := liveRecordSnapshot(state, ref)
	if em == nil {
		return nil
	}
	em.AtHlc = atHLC
	return &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{EntityMaterialized: em},
	}
}

// buildEntityRemovedEvent constructs the EntityRemoved wrapper for a
// ref. Unlike Materialized, this is pure metadata (no state lookup).
// Returns nil for an EntityKind the EntityRemoved oneof has no variant
// for — EntityKindUnknown, and EntityKindWorkspaceRoot (workspace
// membership has no per-entity Removed event; a TombstoneWorkspace op's
// visibility transition is delivered as the raw op in the Batch frame).
// Callers must nil-guard the result before wrapping (see removed()).
func buildEntityRemovedEvent(ref EntityRef, atHLC *leapmuxv1.HLC) *leapmuxv1.WatchUserEvent {
	switch ref.Kind {
	case EntityKindTab:
		return &leapmuxv1.WatchUserEvent{
			Event: &leapmuxv1.WatchUserEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_Tab{Tab: &leapmuxv1.TabIdent{TabType: ref.TabType, TabId: ref.TabID}},
				},
			},
		}
	case EntityKindFloatingWindow:
		return &leapmuxv1.WatchUserEvent{
			Event: &leapmuxv1.WatchUserEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_WindowId{WindowId: ref.WindowID},
				},
			},
		}
	case EntityKindNode:
		return &leapmuxv1.WatchUserEvent{
			Event: &leapmuxv1.WatchUserEvent_EntityRemoved{
				EntityRemoved: &leapmuxv1.EntityRemoved{
					AtHlc:  atHLC,
					Entity: &leapmuxv1.EntityRemoved_NodeId{NodeId: ref.NodeID},
				},
			},
		}
	default:
		// EntityKindUnknown and EntityKindWorkspaceRoot have no EntityRemoved
		// variant, per the doc above; callers nil-guard the result.
		return nil
	}
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
	m.broadcastTo(workspaceID, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
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
func (m *Manager) broadcastTo(workspaceID string, evt *leapmuxv1.WatchUserEvent) {
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
// RequestedWorkspaceIDs bound. Idempotent — calling on an already-allowed
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
		readable, err := accessWorkspaceForUsers(ctx, m.auth, workspaceID, userIDs)
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
		sub.Filter = sub.Filter.WithWorkspace(workspaceID)
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
		sub.Filter = sub.Filter.WithoutWorkspace(workspaceID)
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
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Created{
			Created: &leapmuxv1.WorkspaceCreated{WorkspaceId: workspaceID, Title: title, RootNodeId: rootNodeID},
		},
	})
}

func (m *Manager) BroadcastWorkspaceRenamed(workspaceID, title string) {
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Renamed{
			Renamed: &leapmuxv1.WorkspaceRenamed{WorkspaceId: workspaceID, Title: title},
		},
	})
}

func (m *Manager) BroadcastWorkspaceDeleted(workspaceID string, workerIDs []string) {
	m.broadcastWorkspaceLifecycle(workspaceID, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Deleted{
			Deleted: &leapmuxv1.WorkspaceDeleted{WorkspaceId: workspaceID, WorkerIds: workerIDs},
		},
	})
}

// broadcastWorkspaceLifecycle fans out `evt` to subscribers that admit
// `workspaceID`. Thin wrapper preserved as a name-readable call site
// in the Created/Renamed/Deleted helpers.
func (m *Manager) broadcastWorkspaceLifecycle(workspaceID string, evt *leapmuxv1.WatchUserEvent) {
	m.broadcastTo(workspaceID, evt)
}
