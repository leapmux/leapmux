package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestEmitCatchUpFrames_LiveSinkDoesNotBuildPayloadsItDiscards pins the reason
// the sink takes THUNKS rather than built payloads.
//
// The live sink answers every frame from a MarshaledEvent cache shared across
// the whole fan-out. When the planner handed it finished payloads, it built
// them once per subscriber and threw them away — turning the O(ops)
// stable-visibility filter from once-per-visibility-mask into
// once-per-subscriber, and allocating a fresh EntityRemoved per subscriber per
// becoming-hidden ref. That is invisible to a correctness test (the bytes on
// the wire are identical), so it is pinned here by counting thunk evaluations.
func TestEmitCatchUpFrames_LiveSinkDoesNotBuildPayloadsItDiscards(t *testing.T) {
	stable := EntityRef{Kind: EntityKindNode, NodeID: "n1"}
	crossingIn := EntityRef{Kind: EntityKindNode, NodeID: "n2"}
	trans := map[EntityRef]EntityWorkspaceTransition{
		// n1 is stably visible, so its op ships in the batch frame.
		stable: {Pre: "w1", Post: "w1"},
		// n2 crosses INTO view, so it gets a materialized frame instead.
		crossingIn: {Pre: "", Post: "w1"},
	}
	batch := &leapmuxv1.OpBatch{
		BatchId: "b1",
		Ops: []*leapmuxv1.CrdtOp{{
			OpId:         "op1",
			CanonicalHlc: &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "c"},
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "n1",
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
			}},
		}},
	}
	filter := NewSubscriberFilter(map[string]bool{"w1": true})
	atHLC := &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "c"}
	cb := catchUpBatch{
		refs:        orderedAffectedRefs(trans),
		batch:       batch,
		transitions: trans,
		atHLC:       atHLC,
	}

	materializedCalls := 0
	materializedFor := func(EntityRef) *leapmuxv1.EntityMaterialized {
		materializedCalls++
		return &leapmuxv1.EntityMaterialized{AtHlc: atHLC}
	}

	// A sink that never evaluates a thunk, standing in for the live sink's
	// cache-hit path.
	discarding := &countingSink{}
	emitCatchUpFrames(cb, liveScope(filter), materializedFor, discarding)
	assert.Equal(t, 0, materializedCalls,
		"a sink that answers from cache must not make the planner clone live state")
	assert.Equal(t, 1, discarding.batchCalls, "Batch is still invoked exactly once per batch")
	assert.Equal(t, 1, discarding.endCalls,
		"End must close the sequence even for a sink that emitted nothing -- it is the client's only watermark-advance point")

	// The resume sink DOES need the payloads, so its thunks must produce them.
	evaluating := &countingSink{evaluate: true}
	emitCatchUpFrames(cb, liveScope(filter), materializedFor, evaluating)
	assert.Equal(t, 1, materializedCalls,
		"a sink that needs the payload gets it built exactly once")
	assert.NotNil(t, evaluating.lastBatch, "the evaluated batch thunk must yield the visible ops")
	assert.Equal(t, "b1", evaluating.lastBatch.GetBatchId())
}

