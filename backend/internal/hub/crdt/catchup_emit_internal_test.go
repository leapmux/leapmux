package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"

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
