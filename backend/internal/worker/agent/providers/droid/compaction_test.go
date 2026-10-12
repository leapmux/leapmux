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
	assert.Equal(t, contracts.NotificationTypeAgentError, notice[contracts.NotificationFieldType])
	assert.Contains(t, notice[contracts.NotificationFieldError], "Nothing to compact")
	require.NoError(t, a.SendInput("continue after the refusal", nil), "the native refusal releases the input guard")
}

func TestCompactContextBusyRefusalPreservesAnIndependentTurn(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newSteerAgent(t)
	require.NoError(t, a.CompactContext())
	a.armTurn()
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
	a.armTurn()
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
	a.armTurn()
	a.disarmTurn()
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
	nativeRows := len(sink.PersistedNotifications())
	sendDroidCompactedNotification(t, a)
	assert.Len(t, sink.PersistedNotifications(), nativeRows, "a late completion cannot follow the timeout error")
}
