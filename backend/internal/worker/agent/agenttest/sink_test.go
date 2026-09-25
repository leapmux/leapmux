package agenttest

import (
	"errors"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// This test belongs beside the fake: every caller of ChildAgentIDs asserts
// NotContains against a child id, so a version that returned the map's
// spawn-span keys satisfied all of them and proved nothing. Pinning the return
// shape beside the fake keeps that from coming back.
func TestTestSink_ChildAgentIDsReturnsChildIDsNotSpawnSpans(t *testing.T) {
	t.Parallel()

	s := &Sink{}
	childID, err := s.EnsureChildAgent("tu-spawn", "task-1", "explore")
	require.NoError(t, err)
	require.Equal(t, "child-of-tu-spawn", childID)

	assert.Equal(t, []string{childID}, s.ChildAgentIDs())
	assert.NotContains(t, s.ChildAgentIDs(), "tu-spawn", "the spawn span is the key, not the id")
}

func TestSink_PersistNotificationReportsBroadcastChoice(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		suppress  bool
		broadcast bool
	}{
		{name: "default broadcasts", broadcast: true},
		{name: "suppression prevents broadcast", suppress: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &Sink{SuppressNotificationBroadcast: tc.suppress}
			content := []byte(`{"type":"status"}`)

			broadcast, err := sink.PersistNotification(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content)
			require.NoError(t, err)
			assert.Equal(t, tc.broadcast, broadcast)
			content[0] = 'x'
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.JSONEq(t, `{"type":"status"}`, string(sink.PersistedNotifications()[0].Content))
		})
	}
}

// Sink delegates its span bookkeeping to the REAL engine, so the geometry a
// provider test asserts is the geometry production computes. It kept its own
// copy before, and drifted from the engine twice.
func TestTestSink_SpanStateComesFromTheRealEngine(t *testing.T) {
	t.Parallel()

	sink := &Sink{}
	// A reservation the tracker really parks: the color is a palette entry, not
	// a constant, and it is the one the matching open consumes.
	reserved := sink.ReserveSpanColor("tu-a", "")
	require.NotZero(t, reserved, "the real tracker never reserves color 0")
	sink.OpenSpan("tu-a", "")
	sink.OpenSpan("tu-b", "tu-a")

	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{SpanID: "tu-c"}))
	msgs := sink.Messages()
	require.Len(t, msgs, 1)
	// Column order, and the parentage the engine recorded -- neither of which a
	// hand-kept slice reproduced.
	assert.Equal(t, []SpanOpen{
		{SpanID: "tu-a", ParentSpanID: ""},
		{SpanID: "tu-b", ParentSpanID: "tu-a"},
	}, msgs[0].SpansOpenAtPersist)

	// The engine's type lifetime, not the double's: a close keeps the type and
	// only a reset clears it.
	sink.SetSpanType("tu-a", "Read")
	sink.CloseSpan("tu-a")
	assert.Equal(t, "Read", sink.GetSpanType("tu-a"))
	sink.ResetSpans()
	assert.Empty(t, sink.GetSpanType("tu-a"))
	assert.Empty(t, sink.liveSpansLocked(), "a reset empties the active set")
}

// The fake records each hand-back in order, a repeat included, with the turn
// state that the provider published last. It also places each one in the turn
// lifecycle, so a test can see whether the input reached the queue before the
// turn ended.
func TestSink_RequeueDroppedInputRecordsEachCallInOrder(t *testing.T) {
	t.Parallel()

	s := &Sink{}
	attachments := []*leapmuxv1.Attachment{{Filename: "a.txt"}}
	require.NoError(t, s.RequeueDroppedInput("before", "no turn yet", nil))
	s.SetTurnState(agent.TurnState{Active: true}, 1)
	require.NoError(t, s.RequeueDroppedInput("drop-1", "first", attachments))
	require.NoError(t, s.RequeueDroppedInput("drop-1", "first", attachments))
	s.SetTurnState(agent.TurnState{Active: false}, 2)
	require.NoError(t, s.RequeueDroppedInput("drop-2", "second", nil))

	got := s.RequeuedInputs()
	require.Len(t, got, 4, "a repeat is recorded, so a test sees a provider that repeats a call")
	assert.Equal(t, RequeuedInput{DropID: "before", Content: "no turn yet"}, got[0],
		"TurnActive is false when the provider published no turn state")
	assert.Equal(t, RequeuedInput{DropID: "drop-1", Content: "first", Attachments: attachments, TurnActive: true}, got[1])
	assert.Equal(t, got[1], got[2])
	assert.Equal(t, RequeuedInput{DropID: "drop-2", Content: "second"}, got[3])
	assert.Equal(t, []string{
		"requeue:before", "turn_active:true", "requeue:drop-1", "requeue:drop-1", "turn_active:false", "requeue:drop-2",
	}, s.TurnLifecycle())

	got[0].DropID = "changed"
	assert.Equal(t, "before", s.RequeuedInputs()[0].DropID, "the caller gets a copy")
}

// RequeueErr stands for a queue that refused the input. The call fails and
// records nothing, so the provider must state the drop to the reader itself.
func TestSink_RequeueDroppedInputReturnsRequeueErrAndRecordsNothing(t *testing.T) {
	t.Parallel()

	refused := errors.New("the queue is full")
	s := &Sink{RequeueErr: refused}
	require.ErrorIs(t, s.RequeueDroppedInput("drop-1", "first", nil), refused)
	assert.Empty(t, s.RequeuedInputs())
	assert.Empty(t, s.TurnLifecycle())
}

// The count covers every read of a row key, a read that LookupErr fails
// included, so a test can prove that a caller keeps an answer rather than
// reading the registry again.
func TestSink_LookupBackgroundTaskCallsCountsEveryRead(t *testing.T) {
	t.Parallel()

	s := &Sink{}
	require.NoError(t, s.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "row-1", Kind: bgtask.KindShell, Title: "printf hi", Status: bgtask.StatusRunning,
	}))
	_, status, found, err := s.LookupBackgroundTask("row-1")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, bgtask.StatusRunning, status)
	_, _, found, err = s.LookupBackgroundTask("absent")
	require.NoError(t, err)
	assert.False(t, found)
	_, _, _, err = s.LookupBackgroundTask("row-1")
	require.NoError(t, err)

	assert.Equal(t, 2, s.LookupBackgroundTaskCalls("row-1"))
	assert.Equal(t, 1, s.LookupBackgroundTaskCalls("absent"), "a miss is a read too")
	assert.Zero(t, s.LookupBackgroundTaskCalls("never-read"))

	unreadable := errors.New("the registry is unreadable")
	failing := &Sink{LookupErr: unreadable}
	_, _, _, err = failing.LookupBackgroundTask("row-1")
	require.ErrorIs(t, err, unreadable)
	assert.Equal(t, 1, failing.LookupBackgroundTaskCalls("row-1"), "a read that fails is counted")
}
