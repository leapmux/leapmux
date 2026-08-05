package crdt

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// planFixture is one committed batch, reduced to what a batchFanout needs.
//
// It touches two workspaces and carries one ref of each classification, so a
// fanout built from it exercises every arm of the planner: a stably-visible ref
// per workspace (raw ops in the batch frame), a ref entering view
// (EntityMaterialized) and a ref leaving it (EntityRemoved).
type planFixture struct {
	batch *leapmuxv1.OpBatch
	res   ValidationResult
	atHLC *leapmuxv1.HLC

	crossingIn  EntityRef
	crossingOut EntityRef
}

func newPlanFixture() planFixture {
	atHLC := &leapmuxv1.HLC{Physical: 42, Logical: 0, ClientId: "c"}
	nodeOp := func(id string) *leapmuxv1.CrdtOp {
		return &leapmuxv1.CrdtOp{
			OpId:         "op-" + id,
			CanonicalHlc: atHLC,
			Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
				NodeId: id,
				Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
			}},
		}
	}
	crossingIn := EntityRef{Kind: EntityKindNode, NodeID: "n3"}
	crossingOut := EntityRef{Kind: EntityKindNode, NodeID: "n4"}
	trans := map[EntityRef]EntityWorkspaceTransition{
		{Kind: EntityKindNode, NodeID: "n1"}: {Pre: "w1", Post: "w1"},
		{Kind: EntityKindNode, NodeID: "n2"}: {Pre: "w2", Post: "w2"},
		crossingIn:                           {Pre: "", Post: "w1"},
		crossingOut:                          {Pre: "w2", Post: ""},
	}
	return planFixture{
		batch: &leapmuxv1.OpBatch{
			BatchId: "b1",
			Ops:     []*leapmuxv1.CrdtOp{nodeOp("n1"), nodeOp("n2")},
		},
		res:         ValidationResult{AffectedEntities: trans},
		atHLC:       atHLC,
		crossingIn:  crossingIn,
		crossingOut: crossingOut,
	}
}

// newFanout builds a fanout over this fixture with FRESH memo caches, so two
// fanouts from the same fixture share no marshaled frame and each one's work is
// readable on its own.
func (f planFixture) newFanout() *batchFanout {
	materializedEvents := map[EntityRef]*MarshaledEvent{
		f.crossingIn: NewMarshaledEvent(materializedNodeEvent(f.crossingIn.NodeID, f.atHLC)),
	}
	removedEvents := map[EntityRef]*MarshaledEvent{
		f.crossingOut: NewMarshaledEvent(buildEntityRemovedEvent(f.crossingOut, f.atHLC)),
	}
	return &batchFanout{
		batch:           f.batch,
		res:             f.res,
		wsKeys:          batchWorkspaceKeys(f.batch, f.res),
		refs:            orderedAffectedRefs(f.res.AffectedEntities),
		atHLC:           f.atHLC,
		batchEventCache: map[uint64]*MarshaledEvent{},
		materialized:    func(ref EntityRef) *MarshaledEvent { return materializedEvents[ref] },
		removed:         func(ref EntityRef) *MarshaledEvent { return removedEvents[ref] },
	}
}

// newManager builds a Manager whose live state can actually serve this fixture's
// batch.
//
// Two things have to hold, and neither is incidental. The becoming-visible ref is
// cloned out of live state, so it has to exist there for a materialized frame to
// be produced at all. And MaxHlc must sit BELOW the batch's HLC: it is the
// generation every registration captures as its suppress gate, so a MaxHlc at or
// above the batch would (correctly) hand these frames to the bootstrap arm and
// nothing would be broadcast.
func (f planFixture) newManager() *Manager {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())
	m.state.MaxHlc = &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "hub"}
	m.state.Nodes[f.crossingIn.NodeID] = &leapmuxv1.NodeRecord{NodeId: f.crossingIn.NodeID}
	return m
}

// recordingSubscriber returns a subscriber for `m` that appends every frame it is
// sent to *sent, in delivery order.
func recordingSubscriber(m *Manager, workspaceIDs map[string]bool, sent *[]*MarshaledEvent) *Subscriber {
	return &Subscriber{
		UserID: m.owner.String(),
		Filter: NewSubscriberFilter(workspaceIDs),
		Send: func(evt *MarshaledEvent) error {
			*sent = append(*sent, evt)
			return nil
		},
	}
}

