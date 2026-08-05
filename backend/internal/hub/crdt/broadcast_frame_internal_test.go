package crdt

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func presenceEvent(workspaceID, activeClientID string) *leapmuxv1.WatchUserEvent {
	return &leapmuxv1.WatchUserEvent{
		Event: &leapmuxv1.WatchUserEvent_Presence{
			Presence: &leapmuxv1.PresenceUpdate{WorkspaceId: workspaceID, ActiveClientId: activeClientID},
		},
	}
}

func admittingSubscriber(workspaceID string, send func(*MarshaledEvent) error) *Subscriber {
	return &Subscriber{
		UserID: "user-1",
		Filter: NewSubscriberFilter(map[string]bool{workspaceID: true}),
		Send:   send,
	}
}

// The pre-warm exists to keep proto.Marshal off the projection lock, not to
// marshal a frame nobody can see. A NON-EMPTY subscriber set with none of its
// filters admitting the workspace is routine rather than exotic:
// PresenceController.processClear fans one broadcastTo per workspace a
// just-disconnected client held presence in, and that client's own subscriber is
// already gone -- so a surviving narrow subscriber can admit none of them.
func TestBroadcastFrame_PrewarmMarshalsNothingWhenNoSubscriberAdmitsTheWorkspace(t *testing.T) {
	frame := &broadcastFrame{evt: presenceEvent("w1", "client-a")}
	subs := []*Subscriber{
		{Filter: NewSubscriberFilter(map[string]bool{"w2": true})},
		{Filter: NewSubscriberFilter(map[string]bool{})},
	}

	frame.prewarm(subs, "w1")

	assert.Nil(t, frame.me,
		"no subscriber admits w1, so no wire frame for it should exist yet")
}

func TestBroadcastFrame_PrewarmMarshalsWhenSomeSubscriberAdmitsTheWorkspace(t *testing.T) {
	frame := &broadcastFrame{evt: presenceEvent("w1", "client-a")}
	subs := []*Subscriber{
		{Filter: NewSubscriberFilter(map[string]bool{"w2": true})},
		{Filter: NewSubscriberFilter(map[string]bool{"w1": true})},
	}

	frame.prewarm(subs, "w1")

	require.NotNil(t, frame.me, "a subscriber admits w1, so its frame is built ahead of the lock")
	assert.True(t, frame.me.AlreadyMarshaledForTest(),
		"pre-warming must pay the MARSHAL, not merely allocate the wrapper -- otherwise the "+
			"subscriber queue's charge still serializes a proto under the projection lock")
}

// An empty snapshot is the same "skip the work" verdict as a full one that
// admits nobody, and for the same reason it must not become a "skip the
// delivery" verdict -- see fanOutFrame's test below.
func TestBroadcastFrame_PrewarmMarshalsNothingForAnEmptySubscriberSet(t *testing.T) {
	frame := &broadcastFrame{evt: presenceEvent("w1", "client-a")}

	frame.prewarm(nil, "w1")

	assert.Nil(t, frame.me)
}

// get is what makes the pre-warm skippable: it mints and marshals on demand, so
// a late arrival the pre-check never saw still gets a frame, and every caller
// after the first gets the SAME one (N queues holding one buffer is what the
// MarshaledEvent refcount is built on).
func TestBroadcastFrame_GetMarshalsOnceAndReusesTheFrame(t *testing.T) {
	evt := presenceEvent("w1", "client-a")
	frame := &broadcastFrame{evt: evt}

	first := frame.get()
	require.NotNil(t, first)
	assert.Same(t, evt, first.Event, "the frame must wrap the caller's proto, not a copy")
	assert.True(t, first.AlreadyMarshaledForTest())
	assert.Same(t, first, frame.get(), "a second subscriber shares the first one's buffer")
}

