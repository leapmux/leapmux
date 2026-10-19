package droid

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestCompactContextReportsNativeRefusalAndReleasesTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	assert.ErrorIs(t, a.SendInput("do not overtake compaction", nil), agent.ErrAgentBusy)

	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))
	assert.Equal(t, droidMethodCompactSession, request.Method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(request.Params, &params))
	assert.Equal(t, "main-session", params["sessionId"])

	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Error = &droidRPCError{Code: -32000, Message: "Nothing to compact"}
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	require.Eventually(t, func() bool {
		return len(sink.LeapMuxNotifications()) == 1
	}, 30*time.Second, 10*time.Millisecond)
	last, published := sink.LastTurnActive()
	assert.True(t, published, "the native refusal must release the queue's compact turn")
	assert.False(t, last, "the native refusal releases queued input")
	notice := sink.LeapMuxNotifications()[0]
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, notice[contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeFailed, notice[contracts.NotificationFieldOutcome])
	assert.Contains(t, notice[contracts.NotificationFieldError], "Nothing to compact")
	require.NoError(t, a.SendInput("continue after the refusal", nil), "the native refusal releases the input guard")
}

func TestCompactContextBusyRefusalPreservesAnIndependentTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	a.startTurn()
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))

	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Error = &droidRPCError{Code: -32602, Message: "Wait for the current turn to finish before compressing."}
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "a refused compact request does not end the independent turn")
	assert.ErrorIs(t, a.SendInput("wait for the independent turn", nil), agent.ErrAgentBusy)
}

func TestCompactContextInvalidSuccessStopsUnknownNativeState(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	a.startTurn()
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))

	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session"}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	require.Eventually(t, func() bool {
		return a.IsStopped() && len(sink.LeapMuxNotifications()) == 1
	}, 30*time.Second, 10*time.Millisecond)
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "an invalid native result must not publish an idle turn")
	assert.ErrorIs(t, a.SendInput("do not enter the unknown native state", nil), errAgentStopped)
	assert.Contains(t, sink.LeapMuxNotifications()[0][contracts.NotificationFieldError], "invalid compaction result")
}

func TestCompactContextReleasesQueuedInputAfterNativeCompletion(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	assert.ErrorIs(t, a.SendInput("do not overtake compaction", nil), agent.ErrAgentBusy)

	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session","removedCount":1}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	sendDroidCompactedNotification(t, a)
	require.Eventually(t, func() bool {
		a.Mu.Lock()
		defer a.Mu.Unlock()
		return a.compaction == nil
	}, 30*time.Second, 10*time.Millisecond)
	last, published := sink.LastTurnActive()
	assert.True(t, published, "the native reply must release the queue's compact turn")
	assert.False(t, last, "the native reply releases queued input")
	require.NoError(t, a.SendInput("continue after compaction", nil))
}

func TestCompactContextWaitsForCompletedNotificationAfterSuccessReply(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))

	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session","removedCount":1}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	a.Mu.Lock()
	pendingID := ""
	if a.compaction != nil {
		pendingID = a.compaction.requestID
	}
	a.Mu.Unlock()
	assert.Equal(t, request.ID, pendingID, "a successful reply precedes the native completed boundary")
	assert.ErrorIs(t, a.SendInput("wait for the completed boundary", nil), agent.ErrAgentBusy)
	last, published := sink.LastTurnActive()
	assert.False(t, published && !last, "the response must not release queued input")

	sendDroidCompactedNotification(t, a)
	last, published = sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last, "the native completed boundary releases queued input")
	require.NoError(t, a.SendInput("continue after compaction", nil))
}

func TestCompactContextCompletedNotificationDoesNotEndTheNextTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))

	sendDroidCompactedNotification(t, a)
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last, "the completed boundary releases queued input")
	require.NoError(t, a.SendInput("continue after compaction", nil))

	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session","removedCount":1}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	last, published = sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "the late compact reply cannot end the next turn")
}