// sentWireFrames runs one fresh fanout's send pass for a copy of `sub` and
// returns the wire bytes of every frame delivered, in order.
//
// `prewarmed` selects the two paths broadcastBatch actually takes: true is a
// subscriber the pre-warm covered (its plan is replayed under the lock), false
// is one it did not -- a registration that landed while the pre-warm ran, or a
// re-published clone whose filter changed under it, both of which reach the
// locked pass with no recorded plan.
//
// The subscriber is COPIED so each arm gets its own pointer, which is also what
// SubscriberController's republication does.
func (f planFixture) sentWireFrames(t *testing.T, sub Subscriber, prewarmed bool) [][]byte {
	t.Helper()
	frames := [][]byte{}
	sub.Send = func(evt *MarshaledEvent) error {
		wire, err := evt.Bytes()
		require.NoError(t, err)
		frames = append(frames, wire)
		return nil
	}
	fan := f.newFanout()
	if prewarmed {
		fan.prewarmFor(&sub)
	}
	fan.sendTo(&sub)
	return frames
}

// TestBatchFanout_ReplayedPlanMatchesAFreshlyPlannedOne is the equivalence half
// of the plan recording: a replayed plan and a freshly planned one must be the
// SAME frame stream, byte for byte and in the same order.
//
// The recording is only allowed to save the classification work, never to change
// its answer -- so every subscriber shape the planner distinguishes is run both
// ways here. The un-pre-warmed arm is not a hypothetical: it is exactly what a
// subscriber that registered during the pre-warm gets when the locked pass
// reaches it.
func TestBatchFanout_ReplayedPlanMatchesAFreshlyPlannedOne(t *testing.T) {
	fixture := newPlanFixture()

	cases := []struct {
		name       string
		sub        Subscriber
		wantFrames int
	}{{
		// materialized(n3) -> batch(n1, n2) -> removed(n4) -> end
		name:       "an ordinary batch, every touched workspace visible",
		sub:        Subscriber{Filter: NewSubscriberFilter(map[string]bool{"w1": true, "w2": true})},
		wantFrames: 4,
	}, {
		// materialized(n3) -> batch(n1 only) -> end. n4 leaves w2, which this
		// subscriber never saw, so there is nothing for it to evict.
		name:       "a filter that admits only some of the batch's workspaces",
		sub:        Subscriber{Filter: NewSubscriberFilter(map[string]bool{"w1": true})},
		wantFrames: 3,
	}, {
		// No visible ops at all, so only the watermark-advancing end frame.
		name:       "a filter that admits none of them",
		sub:        Subscriber{Filter: NewSubscriberFilter(map[string]bool{})},
		wantFrames: 1,
	}, {
		name: "a suppressed subscriber, whose frames belong to the ResumeDelta",
		sub: Subscriber{
			Filter:                NewSubscriberFilter(map[string]bool{"w1": true, "w2": true}),
			resumeSuppressThrough: fixture.atHLC,
		},
		wantFrames: 0,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			replayed := fixture.sentWireFrames(t, tc.sub, true)
			planned := fixture.sentWireFrames(t, tc.sub, false)

			assert.Len(t, replayed, tc.wantFrames)
			assert.Equal(t, planned, replayed,
				"a recorded plan must deliver exactly what the planner would have produced under "+
					"the lock -- same frames, same order, same bytes")
		})
	}
}

// TestBatchFanout_SendServesASubscriberThePreWarmNeverSaw pins that the memo is
// keyed per subscriber and that a subscriber it does not cover is SERVED, not
// skipped.
//
// Skipping is the failure mode a plan cache invites: a late registration has no
// entry, and treating "no entry" as "nothing to send" would silently drop a
// whole batch for the one subscriber least able to notice -- it has no bootstrap
// frame covering this batch, since its suppress gate sits below it.
func TestBatchFanout_SendServesASubscriberThePreWarmNeverSaw(t *testing.T) {
	fixture := newPlanFixture()
	filter := NewSubscriberFilter(map[string]bool{"w1": true, "w2": true})

	var earlySent, lateSent []*MarshaledEvent
	early := &Subscriber{Filter: filter, Send: func(evt *MarshaledEvent) error {
		earlySent = append(earlySent, evt)
		return nil
	}}
	late := &Subscriber{Filter: filter, Send: func(evt *MarshaledEvent) error {
		lateSent = append(lateSent, evt)
		return nil
	}}

	fan := fixture.newFanout()
	// Only `early` is in the pre-warm's snapshot; `late` registers after it.
	fan.prewarmFor(early)
	fan.sendTo(early)
	fan.sendTo(late)

	require.NotEmpty(t, lateSent,
		"a subscriber the pre-warm never saw must still be planned and served by the locked pass")
	assert.Equal(t, earlySent, lateSent,
		"and served the same frames -- the fan-out's memo caches are shared, so both draw the "+
			"same wrappers whether the plan was recorded or freshly built")
}

