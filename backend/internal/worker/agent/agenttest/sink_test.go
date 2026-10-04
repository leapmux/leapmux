package agenttest

import (
	"errors"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSinkSpanOnlyCreationStaysUnlinked(t *testing.T) {
	t.Parallel()
	parent := &Sink{}
	childID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "native-span-only"})
	require.NoError(t, err)
	require.NotEmpty(t, childID)
	child := parent.Child(childID)
	assert.Empty(t, child.nativeChildKey)
	span, err := parent.ChildSpawnSpan(childID)
	require.NoError(t, err)
	assert.Equal(t, "native-span-only", span)
	assert.Empty(t, parent.BackgroundTasks())
}

func TestSinkSpanOnlyChildrenKeepSeparateSpans(t *testing.T) {
	t.Parallel()
	parent := &Sink{}
	first, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "first-native-span"})
	require.NoError(t, err)
	second, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "second-native-span"})
	require.NoError(t, err)
	assert.NotEqual(t, first, second)
	replayed, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "first-native-span"})
	require.NoError(t, err)
	assert.Equal(t, first, replayed)
	assert.ElementsMatch(t, []string{first, second}, parent.ChildAgentIDs())
	assert.Empty(t, parent.BackgroundTasks())
}

func TestSinkAdoptsTheRealKeyAfterSpanOnlyCreation(t *testing.T) {
	t.Parallel()
	parent := &Sink{}
	spec := agent.ChildAgentSpec{SpawnSpanID: "native-adoption-span"}
	childID, err := parent.EnsureChildAgent(spec)
	require.NoError(t, err)
	require.NoError(t, parent.PersistChildPrompt(childID, "Inspect the native task."))
	child := parent.Child(childID)
	before := child.Messages()
	spec.ProviderChildKey = "ses-real-native-child"
	adopted, err := parent.EnsureChildAgent(spec)
	require.NoError(t, err)
	assert.Equal(t, childID, adopted)
	assert.Equal(t, spec.ProviderChildKey, child.nativeChildKey)
	assert.Equal(t, before, child.Messages())
	span, err := parent.ChildSpawnSpan(childID)
	require.NoError(t, err)
	assert.Equal(t, spec.SpawnSpanID, span)
	rows := parent.BackgroundTasks()
	require.Len(t, rows, 1)
	assert.Equal(t, spec.ProviderChildKey, rows[0].RowKey)
	assert.Equal(t, childID, rows[0].ChildAgentID)
	wrong := spec
	wrong.ProviderChildKey = "ses-different-native-child"
	rejected, err := parent.EnsureChildAgent(wrong)
	assert.ErrorIs(t, err, agent.ErrChildIdentityRefused)
	assert.Empty(t, rejected)
	assert.Equal(t, before, child.Messages())
}

func TestSinkChildCreationKeepsItsInitialNativeSession(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name      string
		initial   string
		requested string
		refuses   bool
	}{
		{name: "exact session", initial: "native-a", requested: "native-a"},
		{name: "empty request", initial: "native-a"},
		{name: "different session", initial: "native-a", requested: "native-b", refuses: true},
		{name: "empty existing session", requested: "native-b", refuses: true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			parent := &Sink{}
			spec := agent.ChildAgentSpec{SpawnSpanID: "child-spawn", ProviderChildKey: "native-child", AgentSessionID: scenario.initial}
			childID, err := parent.EnsureChildAgent(spec)
			require.NoError(t, err)
			require.NoError(t, parent.PersistChildPrompt(childID, "Native child prompt."))
			child := parent.Child(childID)
			messages := child.Messages()
			require.Len(t, messages, 1)
			assert.Equal(t, scenario.initial, messages[0].AgentSessionID)
			spec.AgentSessionID = scenario.requested
			got, err := parent.EnsureChildAgent(spec)
			if scenario.refuses {
				assert.Error(t, err)
				assert.ErrorIs(t, err, agent.ErrChildIdentityRefused)
				assert.Empty(t, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, childID, got)
			}
			assert.Equal(t, scenario.initial, child.LastSessionID())
			assert.Equal(t, messages, child.Messages())
		})
	}
}