func sendDroidCompactedNotification(t *testing.T, a *Agent) {
	t.Helper()
	notification := newDroidEnvelope(droidTypeNotification)
	notification.Method = droidMethodSessionNotif
	notification.Params = json.RawMessage(`{"sessionId":"main-session","notification":{"type":"session_compacted","summaryId":"summary-1","removedCount":1}}`)
	line, err := notification.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
}

func TestCompactContextKeepsTheQueueActiveUntilItsNativeResult(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	a.startTurn()
	a.endTurn()
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "an idle state before the compact result keeps queued input blocked")

	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session","removedCount":1}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	last, published = sink.LastTurnActive()
	assert.True(t, published)
	assert.True(t, last, "the successful reply leaves the compact turn active")
	sendDroidCompactedNotification(t, a)
	last, published = sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last, "the matching native result releases queued input")
}

func TestCompactContextWaitsPastTheNormalAPITimeout(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	a.SetAPITimeoutForTest(10 * time.Second)
	trap := clock.Trap().NewTimer("droid", "compaction")
	defer trap.Close()
	require.NoError(t, a.CompactContext())
	ctx := testutil.DeadlineContext(t)
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Greater(t, wait, a.APITimeout(), "model summarization outlives a normal JSON-RPC request")
	clock.Advance(a.APITimeout() + time.Second).MustWait(ctx)
	a.Mu.Lock()
	pending := a.compaction != nil
	a.Mu.Unlock()
	assert.True(t, pending, "the input queue waits while native compaction still runs")
	assert.ErrorIs(t, a.SendInput("wait for the summary", nil), agent.ErrAgentBusy)

	var request droidEnvelope
	require.NoError(t, json.Unmarshal([]byte(stdin.frames()[0]), &request))
	reply := newDroidEnvelope(droidTypeResponse)
	reply.ID = request.ID
	reply.Result = json.RawMessage(`{"newSessionId":"main-session","removedCount":1}`)
	line, err := reply.Marshal()
	require.NoError(t, err)
	a.HandleOutput(line)
	sendDroidCompactedNotification(t, a)
	last, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, last, "the native completion releases queued input")
}

func TestCompactContextStopsWhenTheNativeResultStalls(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	clock := testutil.NewQuartzMock(t)
	a.clock = clock
	a.SetAPITimeoutForTest(10 * time.Second)
	trap := clock.Trap().NewTimer("droid", "compaction")
	defer trap.Close()
	require.NoError(t, a.CompactContext())
	ctx := testutil.DeadlineContext(t)
	wait := testutil.WaitForTimer(t, ctx, trap)
	assert.Equal(t, droidCompactionTimeout, wait)
	clock.Advance(wait).MustWait(ctx)
	require.Eventually(t, func() bool {
		return a.IsStopped() && len(sink.LeapMuxNotifications()) == 1
	}, 30*time.Second, 10*time.Millisecond)
	last, published := sink.LastTurnActive()
	assert.False(t, published && !last, "an unknown native state must not release queued input")
	assert.ErrorIs(t, a.SendInput("do not enter the unknown native state", nil), errAgentStopped)
	assert.Contains(t, sink.LeapMuxNotifications()[0][contracts.NotificationFieldError], "context deadline exceeded")
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeFailed, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
	nativeRows := len(sink.PersistedNotifications())
	sendDroidCompactedNotification(t, a)
	assert.Len(t, sink.PersistedNotifications(), nativeRows, "a late completion cannot follow the timeout error")
}