// TestBatchFanout_ReplayKeepsTheBatchMarshalOffTheLockWithoutTheMaskCache is the
// case the recording rescues outright rather than merely speeds up.
//
// Past 64 distinct workspaces the mask cache is DISABLED for soundness (see
// TestSubscriberWorkspaceMask_AliasesPast64Keys), so batchEventFor mints a fresh
// wrapper on every call. Before the recording, that made the pre-warm useless
// there: the send pass built a SECOND wrapper for the same subscriber and
// marshaled it under the projection lock, which is the exact multi-megabyte
// serialization the pre-warm exists to hoist. Replaying the recorded plan sends
// the wrapper the pre-warm already paid for.
func TestBatchFanout_ReplayKeepsTheBatchMarshalOffTheLockWithoutTheMaskCache(t *testing.T) {
	fixture := newPlanFixture()
	fan := fixture.newFanout()
	fan.batchEventCache = nil // as prewarmBatch leaves it past 64 workspace keys

	var sent []*MarshaledEvent
	sub := &Subscriber{
		Filter: NewSubscriberFilter(map[string]bool{"w1": true, "w2": true}),
		Send: func(evt *MarshaledEvent) error {
			sent = append(sent, evt)
			return nil
		},
	}

	fan.prewarmFor(sub)
	fan.sendTo(sub)

	require.Len(t, sent, 4, "materialized -> batch -> removed -> end")
	for i, evt := range sent {
		assert.True(t, evt.AlreadyMarshaledForTest(),
			"frame %d of %d reached Send unmarshaled, so the subscriber queue's byte charge will "+
				"serialize it under the projection lock", i, len(sent))
	}
}

// TestPrewarmBatch_PlansNoFanOutWhenNobodyIsSubscribed pins the one place this
// path is deliberately NOT symmetric with broadcastTo: an empty snapshot yields
// no fanout, and that nil is what lets broadcastBatch skip the locked pass
// instead of taking m.projection to serve nobody.
//
// It is a sound answer here and not on the presence path because of the second
// interlock (see prewarmBatch): a registration that lands after the commit
// captured a generation at or above this batch, so suppressedFor drops these ops
// from the live fan-out and its own bootstrap carries them. Presence has neither
// a gate nor a bootstrap arm, which is why fanOutFrame's lock is unconditional
// and this pass is skippable.
func TestPrewarmBatch_PlansNoFanOutWhenNobodyIsSubscribed(t *testing.T) {
	fixture := newPlanFixture()
	m := fixture.newManager()

	assert.Nil(t, m.prewarmBatch(fixture.batch, fixture.res),
		"an empty subscriber snapshot must plan nothing, and the nil is what tells "+
			"broadcastBatch there is no locked pass to run")
}