func TestSinkStoredToolRowsKeepTheirNativeSessionAndOwner(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("first-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"owner":"first"}`)}, agent.SpanInfo{SpanID: "reused-call"}))
	first, err := sink.ReadToolRequest("reused-call")
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "first-session", first.Content.AgentSessionID)
	sink.UpdateSessionID("second-session")
	old, err := sink.ReadToolRequest("reused-call")
	require.NoError(t, err)
	assert.Nil(t, old, "a new native session cannot read the earlier session's opener")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{"owner":"second"}`)}, agent.SpanInfo{SpanID: "reused-call"}))
	current, err := sink.ReadToolRequest("reused-call")
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, []byte(`{"owner":"second"}`), current.Content.Original)
	assert.Equal(t, "second-session", current.Content.AgentSessionID)
	assert.Equal(t, "first-session", first.Content.AgentSessionID, "the earlier read keeps its immutable session")
	last, err := sink.ReadToolResult("reused-call")
	require.NoError(t, err)
	require.NotNil(t, last)
	assert.Equal(t, "second-session", last.Content.AgentSessionID)
	assert.Equal(t, current.Content.Original, last.Content.Original)
}

func TestSinkStoredToolRowsUseTheExplicitSessionAndCopyTheirContent(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("current-session")
	original := []byte(`{"value":0}`)
	supplement := []byte(`{"output":""}`)
	metadata := []byte(`{"count":0}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT,
		agent.MessageContent{Original: original, Supplemental: supplement, Metadata: metadata, AgentSessionID: "explicit-session"},
		agent.SpanInfo{SpanID: "call"}))
	original[0], supplement[0], metadata[0] = 'x', 'x', 'x'
	missing, err := sink.ReadToolRequest("call")
	require.NoError(t, err)
	assert.Nil(t, missing)
	sink.UpdateSessionID("explicit-session")
	stored, err := sink.ReadToolRequest("call")
	require.NoError(t, err)
	require.NotNil(t, stored)
	assert.Equal(t, "explicit-session", stored.Content.AgentSessionID)
	assert.JSONEq(t, `{"value":0}`, string(stored.Content.Original))
	assert.JSONEq(t, `{"output":""}`, string(stored.Content.Supplemental))
	assert.JSONEq(t, `{"count":0}`, string(stored.Content.Metadata))
	stored.Content.Original[0], stored.Content.Supplemental[0], stored.Content.Metadata[0] = 'x', 'x', 'x'
	again, err := sink.ReadToolRequest("call")
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.JSONEq(t, `{"value":0}`, string(again.Content.Original))
	assert.JSONEq(t, `{"output":""}`, string(again.Content.Supplemental))
	assert.JSONEq(t, `{"count":0}`, string(again.Content.Metadata))
	snapshot := sink.Messages()
	snapshot[0].Content[0], snapshot[0].SupplementalContent[0], snapshot[0].Metadata[0] = 'x', 'x', 'x'
	afterSnapshot, err := sink.ReadToolRequest("call")
	require.NoError(t, err)
	require.NotNil(t, afterSnapshot)
	assert.JSONEq(t, `{"value":0}`, string(afterSnapshot.Content.Original))
	assert.JSONEq(t, `{"output":""}`, string(afterSnapshot.Content.Supplemental))
	assert.JSONEq(t, `{"count":0}`, string(afterSnapshot.Content.Metadata))
}

func TestSinkMessageEnrichmentMatchesTheWorkerSessionAndSequenceRules(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("first-session")
	original := []byte(`{"output":""}`)
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: original}, agent.SpanInfo{SpanID: "call"}))
	sink.UpdateSessionID("second-session")
	change := agent.MessageEnrichment{SpanID: "call", OriginalContent: original, SupplementalContent: []byte(`{"value":0}`)}
	changed, err := sink.EnrichMessage(change)
	require.NoError(t, err)
	assert.False(t, changed, "a span lookup cannot enrich another native session")
	assert.Empty(t, sink.Messages()[0].SupplementalContent)
	// The Worker permits an exact sequence to identify an earlier session's row.
	change.Seq = 1
	changed, err = sink.EnrichMessage(change)
	require.NoError(t, err)
	assert.True(t, changed)
	assert.JSONEq(t, `{"value":0}`, string(sink.Messages()[0].SupplementalContent))
	for _, invalid := range []agent.MessageEnrichment{
		{SpanID: "call", Seq: -1, OriginalContent: original, PreviousRevision: 1, SupplementalContent: []byte(`{"value":1}`)},
		{SpanID: "call", Seq: 1, OriginalContent: original, PreviousRevision: -1, SupplementalContent: []byte(`{"value":1}`)},
		{SpanID: "", Seq: 1, OriginalContent: original, PreviousRevision: 1, SupplementalContent: []byte(`{"value":1}`)},
	} {
		changed, err := sink.EnrichMessage(invalid)
		require.NoError(t, err)
		assert.False(t, changed)
	}
}