func TestCompactContextPublishesLiveState(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"droid_working_state_changed","newState":"compacting_conversation"}`)))
	assert.Equal(t, []interface{}{map[string]interface{}{contracts.CompactionFieldTrigger: "manual"}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.LeapMuxNotifications())
	assert.Empty(t, sink.PersistedNotifications())
}

func TestCompactingWorkingStateIsLiveState(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"main-session", "child-1"} {
		t.Run(sessionID, func(t *testing.T) {
			t.Parallel()
			a, root, _ := newSteerAgent(t)
			sink := droidCompactionSink(t, a, root, sessionID)
			a.HandleOutput(droidAuditNotification(t, sessionID, []byte(`{"type":"droid_working_state_changed","newState":"compacting_conversation"}`)))
			assert.Equal(t, []interface{}{map[string]interface{}{}}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			if sink != root {
				assert.Empty(t, root.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			}
		})
	}
}

func droidCompactionSink(t *testing.T, a *Agent, root *agenttest.Sink, sessionID string) *agenttest.Sink {
	t.Helper()
	if sessionID == "main-session" {
		return root
	}
	a.HandleOutput([]byte(droidChildAvailable))
	child, ok := root.BackgroundTask(sessionID)
	require.True(t, ok)
	return root.Child(child.ChildAgentID)
}

func TestSessionCompactedStoresTheEndAndEndsLiveState(t *testing.T) {
	t.Parallel()
	for _, sessionID := range []string{"main-session", "child-1"} {
		t.Run(sessionID, func(t *testing.T) {
			t.Parallel()
			a, root, _ := newSteerAgent(t)
			sink := droidCompactionSink(t, a, root, sessionID)
			a.HandleOutput(droidAuditNotification(t, sessionID, []byte(`{"type":"droid_working_state_changed","newState":"compacting_conversation"}`)))
			end := []byte(`{"type":"session_compacted","removedCount":1}`)
			a.HandleOutput(droidAuditNotification(t, sessionID, end))
			assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			require.Len(t, sink.PersistedNotifications(), 1)
			assert.JSONEq(t, string(end), string(sink.PersistedNotifications()[0].Content))
		})
	}
}

func TestLeavingCompactingWithoutAnEndEndsLiveStateWithNoRow(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSteerAgent(t)
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"droid_working_state_changed","newState":"compacting_conversation"}`)))
	a.HandleOutput(droidAuditNotification(t, "", []byte(`{"type":"droid_working_state_changed","newState":"thinking"}`)))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.LeapMuxNotifications())
}

func TestChildCompactionSurvivesItsTurnEndUntilTheIdleState(t *testing.T) {
	t.Parallel()
	a, root, _ := newSteerAgent(t)
	sink := droidCompactionSink(t, a, root, "child-1")
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"droid_working_state_changed","newState":"compacting_conversation"}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"agent_turn_completed","reason":"completed"}`)))
	a.HandleOutput(droidAuditNotification(t, "child-1", []byte(`{"type":"droid_working_state_changed","newState":"idle"}`)))
	assert.Equal(t, []interface{}{map[string]interface{}{}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
}

func TestProcessExitDuringACompactWritesCompactionEndedAborted(t *testing.T) {
	t.Parallel()
	done := make(chan struct{})
	a, sink, _ := newSteerAgentWithProcessDone(t, done)
	require.NoError(t, a.CompactContext())
	close(done)
	testutil.RequireEventually(t, func() bool { return len(sink.LeapMuxNotifications()) == 1 }, "process exit ends the compaction")
	assert.Equal(t, contracts.NotificationTypeCompactionEnded, sink.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	assert.Equal(t, contracts.CompactionEndOutcomeAborted, sink.LeapMuxNotifications()[0][contracts.NotificationFieldOutcome])
	value, exists := sink.LastSessionInfoValue(contracts.SessionInfoKeyCompaction)
	assert.True(t, exists)
	assert.Nil(t, value)
}

func TestCompactEndDuringTheWriteDoesNotPublishALateStart(t *testing.T) {
	t.Parallel()
	a, sink, writer := newSteerAgent(t)
	writer.respond = func(request droidEnvelope) {
		if request.Method == droidMethodCompactSession {
			sendDroidCompactedNotification(t, a)
		}
	}
	require.NoError(t, a.CompactContext())
	for _, value := range sink.SessionInfoValues(contracts.SessionInfoKeyCompaction) {
		assert.Nil(t, value)
	}
	require.Len(t, sink.PersistedNotifications(), 1)
}