// TestFanOutBatch_ServesASubscriberPublishedWhileThePreWarmRan drives the split
// broadcastBatch is -- prewarmBatch off the projection lock, fanOutBatch under it
// -- with a subscriber appearing in the snapshot BETWEEN the two halves, which is
// the one window neither half can be tested for on its own.
//
// WHAT IT PINS: the send loop's subscriber set is the snapshot AS OF THE LOCK,
// and an entry the pre-warm never saw is PLANNED, not skipped. Which subscribers
// see a batch is the planner's call (suppressedFor), and the loop must not carry
// a second, coarser copy of that decision -- "whoever the pre-warm covered" is
// exactly such a copy, and it drops anyone whose plan is missing for ANY reason.
//
// The reason that is not hypothetical is the filter re-publication:
// ExpandSubscribersForWorkspace runs on the lifecycle-outbox consumer goroutine
// and takes subscribeExpandMu, NOT m.projection, so its MutateEach can re-clone a
// subscriber while this broadcast's pre-warm is running. The locked snapshot then
// holds a pointer the recording does not cover (see batchPlans), belonging to a
// subscriber registered long before this batch -- so its suppress gate sits BELOW
// the batch and the planner owes it the full frame stream. Serving it off the
// pre-warm's slice would lose the batch for it outright.
//
// The subscriber here is given that same shape -- in the locked snapshot, absent
// from the recording, gate below the batch -- through the real register window,
// which is the seam the Manager exposes. (A registration is the milder case:
// landing after the commit it captures a gate at or above the batch, so the
// planner returns an empty plan and its own bootstrap carries these ops. See
// prewarmBatch.)
//
// The window is opened deterministically rather than raced: the test holds
// m.projection itself, so the fan-out CANNOT proceed until the subscriber has
// been published and the lock released.
func TestFanOutBatch_ServesASubscriberPublishedWhileThePreWarmRan(t *testing.T) {
	fixture := newPlanFixture()
	m := fixture.newManager()
	visible := map[string]bool{"w1": true, "w2": true}

	var earlySent, lateSent []*MarshaledEvent
	early := recordingSubscriber(m, visible, &earlySent)
	regEarly := m.registerForFallback(early, fallbackCold)
	t.Cleanup(regEarly.unsub)

	// The unlocked half, exactly as broadcastBatch runs it: `early` is the whole
	// published snapshot, so it is the only subscriber with a recorded plan.
	fan := m.prewarmBatch(fixture.batch, fixture.res)
	require.NotNil(t, fan, "a published subscriber must produce a fan-out to send")
	require.Len(t, fan.plans.spans, 1, "the pre-warm covers exactly the snapshot it read")
	require.Empty(t, earlySent, "the pre-warm plans and marshals frames; it does not send them")

	// Stand in for the registration in flight: hold the lock the register window
	// holds, and publish the subscriber inside that hold.
	late := recordingSubscriber(m, visible, &lateSent)
	m.projection.Lock()
	fanOutDone := make(chan struct{})
	go func() {
		defer close(fanOutDone)
		m.fanOutBatch(fan)
	}()
	regLate := m.registerForFallbackLocked(late, fallbackCold)
	m.projection.Unlock()
	t.Cleanup(regLate.unsub)

	<-fanOutDone

	require.NotEmpty(t, lateSent,
		"the batch was lost for the late arrival: the locked pass must RE-READ the subscriber "+
			"snapshot, so a registration that published while the pre-warm ran is still served")
	// materialized(n3) -> batch(n1, n2) -> removed(n4) -> end
	require.Len(t, earlySent, 4)
	require.Len(t, lateSent, len(earlySent),
		"a late arrival is planned on the spot, so it receives the same frame count as the "+
			"subscriber whose plan was recorded")
	for i := range earlySent {
		assert.Same(t, earlySent[i], lateSent[i],
			"frame %d differs: both subscribers are planned against the SAME fanout, so the late "+
				"arrival draws the same shared wrapper in the same wire order", i)
	}
}

// TestBroadcastBatch_DeliversPreMarshaledFramesToEverySubscriber runs the whole
// fan-out -- both passes, the real memo caches, the real projection lock -- and
// asserts the property the pre-warm exists for END TO END: by the time a frame
// reaches Send, its proto.Marshal has already been paid.
//
// The per-subscriber tests above pin the plan's shape; this pins that
// broadcastBatch still wires the two passes together so that the shape holds for
// every subscriber in one real fan-out, including one whose filter admits a
// different subset of the batch.
func TestBroadcastBatch_DeliversPreMarshaledFramesToEverySubscriber(t *testing.T) {
	fixture := newPlanFixture()
	m := fixture.newManager()

	received := map[string][]*MarshaledEvent{}
	register := func(name string, workspaceIDs map[string]bool) {
		sub := &Subscriber{
			UserID: m.owner.String(),
			Filter: NewSubscriberFilter(workspaceIDs),
			Send: func(evt *MarshaledEvent) error {
				received[name] = append(received[name], evt)
				return nil
			},
		}
		reg := m.registerForFallback(sub, fallbackCold)
		t.Cleanup(reg.unsub)
	}
	register("both", map[string]bool{"w1": true, "w2": true})
	register("w1-only", map[string]bool{"w1": true})

	m.broadcastBatch(fixture.batch, fixture.res)

	// materialized(n3) -> batch(n1, n2) -> removed(n4) -> end
	require.Len(t, received["both"], 4)
	// materialized(n3) -> batch(n1) -> end: n4 leaves a workspace this
	// subscriber never saw, so it has nothing to evict.
	require.Len(t, received["w1-only"], 3)
	for name, frames := range received {
		for i, evt := range frames {
			assert.True(t, evt.AlreadyMarshaledForTest(),
				"%s's frame %d reached Send unmarshaled: the subscriber queue charges by size on the "+
					"way in, so its proto.Marshal would run inside the projection lock", name, i)
		}
	}
	assert.Same(t, received["both"][0], received["w1-only"][0],
		"the becoming-visible record is one wrapper shared across the fan-out")
	assert.NotSame(t, received["both"][1], received["w1-only"][1],
		"but the batch frame differs by visibility mask, so these two get different ops")
}