func TestSinkChildSpawnLookupRequiresTheDirectParent(t *testing.T) {
	t.Parallel()
	root := &Sink{}
	firstID, err := root.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "first-spawn"})
	require.NoError(t, err)
	secondID, err := root.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "second-spawn"})
	require.NoError(t, err)
	first := root.Child(firstID)
	for _, id := range []string{firstID, secondID} {
		span, err := first.ChildSpawnSpan(id)
		require.NoError(t, err)
		assert.Empty(t, span, "a child cannot grant ownership of itself or its sibling")
	}
	nestedID, err := first.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "nested-spawn"})
	require.NoError(t, err)
	span, err := first.ChildSpawnSpan(nestedID)
	require.NoError(t, err)
	assert.Equal(t, "nested-spawn", span)
	span, err = root.ChildSpawnSpan(nestedID)
	require.NoError(t, err)
	assert.Empty(t, span, "a root cannot claim its child's own spawn")
	span, err = root.ChildSpawnSpan(firstID)
	require.NoError(t, err)
	assert.Equal(t, "first-spawn", span)
}

// This test belongs beside the fake: every caller of ChildAgentIDs asserts
// NotContains against a child id, so a version that returned the map's
// spawn-span keys satisfied all of them and proved nothing. Pinning the return
// shape beside the fake keeps that from coming back.
func TestTestSink_ChildAgentIDsReturnsChildIDsNotSpawnSpans(t *testing.T) {
	t.Parallel()

	s := &Sink{}
	childID, err := s.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "tu-spawn", ProviderChildKey: "task-1", Title: "explore"})
	require.NoError(t, err)
	require.Equal(t, "child-of-tu-spawn", childID)

	assert.Equal(t, []string{childID}, s.ChildAgentIDs())
	assert.NotContains(t, s.ChildAgentIDs(), "tu-spawn", "the spawn span is the key, not the id")
}

func TestSink_IndependentUnlinkedChildrenKeepTheirNativeIdentity(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	first, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "", ProviderChildKey: "native-child-a", Title: "First child"})
	require.NoError(t, err)
	second, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "", ProviderChildKey: "native-child-b", Title: "Second child"})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	assert.ElementsMatch(t, []string{first, second}, sink.ChildAgentIDs())
	replay, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "", ProviderChildKey: "native-child-a", Title: "First child"})
	require.NoError(t, err)
	assert.Equal(t, first, replay)
	span, err := sink.ChildSpawnSpan(first)
	require.NoError(t, err)
	assert.Empty(t, span)
}

func TestSink_UnlinkedChildRequiresNativeIdentity(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	_, err := sink.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "", ProviderChildKey: "", Title: "Unidentified child"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, agent.ErrChildIdentityRefused)
	assert.Empty(t, sink.ChildAgentIDs())
}

func TestSink_ConflictingNativeChildKeyCannotReuseAnotherChildsSpawnSpan(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	first, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-a", SpawnSpanID: "spawn", Title: "First"})
	require.NoError(t, err)
	_, err = sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-b", SpawnSpanID: "spawn", Title: "Wrong child"})
	require.Error(t, err)
	assert.ErrorIs(t, err, agent.ErrChildIdentityRefused)
	assert.Equal(t, []string{first}, sink.ChildAgentIDs())
	_, exists := sink.BackgroundTask("native-b")
	assert.False(t, exists)
}

func TestSink_ConcurrentUnlinkedNativeChildCreatesOneSink(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	type outcome struct {
		id  string
		err error
	}
	results := make(chan outcome, 16)
	var group sync.WaitGroup
	for range cap(results) {
		group.Go(func() {
			id, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child"})
			results <- outcome{id: id, err: err}
		})
	}
	group.Wait()
	close(results)
	var childID string
	for result := range results {
		require.NoError(t, result.err)
		if childID == "" {
			childID = result.id
		}
		assert.Equal(t, childID, result.id)
	}
	assert.Equal(t, []string{childID}, sink.ChildAgentIDs())
}

func TestSink_FirstExactSpanAttachesToAnExistingNativeChild(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	childID, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", Title: "Child"})
	require.NoError(t, err)
	replay, err := sink.EnsureChildAgent(agent.ChildAgentSpec{ProviderChildKey: "native-child", SpawnSpanID: "native-call", Title: "Child"})
	require.NoError(t, err)
	assert.Equal(t, childID, replay)
	span, err := sink.ChildSpawnSpan(childID)
	require.NoError(t, err)
	assert.Equal(t, "native-call", span)
}