// TestBatchFanout_PrewarmAndSendSelectTheSameFrames pins the invariant that
// makes the pre-warm safe to run as a SEPARATE pass over the same subscribers.
//
// The two passes must agree exactly. A frame the pre-warm skips but the send
// pass delivers is a proto.Marshal paid under the projection lock -- the lock
// that gates every SubmitOps, presence update and projection read for the user,
// and the entire reason the pre-warm exists. A frame the pre-warm builds but the
// send pass never delivers is a marshal (and, for EntityMaterialized, a deep
// clone of live state) paid for nothing.
//
// batchFanout.planFor is what makes that structural rather than a promise: both
// passes are one call to it, and the second finds the first's RECORDING, so the
// send pass does not classify anything at all for a subscriber the pre-warm
// covered. This asserts both halves -- that the frames agree, and that the
// planner ran once rather than twice -- so a future hand-copy that drifts, or a
// send pass that quietly re-plans, fails here.
func TestBatchFanout_PrewarmAndSendSelectTheSameFrames(t *testing.T) {
	// One ref per classification, so the planner exercises every arm:
	//   n1 stable in w1 -> its op ships inside the batch frame
	//   n2 crosses INTO w1 -> EntityMaterialized
	//   n3 crosses OUT of w1 -> EntityRemoved
	//   n4 crosses into w2, which the subscriber cannot see -> nothing
	stable := EntityRef{Kind: EntityKindNode, NodeID: "n1"}
	crossingIn := EntityRef{Kind: EntityKindNode, NodeID: "n2"}
	crossingOut := EntityRef{Kind: EntityKindNode, NodeID: "n3"}
	otherWorkspace := EntityRef{Kind: EntityKindNode, NodeID: "n4"}
	trans := map[EntityRef]EntityWorkspaceTransition{
		stable:         {Pre: "w1", Post: "w1"},
		crossingIn:     {Pre: "", Post: "w1"},
		crossingOut:    {Pre: "w1", Post: ""},
		otherWorkspace: {Pre: "", Post: "w2"},
	}
	atHLC := &leapmuxv1.HLC{Physical: 7, Logical: 0, ClientId: "c"}
	batch := &leapmuxv1.OpBatch{
		BatchId: "b1",
		Ops: []*leapmuxv1.CrdtOp{{
			OpId:         "op1",
			CanonicalHlc: atHLC,
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: "n1",
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
			}},
		}},
	}
	res := ValidationResult{AffectedEntities: trans}

	// Distinct wrappers per ref, so "was this frame paid for?" is readable off
	// each one -- the memo caches prewarmBatch builds, reduced to a lookup.
	materializedEvents := map[EntityRef]*MarshaledEvent{
		stable:         NewMarshaledEvent(materializedNodeEvent("n1", atHLC)),
		crossingIn:     NewMarshaledEvent(materializedNodeEvent("n2", atHLC)),
		otherWorkspace: NewMarshaledEvent(materializedNodeEvent("n4", atHLC)),
	}
	removedEvents := map[EntityRef]*MarshaledEvent{
		crossingOut: NewMarshaledEvent(buildEntityRemovedEvent(crossingOut, atHLC)),
	}
	// The two suppliers COUNT their calls. Each one is the planner asking "what
	// is this ref's frame?", which is work only a planner run does -- so the
	// counters read out how many times the planner classified this subscriber,
	// without timing anything. In prewarmBatch these closures are the memoized
	// materialized/removed caches; here they are the same shape, reduced to a
	// lookup.
	var materializedLookups, removedLookups int
	newFanout := func() *batchFanout {
		return &batchFanout{
			batch:           batch,
			res:             res,
			wsKeys:          batchWorkspaceKeys(batch, res),
			refs:            orderedAffectedRefs(trans),
			atHLC:           atHLC,
			batchEventCache: map[uint64]*MarshaledEvent{},
			materialized: func(ref EntityRef) *MarshaledEvent {
				materializedLookups++
				return materializedEvents[ref]
			},
			removed: func(ref EntityRef) *MarshaledEvent {
				removedLookups++
				return removedEvents[ref]
			},
		}
	}

	var sent []*MarshaledEvent
	sub := &Subscriber{
		Filter: NewSubscriberFilter(map[string]bool{"w1": true}),
		Send: func(evt *MarshaledEvent) error {
			sent = append(sent, evt)
			return nil
		},
	}

	fan := newFanout()
	fan.prewarmFor(sub)
	require.Empty(t, sent, "the pre-warm pass builds frames; sending is the other pass's job")
	assert.True(t, materializedEvents[crossingIn].AlreadyMarshaledForTest(),
		"the entity entering view is delivered, so its frame must be paid for off the lock")
	assert.True(t, removedEvents[crossingOut].AlreadyMarshaledForTest(),
		"so must the entity leaving it")
	assert.False(t, materializedEvents[stable].AlreadyMarshaledForTest(),
		"a stably-visible entity ships as a raw op in the batch frame, so cloning and marshaling "+
			"a materialized record for it is work the send pass would throw away")
	assert.False(t, materializedEvents[otherWorkspace].AlreadyMarshaledForTest(),
		"and an entity entering a workspace this subscriber cannot see is not its frame at all")
	require.Equal(t, 1, materializedLookups, "exactly one ref enters this subscriber's view")
	require.Equal(t, 1, removedLookups, "and exactly one leaves it")

	fan.sendTo(sub)
	require.NotEmpty(t, sent, "the send pass must deliver this subscriber's frames")
	assert.Equal(t, 1, materializedLookups,
		"the send pass must REPLAY the plan the pre-warm recorded, not classify this subscriber a "+
			"second time -- re-running the planner under the projection lock is the cost the "+
			"recording exists to remove")
	assert.Equal(t, 1, removedLookups, "...for the becoming-hidden refs too")
	for i, evt := range sent {
		assert.True(t, evt.AlreadyMarshaledForTest(),
			"frame %d of %d reached Send unmarshaled: the pre-warm and the send pass disagree "+
				"about which frames this subscriber gets, so its marshal lands under the projection lock",
			i, len(sent))
	}
	// Wire order is the contract emitCatchUpFrames owns; a replay must preserve
	// it exactly, so pin the whole sequence rather than only its marshaled-ness.
	if assert.Len(t, sent, 4, "materialized -> batch -> removed -> end") {
		assert.Same(t, materializedEvents[crossingIn], sent[0],
			"and each frame must be the SHARED cross-subscriber wrapper, not a per-subscriber copy")
		assert.NotNil(t, sent[1].Event.GetBatch())
		assert.Same(t, removedEvents[crossingOut], sent[2])
		assert.Same(t, fan.batchEnd(), sent[3])
	}

	// The suppression gate is part of that agreement: a batch the resume delta
	// owns is not sent, so it must not be marshaled either. The recording must
	// carry that verdict rather than lose it -- an EMPTY plan means "send
	// nothing", and must stay distinguishable from "no plan yet", which would
	// re-plan the subscriber and resurrect its frames under the lock.
	newSuppressed := func() (*batchFanout, map[EntityRef]*MarshaledEvent) {
		fresh := map[EntityRef]*MarshaledEvent{
			crossingIn: NewMarshaledEvent(materializedNodeEvent("n2", atHLC)),
		}
		fan := newFanout()
		fan.materialized = func(ref EntityRef) *MarshaledEvent { return fresh[ref] }
		fan.removed = func(EntityRef) *MarshaledEvent { return nil }
		return fan, fresh
	}
	newSuppressedSub := func(sends *int) *Subscriber {
		return &Subscriber{
			Filter:                NewSubscriberFilter(map[string]bool{"w1": true}),
			resumeSuppressThrough: atHLC,
			Send: func(*MarshaledEvent) error {
				*sends++
				return nil
			},
		}
	}

	t.Run("a suppressed subscriber is skipped by both passes", func(t *testing.T) {
		suppressedFan, fresh := newSuppressed()
		var suppressedSends int
		suppressed := newSuppressedSub(&suppressedSends)

		suppressedFan.prewarmFor(suppressed)
		assert.False(t, fresh[crossingIn].AlreadyMarshaledForTest(),
			"a batch the ResumeDelta will deliver must not be marshaled by the live pre-warm")
		suppressedFan.sendTo(suppressed)
		assert.Zero(t, suppressedSends, "...nor sent by the live pass")
	})

	t.Run("a suppressed subscriber the pre-warm never saw is skipped too", func(t *testing.T) {
		// The fallback arm of the same verdict: suppression is decided by the
		// planner, so a subscriber with no recorded plan must reach the same
		// answer rather than depending on the recording to enforce it.
		suppressedFan, fresh := newSuppressed()
		var suppressedSends int

		suppressedFan.sendTo(newSuppressedSub(&suppressedSends))
		assert.Zero(t, suppressedSends,
			"a registration that landed during the pre-warm is still subject to its own suppress gate")
		assert.False(t, fresh[crossingIn].AlreadyMarshaledForTest())
	})
}

