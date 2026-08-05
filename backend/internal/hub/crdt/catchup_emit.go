package crdt

import (
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// catchUpSink receives one batch's catch-up frames in wire order
// (materialized → batch → removed). Both the live broadcast path and the
// resume path adapt this sink so a third catch-up path cannot reintroduce
// #357-style order/filter drift.
//
// Payloads arrive as THUNKS, and the EntityRef travels alongside, because the
// two sinks have opposite cost profiles. The resume sink needs every payload
// (it wraps them into the delta), while the live sink memoizes its wire frames
// — per visibility mask for the batch frame, per ref for the transition frames
// — and shares them across every subscriber in the fan-out. Passing built
// payloads made the planner do that work once PER SUBSCRIBER only for the live
// sink to discard it, which silently undid the memoization `batchEventCache`
// exists for: the O(ops) stable-visibility filter went from once-per-mask to
// once-per-subscriber, and each becoming-hidden ref allocated a fresh
// EntityRemoved per subscriber. A thunk keeps ONE definition of each frame's
// content here (so the two paths cannot drift) while letting the live sink skip
// building what it already has cached.
//
// Borrow-only: a thunk's result MUST NOT be mutated — it may be, or may become,
// a pointer aliased into a cross-subscriber cache.
type catchUpSink interface {
	// Materialized is called for a ref that became visible. em builds the
	// payload; the live sink never calls it, since its wrapper for the ref is
	// memoized across the whole fan-out.
	Materialized(ref EntityRef, em func() *leapmuxv1.EntityMaterialized)
	// Batch is called once per batch with a thunk computing this subscriber's
	// stable-visibility op subset. A nil result means "no visible ops" and the
	// sink must emit nothing. The live sink calls it only on a memo miss (its
	// cache is keyed by visibility mask, not by subscriber).
	Batch(visible func() *leapmuxv1.OpBatch)
	// Removed is called for a ref that became hidden.
	Removed(ref EntityRef, er func() *leapmuxv1.EntityRemoved)
	// End closes the batch's sequence. Called exactly once per batch, after
	// every other frame, INCLUDING when the subscriber saw none of them -- it
	// is the only point at which the client may advance its resume watermark,
	// so skipping it would strand the cursor. See the BatchEnd proto doc.
	End(atHLC *leapmuxv1.HLC)
}

// catchUpBatch names the four inputs that must all describe ONE committed
// batch: its kind-ordered affected refs, the batch itself, the per-entity
// workspace transitions recorded for it, and the HLC every frame it produces is
// stamped with.
//
// A struct rather than four positionals because nothing in a positional call
// site said they belonged together, and a call that paired one batch's refs or
// transitions with another's ops would not fail to compile -- it would classify
// every entity against the wrong batch's visibility and ship a silently wrong
// frame stream. Both callers already draw all four from a single value (the live
// path from one batchFanout, the resume path from one ResumeBatch), so the type
// only names a grouping they already have. The same reasoning already produced
// resumeRequest (manager_subscribe.go) one layer up and service.journalScan two.
//
// refs is the kind-ordered affected-refs slice; the live path passes the
// batchFanout's precomputed `refs` (one sort per broadcast) and the resume path
// computes it once per tail batch, so neither re-sorts per subscriber.
type catchUpBatch struct {
	refs        []affectedRef
	batch       *leapmuxv1.OpBatch
	transitions map[EntityRef]EntityWorkspaceTransition
	atHLC       *leapmuxv1.HLC
}

// emitCatchUpFrames is the SINGLE per-batch frame planner for both catch-up
// paths. It applies the shared visibility predicates (visibilityFor /
// opVisibleForSubscriber / isMaterializedTransition / isRemovedTransition)
// and emits frames in the wire-contract order batchFanout.planFor and
// buildResumeDelta must never diverge on.
//
// materializedFor supplies the EntityMaterialized payload for a becoming-
// visible ref (live: clone from m.state; resume: persisted batch-era snapshot).
// Returning nil skips that frame (tombstoned / missing / unsupported kind).
// materializedFor's return value is borrow-only and is never mutated: when its
// AtHlc is nil the backfill builds a COPY rather than stamping in place, because
// the live supplier hands back a pointer aliased into a cross-subscriber memo
// cache. Both suppliers pre-stamp AtHlc today (live: the cache; resume: a fresh
// struct per ref), so the backfill is defence-in-depth.
func emitCatchUpFrames(
	cb catchUpBatch,
	scope visibilityScope,
	materializedFor func(EntityRef) *leapmuxv1.EntityMaterialized,
	sink catchUpSink,
) {
	// 1. EntityMaterialized FIRST, kind-ordered (nodes before tabs).
	for _, a := range cb.refs {
		if !isMaterializedTransition(visibilityFor(scope, a.ref, a.trans)) {
			continue
		}
		sink.Materialized(a.ref, func() *leapmuxv1.EntityMaterialized {
			em := materializedFor(a.ref)
			if em == nil {
				return nil
			}
			if em.GetAtHlc() == nil {
				// Copy rather than stamp in place. On the live path this
				// pointer is the inner proto of a MarshaledEvent memoized
				// across every subscriber in the fan-out, whose Bytes() is
				// sync.Once-cached — a write here would mutate shared state
				// AFTER some subscribers had already marshaled, and would race
				// their writer goroutines. (Both suppliers pre-stamp AtHlc
				// today, so this is defence-in-depth for a future third
				// supplier, which is exactly the case an in-place write would
				// break.)
				return &leapmuxv1.EntityMaterialized{AtHlc: cb.atHLC, Entity: em.GetEntity()}
			}
			return em
		})
	}

	// 2. The batch frame (stable-visibility ops).
	sink.Batch(func() *leapmuxv1.OpBatch {
		return filterVisibleOps(cb, scope)
	})

	// 3. EntityRemoved LAST (pending-drop / echo-splice ordering).
	for _, a := range cb.refs {
		if !isRemovedTransition(visibilityFor(scope, a.ref, a.trans)) {
			continue
		}
		sink.Removed(a.ref, func() *leapmuxv1.EntityRemoved {
			evt := buildEntityRemovedEvent(a.ref, cb.atHLC)
			if evt == nil {
				return nil
			}
			return evt.GetEntityRemoved()
		})
	}

	// 4. Close the sequence. Unconditional — this is the client's ONLY
	// watermark-advance point, so a batch that emitted nothing for this
	// subscriber must still end, or the cursor never moves past it.
	sink.End(cb.atHLC)
}

// filterVisibleOps returns the OpBatch subset a subscriber with `filter` should
// see as the stable-visibility batch frame, or nil when none are visible.
// The SINGLE implementation of the stable-visibility op-subset rule: both
// catch-up paths reach it through emitCatchUpFrames, and the live path's
// mask-keyed cache wraps its result rather than re-deriving one.
//
// When every op is visible, the ORIGINAL batch pointer is returned (no copy), so
// the result ALIASES the caller's batch on both paths. MarshaledEvent marshals
// lazily (sync.Once, on whichever writer goroutine touches it first), so wrapping
// the alias in a fresh event buys no isolation: what actually makes the broadcast
// path safe is that nothing mutates the committed batch after
// Manager.commit hands it to broadcastBatch. Any caller that gains a
// post-broadcast writer -- a pooled/reused OpBatch, an op-list trim, a
// post-commit re-stamp -- must clone here first, on BOTH paths.
func filterVisibleOps(cb catchUpBatch, scope visibilityScope) *leapmuxv1.OpBatch {
	var visibleOps []*leapmuxv1.CrdtOp
	for _, op := range cb.batch.GetOps() {
		ref := OpTarget(op)
		trans := cb.transitions[ref]
		if !opVisibleForSubscriber(ref, visibilityFor(scope, ref, trans)) {
			continue
		}
		if visibleOps == nil {
			visibleOps = make([]*leapmuxv1.CrdtOp, 0, len(cb.batch.GetOps()))
		}
		visibleOps = append(visibleOps, op)
	}
	if len(visibleOps) == 0 {
		return nil
	}
	if len(visibleOps) == len(cb.batch.GetOps()) {
		return cb.batch
	}
	return &leapmuxv1.OpBatch{BatchId: cb.batch.GetBatchId(), Ops: visibleOps}
}

// planCatchUpSink resolves each planner slot to the broadcast fanout's shared
// MarshaledEvent for that frame, MARSHALS it, and appends it to the plan being
// recorded for one subscriber (see batchPlans).
//
// It is the ONLY live sink, and that is deliberate. broadcastBatch runs the
// planner once per subscriber, off the projection lock, and the pass under the
// lock REPLAYS the recorded plan rather than planning again -- so sending is no
// longer a sink at all (see batchFanout.sendTo). A second sink resolving "which
// wire frame does this planner slot produce" alongside this one would be a
// second source of truth for exactly the sequence being replayed: a frame
// recorded off one memo and sent off another is a divergence no test of the send
// path could see, because both would look correct in isolation.
//
// It exists so broadcastBatch can pay for the marshals before it takes the
// projection lock. The subscriber queue charges a frame against the shared byte
// budget on the way in, and asking its size is what forces the lazy marshal --
// so with the fan-out inside the lock, a multi-megabyte batch frame was
// serialized while holding the lock that gates every SubmitOps, presence update
// and projection read for that user.
//
// CONTRACT: the batch frame is memoized per visibility MASK, so the thunk that
// builds it may come from a different subscriber than the one being planned for.
// That is sound only because two subscribers sharing a mask have identical
// IsAllowed verdicts over the workspaces this batch touches, and therefore an
// identical stable-visibility op subset. subscriberWorkspaceMask keys on
// f.wsKeys (exactly those workspaces) and prewarmBatch disables the cache
// entirely past 64 of them, where the mask would alias.
type planCatchUpSink struct {
	fan *batchFanout
	sub *Subscriber
}

// The thunks are ignored on all three frame methods: every payload this sink
// records is a MarshaledEvent memoized across the whole fan-out (per ref for the
// transition frames, per visibility mask for the batch frame), so it never needs
// the planner to build one. Dropping the thunk unevaluated IS the optimization.

func (s planCatchUpSink) Materialized(ref EntityRef, _ func() *leapmuxv1.EntityMaterialized) {
	s.record(s.fan.materialized(ref))
}

func (s planCatchUpSink) Batch(visible func() *leapmuxv1.OpBatch) {
	s.record(s.fan.batchEventFor(s.sub, visible))
}

func (s planCatchUpSink) Removed(ref EntityRef, _ func() *leapmuxv1.EntityRemoved) {
	s.record(s.fan.removed(ref))
}

func (s planCatchUpSink) End(*leapmuxv1.HLC) {
	// Identical for every subscriber of this batch, so it is built once per
	// broadcast and shared like the other frames.
	s.record(s.fan.batchEnd())
}

// record drops a slot the fanout has no frame for -- a tombstoned or
// unsupported-kind ref, or a batch none of whose ops this subscriber may see --
// and appends the rest to the plan, MARSHALED.
//
// The marshal is the point: a plan holds frames the send pass hands straight to
// Send, and Send's byte charge is what would otherwise force proto.Marshal under
// the projection lock.
func (s planCatchUpSink) record(evt *MarshaledEvent) {
	if evt == nil {
		return
	}
	prepare(evt)
	s.fan.plans.add(evt)
}

// prepare forces the frame's marshal, so whoever sends it next finds it
// memoized. NewMarshaledEvent only WRAPS the proto -- Size/Bytes is where the
// cost is, and pre-warming the wrapper without it would move nothing off the
// lock. Shared with broadcastFrame.get, which pays the same cost for the
// single-frame broadcasts.
func prepare(evt *MarshaledEvent) {
	if evt != nil {
		_ = evt.Size()
	}
}

// resumeCatchUpSink ACCUMULATES a ResumeDelta: the ordered WatchUserEvent frame
// stream (same envelope live Send uses) and the watermark those frames
// advertise. It owns both, so buildResumeDelta constructs exactly ONE per resume
// (above the tail loop) and reads the two fields back when the scan is done,
// rather than threading a pointer-to-slice plus a closure over a sibling local
// through a freshly boxed sink per tail batch.
//
// POINTER RECEIVERS throughout, and the reason is mechanical, not stylistic: a
// value receiver would append into a copy of the struct, so every frame after
// the first would land in a slice header nobody reads and the delta would ship
// short. The interface is satisfied by *resumeCatchUpSink alone, which makes a
// value-typed misuse a compile error rather than a silent truncation.
type resumeCatchUpSink struct {
	frames []*leapmuxv1.WatchUserEvent
	// maxHLC is the max over the frames THIS delta emits -- never a live-state
	// read. See the max_hlc paragraph on SubscribeWithACL for why that
	// distinction is load-bearing. Seeded with the request cursor so a delta
	// that emits nothing still advertises the client's own position.
	maxHLC *leapmuxv1.HLC
}

// advance raises maxHLC to hlc when hlc is newer, cloning rather than aliasing:
// the HLCs handed here are borrowed out of frames whose payloads may alias
// persisted or cross-subscriber protos.
func (s *resumeCatchUpSink) advance(hlc *leapmuxv1.HLC) {
	if hlc != nil && HLCCmp(hlc, s.maxHLC) > 0 {
		s.maxHLC = HLCClone(hlc)
	}
}

// The resume sink is the one that actually needs the payloads, so it evaluates
// every thunk. A nil result means the planner had nothing to emit for that slot
// (tombstoned / unsupported kind / no visible ops) and the frame is skipped.

func (s *resumeCatchUpSink) Materialized(_ EntityRef, build func() *leapmuxv1.EntityMaterialized) {
	em := build()
	if em == nil {
		return
	}
	s.frames = append(s.frames, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{EntityMaterialized: em},
	})
	s.advance(em.GetAtHlc())
}

func (s *resumeCatchUpSink) Batch(visible func() *leapmuxv1.OpBatch) {
	batch := visible()
	if batch == nil {
		return
	}
	s.frames = append(s.frames, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Batch{Batch: batch},
	})
	for _, op := range batch.GetOps() {
		s.advance(op.GetCanonicalHlc())
	}
}

func (s *resumeCatchUpSink) Removed(_ EntityRef, build func() *leapmuxv1.EntityRemoved) {
	er := build()
	if er == nil {
		return
	}
	s.frames = append(s.frames, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_EntityRemoved{EntityRemoved: er},
	})
	s.advance(er.GetAtHlc())
}

func (s *resumeCatchUpSink) End(atHLC *leapmuxv1.HLC) {
	s.frames = append(s.frames, &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_BatchEnd{BatchEnd: &leapmuxv1.BatchEnd{AtHlc: atHLC}},
	})
	s.advance(atHLC)
}