func TestSink_ImmutableNativeMessageReplayKeepsOneRecord(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"native":"complete"}`), IdempotencyKey: "native-message:content"}
	for range 2 {
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	}
	assert.Len(t, sink.Messages(), 1)
	sink.UpdateSessionID("another-session")
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	assert.Len(t, sink.Messages(), 2)
}

func TestSink_NativeMessageIdentityUsesItsExplicitSessionAndPart(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("current-session")
	for _, session := range []string{"old-session", "current-session"} {
		for _, key := range []string{"message:content", "message:thought:0"} {
			for range 2 {
				require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`), AgentSessionID: session, IdempotencyKey: key}, agent.SpanInfo{}))
			}
		}
	}
	assert.Len(t, sink.Messages(), 4)
	for range 2 {
		require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`)}, agent.SpanInfo{}))
	}
	assert.Len(t, sink.Messages(), 6)
}

func TestSink_ConcurrentNativeReplayStoresOneRecord(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("session")
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			assert.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`), IdempotencyKey: "native-record"}, agent.SpanInfo{}))
		})
	}
	group.Wait()
	assert.Len(t, sink.Messages(), 1)
}

func TestSink_FailedNativeWritesDoNotClaimTheMessageKey(t *testing.T) {
	t.Parallel()
	failure := errors.New("the store refused the row")
	sink := &Sink{PersistErr: failure}
	for range 2 {
		require.ErrorIs(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, agent.MessageContent{Original: []byte(`{}`), IdempotencyKey: "native-record"}, agent.SpanInfo{}), failure)
	}
	assert.Empty(t, sink.messageKeys)
	assert.Len(t, sink.Messages(), 2, "the fake records both failed attempts")
}

func TestSink_KeyedTurnEndReplayKeepsOneRecordAndLifecycleEvent(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("native-session")
	content := agent.MessageContent{Original: []byte(`{"type":"native-turn-end"}`), IdempotencyKey: "native-turn:17"}
	for range 2 {
		require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	}
	assert.Len(t, sink.Messages(), 1)
	assert.Equal(t, []string{"turn_end"}, sink.TurnLifecycle())
	sink.UpdateSessionID("another-session")
	require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	assert.Len(t, sink.Messages(), 2)
}

func TestSink_FailedTurnEndReturnsTheStoreFailureWithoutClaimingItsKey(t *testing.T) {
	t.Parallel()
	failure := errors.New("the store refused the turn end")
	sink := &Sink{PersistErr: failure}
	content := agent.MessageContent{Original: []byte(`{"type":"native-turn-end"}`), IdempotencyKey: "native-turn:17"}
	for range 2 {
		require.ErrorIs(t, sink.PersistTurnEnd(content, agent.SpanInfo{}), failure)
	}
	assert.Empty(t, sink.messageKeys)
	assert.Empty(t, sink.turnEndKeys)
	assert.Empty(t, sink.TurnLifecycle(), "a failed turn end cannot publish a lifecycle event")
}

func TestSink_ConcurrentTurnEndReplayPublishesOneLifecycleEvent(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	sink.UpdateSessionID("native")
	var group sync.WaitGroup
	for range 16 {
		group.Go(func() {
			assert.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"result"}`), IdempotencyKey: "turn:0"}, agent.SpanInfo{}))
		})
	}
	group.Wait()
	assert.Len(t, sink.Messages(), 1)
	assert.Equal(t, []string{"turn_end"}, sink.TurnLifecycle())
}

func TestSink_TurnEndClaimsRemainSeparateFromMessageKeys(t *testing.T) {
	t.Parallel()
	sink := &Sink{}
	content := agent.MessageContent{Original: []byte(`{"type":"result"}`), AgentSessionID: "native", IdempotencyKey: "turn:0"}
	require.NoError(t, sink.PersistMessage(leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, content, agent.SpanInfo{}))
	for range 2 {
		require.NoError(t, sink.PersistTurnEnd(content, agent.SpanInfo{}))
	}
	assert.Len(t, sink.Messages(), 1)
	assert.Equal(t, []string{"turn_end"}, sink.TurnLifecycle())
	for range 2 {
		require.NoError(t, sink.PersistTurnEnd(agent.MessageContent{Original: []byte(`{"type":"result"}`)}, agent.SpanInfo{}))
	}
	assert.Len(t, sink.Messages(), 3)
	assert.Equal(t, []string{"turn_end", "turn_end", "turn_end"}, sink.TurnLifecycle())
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