// TestFanOutFrame_DeliversToASubscriberPublishedWhileItWaitedForTheLock is the
// zero-subscriber-path regression.
//
// broadcastTo's pre-check reads the LOCK-FREE snapshot, and a registration in
// flight holds m.projection with its subscriber not yet published (see
// registerLocked). Answering "nobody is listening" from that stale read and
// returning drops the event permanently: PresenceUpdate has no bootstrap arm and
// is not replayed by the resume delta, so unlike a batch there is no second
// delivery path. Taking the lock is what serializes the fan-out behind the
// registration and lets it see what that registration published.
//
// The window is opened deterministically rather than raced: the test holds
// m.projection itself, so the fan-out CANNOT proceed until the subscriber has
// been registered through the real register window and the lock released.
func TestFanOutFrame_DeliversToASubscriberPublishedWhileItWaitedForTheLock(t *testing.T) {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())

	got := make(chan *MarshaledEvent, 1)
	sub := admittingSubscriber("w1", func(evt *MarshaledEvent) error {
		got <- evt
		return nil
	})

	evt := presenceEvent("w1", "client-a")
	frame := &broadcastFrame{evt: evt}
	// The pre-check, exactly as broadcastTo runs it: against the snapshot as it
	// stands before the registration publishes anything.
	frame.prewarm(m.snapshotSubs(), "w1")
	require.Nil(t, frame.me, "nobody is published yet, so the pre-check has nothing to pre-warm")

	// Stand in for the registration in flight: hold the lock the register window
	// holds, and publish the subscriber inside that hold.
	m.projection.Lock()
	fanOutDone := make(chan struct{})
	go func() {
		defer close(fanOutDone)
		m.fanOutFrame("w1", frame)
	}()
	reg := m.registerForFallbackLocked(sub, fallbackCold)
	m.projection.Unlock()
	t.Cleanup(reg.unsub)

	<-fanOutDone
	select {
	case delivered := <-got:
		require.NotNil(t, delivered)
		assert.Same(t, evt, delivered.Event,
			"the subscriber must receive the event the fan-out was called with")
		assert.True(t, delivered.AlreadyMarshaledForTest(),
			"a late arrival's frame is marshaled before Send, so the queue's charge cannot "+
				"serialize a proto under the projection lock")
	default:
		t.Fatal("the presence event was lost: the fan-out must re-read the subscriber snapshot " +
			"under m.projection, so a registration that published while it waited is still served")
	}
}

// The locked pass is also the one that enforces the filter, so a pre-warmed
// frame must not reach a subscriber that cannot see the workspace.
func TestFanOutFrame_SkipsSubscribersThatDoNotAdmitTheWorkspace(t *testing.T) {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())

	var sends int
	sub := &Subscriber{
		UserID: "user-1",
		Filter: NewSubscriberFilter(map[string]bool{"w2": true}),
		Send: func(*MarshaledEvent) error {
			sends++
			return nil
		},
	}
	reg := m.registerForFallback(sub, fallbackCold)
	t.Cleanup(reg.unsub)

	frame := &broadcastFrame{evt: presenceEvent("w1", "client-a")}
	m.fanOutFrame("w1", frame)

	assert.Zero(t, sends, "a subscriber that cannot see w1 must not receive its presence update")
	assert.Nil(t, frame.me, "and no frame is minted for a fan-out that admitted nobody")
}

// broadcastTo is the composition of the two halves above; this pins that it
// still delivers, shares ONE frame across every admitted subscriber, and hands
// it over already marshaled.
func TestBroadcastTo_SharesOneMarshaledFrameAcrossAdmittedSubscribers(t *testing.T) {
	m := NewManager(userid.MustNew("user-1"), nil, nil, nil, nil)
	m.state = NewState(m.owner.String())

	received := make([]*MarshaledEvent, 0, 2)
	for range 2 {
		sub := admittingSubscriber("w1", func(evt *MarshaledEvent) error {
			received = append(received, evt)
			return nil
		})
		reg := m.registerForFallback(sub, fallbackCold)
		t.Cleanup(reg.unsub)
	}
	hidden := 0
	blind := &Subscriber{
		UserID: "user-1",
		Filter: NewSubscriberFilter(map[string]bool{"w2": true}),
		Send: func(*MarshaledEvent) error {
			hidden++
			return nil
		},
	}
	regBlind := m.registerForFallback(blind, fallbackCold)
	t.Cleanup(regBlind.unsub)

	evt := presenceEvent("w1", "client-a")
	m.broadcastTo("w1", evt)

	require.Len(t, received, 2, "both subscribers that admit w1 must be served")
	assert.Same(t, received[0], received[1],
		"one marshaled buffer is shared across the fan-out; a second would be memory that does not exist")
	assert.Same(t, evt, received[0].Event)
	assert.True(t, received[0].AlreadyMarshaledForTest())
	assert.Zero(t, hidden)
}