// materializedNodeEvent builds a minimal EntityMaterialized frame for a node,
// standing in for the deep clone of live state broadcastBatch's memo produces.
func materializedNodeEvent(nodeID string, atHLC *leapmuxv1.HLC) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_EntityMaterialized{
			EntityMaterialized: &leapmuxv1.EntityMaterialized{
				AtHlc:  atHLC,
				Entity: &leapmuxv1.EntityMaterialized_Node{Node: &leapmuxv1.NodeRecord{NodeId: nodeID}},
			},
		},
	}
}

type countingSink struct {
	evaluate         bool
	batchCalls       int
	materializedCall int
	endCalls         int
	lastBatch        *leapmuxv1.OpBatch
}

func (s *countingSink) End(*leapmuxv1.HLC) { s.endCalls++ }

func (s *countingSink) Materialized(_ EntityRef, em func() *leapmuxv1.EntityMaterialized) {
	s.materializedCall++
	if s.evaluate {
		em()
	}
}

func (s *countingSink) Batch(visible func() *leapmuxv1.OpBatch) {
	s.batchCalls++
	if s.evaluate {
		s.lastBatch = visible()
	}
}

func (s *countingSink) Removed(_ EntityRef, er func() *leapmuxv1.EntityRemoved) {
	if s.evaluate {
		er()
	}
}