// BenchmarkBroadcastBatch measures one full fan-out -- both passes -- at a
// realistic subscriber count over a batch large enough that the per-subscriber
// classification is visible next to the marshals.
//
// WHICH NUMBER MATTERS. The proto.Marshal of each distinct batch frame does not
// go away and should stay flat: it is memoized per visibility mask and the
// clients still need the bytes. What the plan recording removes is the SECOND
// per-subscriber planner run -- the walk over every affected ref with two
// Filter.IsAllowed probes each, the workspace mask, and the planner's per-frame
// thunks -- so the allocation count and the wall time should fall while the
// delivered bytes do not change.
//
// The two cases are the two sides of batchEventCache's soundness bound. Up to 64
// distinct workspaces the batch frame is memoized per visibility mask, and the
// recording saves only the classification. Past it the cache is disabled, every
// batchEventFor call mints a fresh wrapper, and the recording additionally saves
// a whole second marshal of the batch frame PER SUBSCRIBER -- which without it
// landed under the projection lock, the one place the pre-warm exists to keep
// clear.
func BenchmarkBroadcastBatch(b *testing.B) {
	for _, bc := range []struct {
		name       string
		workspaces int
	}{
		{"8workspaces_maskcache", 8},
		{"72workspaces_nomaskcache", 72},
	} {
		b.Run(bc.name, func(b *testing.B) {
			benchmarkBroadcastBatch(b, bc.workspaces)
		})
	}
}

func benchmarkBroadcastBatch(b *testing.B, workspaces int) {
	const (
		subscribers   = 8
		opsPerWS      = 64
		crossingPerWS = 2
	)

	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())
	m.state.MaxHlc = &leapmuxv1.HLC{Physical: 1, Logical: 0, ClientId: "hub"}

	atHLC := &leapmuxv1.HLC{Physical: 1_000, Logical: 0, ClientId: "c"}
	trans := make(map[EntityRef]EntityWorkspaceTransition, workspaces*(opsPerWS+2*crossingPerWS))
	batch := &leapmuxv1.OpBatch{BatchId: "bench", Ops: make([]*leapmuxv1.CrdtOp, 0, workspaces*opsPerWS)}
	for w := range workspaces {
		ws := fmt.Sprintf("w%d", w)
		for i := range opsPerWS {
			id := fmt.Sprintf("n-%d-%d", w, i)
			batch.Ops = append(batch.Ops, &leapmuxv1.CrdtOp{
				OpId:         "op-" + id,
				CanonicalHlc: atHLC,
				Body: &leapmuxv1.CrdtOp_SetNodeRegister{SetNodeRegister: &leapmuxv1.SetNodeRegisterOp{
					NodeId: id,
					Field:  &leapmuxv1.SetNodeRegisterOp_Position{Position: "a0"},
				}},
			})
			trans[EntityRef{Kind: EntityKindNode, NodeID: id}] = EntityWorkspaceTransition{Pre: ws, Post: ws}
		}
		// Cross-workspace movement, so the materialized / removed arms of the
		// planner run rather than only the stable-visibility one.
		for i := range crossingPerWS {
			in := fmt.Sprintf("in-%d-%d", w, i)
			m.state.Nodes[in] = &leapmuxv1.NodeRecord{NodeId: in}
			trans[EntityRef{Kind: EntityKindNode, NodeID: in}] = EntityWorkspaceTransition{Post: ws}
			trans[EntityRef{Kind: EntityKindNode, NodeID: fmt.Sprintf("out-%d-%d", w, i)}] =
				EntityWorkspaceTransition{Pre: ws}
		}
	}
	res := ValidationResult{AffectedEntities: trans}

	// Distinct filters, so the fan-out spans several visibility masks rather
	// than collapsing onto one cached batch frame.
	for s := range subscribers {
		allowed := map[string]bool{}
		for w := range workspaces {
			if w%subscribers != s {
				allowed[fmt.Sprintf("w%d", w)] = true
			}
		}
		sub := &Subscriber{
			UserID: m.owner.String(),
			Filter: NewSubscriberFilter(allowed),
			// Stand in for subscriberQueue's byte charge, which is what forces
			// the lazy marshal on the real path.
			Send: func(evt *MarshaledEvent) error {
				_ = evt.Size()
				return nil
			},
		}
		reg := m.registerForFallback(sub, fallbackCold)
		b.Cleanup(reg.unsub)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		m.broadcastBatch(batch, res)
	}
}