// TestFilterVisibleOps_AliasesTheCallerOnlyWhenNothingIsFiltered pins the
// ALIASING as deliberate, so the precondition it depends on is executable
// rather than only prose.
//
// The deleted batchVisibleOpsEvent always allocated a fresh OpBatch, which made
// "the broadcast frame never shares storage with the committed batch"
// structurally true. Returning the caller's pointer on the all-visible path --
// overwhelmingly the common one -- trades that guarantee for the allocation,
// and the guarantee was load-bearing only against a POST-COMMIT WRITER: the
// shared MarshaledEvent marshals lazily under sync.Once, on a WS writer
// goroutine, after broadcastBatch has returned.
//
// No such writer exists today (nothing after Manager.commit's broadcastBatch
// call mutates the batch; it only reads GetOps to clone op ids and HLCs), which
// is why the alias is safe and why this is a test rather than a restored copy:
// copying on every broadcast for every subscriber is real cost on the path #267
// exists to make cheap. What this fails on is the change that makes it UNSAFE
// -- anyone who starts pooling, trimming or re-stamping committed batches will
// land here, next to the doc naming exactly what they must do.
func TestFilterVisibleOps_AliasesTheCallerOnlyWhenNothingIsFiltered(t *testing.T) {
	hidden := EntityRef{Kind: EntityKindNode, NodeID: "n2"}
	// nil WorkspaceIDs means "allow all", so visibility turns purely on the
	// transitions map below.
	scope := liveScope(SubscriberFilter{})
	nodeOp := func(id string) *leapmuxv1.CrdtOp {
		return &leapmuxv1.CrdtOp{
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: id,
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
			}},
		}
	}

	t.Run("all ops visible returns the caller's batch pointer", func(t *testing.T) {
		batch := &leapmuxv1.OpBatch{
			BatchId: "b1",
			Ops:     []*leapmuxv1.CrdtOp{nodeOp("n1")},
		}
		trans := map[EntityRef]EntityWorkspaceTransition{
			{Kind: EntityKindNode, NodeID: "n1"}: {Pre: "ws1", Post: "ws1"},
		}
		got := filterVisibleOps(catchUpBatch{batch: batch, transitions: trans}, scope)
		assert.Same(t, batch, got,
			"the all-visible path aliases by design; see the doc on filterVisibleOps before changing it")
	})

	t.Run("a filtered batch is a fresh value that shares no slice with the caller", func(t *testing.T) {
		batch := &leapmuxv1.OpBatch{
			BatchId: "b1",
			Ops:     []*leapmuxv1.CrdtOp{nodeOp("n1"), nodeOp("n2")},
		}
		trans := map[EntityRef]EntityWorkspaceTransition{
			{Kind: EntityKindNode, NodeID: "n1"}: {Pre: "ws1", Post: "ws1"},
			// Empty Pre/Post is invisible: IsAllowed("") is always false.
			hidden: {},
		}
		got := filterVisibleOps(catchUpBatch{batch: batch, transitions: trans}, scope)
		if assert.NotNil(t, got) {
			assert.NotSame(t, batch, got)
			assert.Len(t, got.GetOps(), 1)
			assert.Len(t, batch.GetOps(), 2, "the caller's batch must be left intact")
		}
	})
}
