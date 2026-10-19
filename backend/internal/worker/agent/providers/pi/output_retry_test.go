package pi

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const piRetryStart = `{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":2000,"errorMessage":"500 retry probe"}`
const piRetryCancelled = `{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"Retry cancelled"}`
const piRetryAttemptEnd = `{"type":"agent_end","willRetry":true,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"500 retry probe"}]}`

func startPiRetryBackoff(a *Agent) {
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(piRetryAttemptEnd))
	a.HandleOutput([]byte(piRetryStart))
}

func TestPiCancelledRetrySettlesTheTurnAsInterrupted(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	rig := newPiTestRig(t, agent.NewProviderServices(sink))
	a := rig.agent
	startPiRetryBackoff(a)
	assert.Empty(t, sink.Messages())
	a.HandleOutput([]byte(piRetryCancelled))
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive, "the native settlement ends the cancelled retry turn")
	assert.True(t, a.turnStartedAt.IsZero())
	a.Mu.Unlock()
	var dividers int
	for _, row := range sink.Messages() {
		var frame struct {
			Type string `json:"type"`
		}
		require.NoError(t, json.Unmarshal(row.Content, &frame))
		if frame.Type == "agent_end" {
			dividers++
			assert.Equal(t, []byte(piRetryAttemptEnd), row.Content, "the existing native divider remains unchanged")
			assert.True(t, row.TurnEnd)
			assert.Equal(t, agent.MessageCompletionInterrupted, row.Completion)
		}
	}
	assert.Equal(t, 1, dividers, "settlement must not add a duplicate divider")
	require.Equal(t, 2, sink.NotificationCount())
	assert.Equal(t, []byte(piRetryCancelled), sink.LastNotification().Content, "the native cancellation notice remains unchanged")
	require.NoError(t, a.SendInput("Continue after cancelled backoff.", nil), "later input must not return ErrAgentBusy")
}

func TestPiAbortDuringBackoffAcceptsLaterInput(t *testing.T) {
	t.Parallel()
	rig := newPiTestRig(t, agent.NewProviderServices(&agenttest.ControlSink{}))
	a := rig.agent
	startPiRetryBackoff(a)
	rig.setResponder(func(request piRecordedRequest) (json.RawMessage, bool, string) {
		if request.Type == CommandAbort {
			a.HandleOutput([]byte(piRetryCancelled))
			a.HandleOutput([]byte(`{"type":"agent_settled"}`))
		}
		return nil, true, ""
	})
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.NoError(t, a.SendInput("A normal prompt after the accepted backoff abort.", nil))
}

func TestPiDuplicateSettlementDoesNotAffectTheNextTurn(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	startPiRetryBackoff(a)
	a.HandleOutput([]byte(piRetryCancelled))
	a.HandleOutput([]byte(piRetryCancelled))
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive, "duplicate old evidence cannot own the replacement")
	a.Mu.Unlock()
}

func TestPiMalformedRetryStartDoesNotBlockSettlement(t *testing.T) {
	t.Parallel()
	for _, started := range []string{
		`{"type":"auto_retry_start","attempt":0,"maxAttempts":3,"delayMs":2000}`,
		`{"type":"auto_retry_start","attempt":-1,"maxAttempts":3,"delayMs":2000}`,
		`{"type":"auto_retry_start","attempt":1.5,"maxAttempts":3,"delayMs":2000}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":0,"delayMs":2000}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":null}`,
		`{"type":"auto_retry_start","attempt":1,"maxAttempts":3,"delayMs":-1}`,
	} {
		t.Run(started, func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
			a.HandleOutput([]byte(`{"type":"agent_start"}`))
			a.HandleOutput([]byte(piRetryAttemptEnd))
			a.HandleOutput([]byte(started))
			a.HandleOutput([]byte(piRetryCancelled))
			a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			a.Mu.Lock()
			assert.False(t, a.currentTurnActive)
			a.Mu.Unlock()
		})
	}
}

func TestPiMalformedSettlementKeepsTheValidCancellationMarker(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	startPiRetryBackoff(a)
	a.HandleOutput([]byte(piRetryCancelled))
	a.handlePiAgentSettled([]byte(`{"type":null}`), a.Clock().Now())
	a.handlePiAgentSettled([]byte(`{"type":"agent_start"}`), a.Clock().Now())
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive)
	a.Mu.Unlock()
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive, "a later valid event still consumes its own marker")
	a.Mu.Unlock()
}

func TestPiNextTurnStartsAfterTheCancelledRetrySettles(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	startPiRetryBackoff(a)
	a.Mu.Lock()
	oldGeneration := a.turnGeneration
	a.Mu.Unlock()
	a.HandleOutput([]byte(piRetryCancelled))
	// The native settlement precedes the next turn.
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive, "the old settlement must not end the replacement")
	assert.Greater(t, a.turnGeneration, oldGeneration, "the cancelled retry makes the later start a replacement")
	assert.False(t, a.turnStartedAt.IsZero())
	a.Mu.Unlock()
}

func TestPiSuccessfulRetryKeepsItsTurnOpenUntilTheFinalEnd(t *testing.T) {
	t.Parallel()
	a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
	startPiRetryBackoff(a)
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"auto_retry_end","success":true,"attempt":1}`))
	a.Mu.Lock()
	assert.True(t, a.currentTurnActive)
	a.Mu.Unlock()
	a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"stop"}]}`))
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive)
	a.Mu.Unlock()
}

func TestPiExhaustedRetriesKeepTheFinalNativeFailure(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := newPiAgentWithSink(agent.NewProviderServices(sink))
	startPiRetryBackoff(a)
	a.HandleOutput([]byte(`{"type":"agent_start"}`))
	a.HandleOutput([]byte(`{"type":"agent_end","willRetry":false,"messages":[{"role":"assistant","stopReason":"error","errorMessage":"500 retry limit"}]}`))
	count := sink.MessageCount()
	a.HandleOutput([]byte(`{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"500 retry limit"}`))
	a.HandleOutput([]byte(`{"type":"agent_settled"}`))
	assert.Zero(t, count, "the divider waits for settlement")
	require.Equal(t, 1, sink.MessageCount())
	assert.True(t, sink.Messages()[0].TurnEnd)
	assert.Contains(t, string(sink.Messages()[0].Content), "500 retry limit")
	assert.Equal(t, 2, sink.NotificationCount(), "the native retry end still adds its notice")
	a.Mu.Lock()
	assert.False(t, a.currentTurnActive)
	a.Mu.Unlock()
}

func TestPiRetryNoticesDoNotReplaceNativeSettlement(t *testing.T) {
	t.Parallel()
	for _, ended := range []string{
		`{"type":"auto_retry_end","attempt":1,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":null,"attempt":1,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":"false","attempt":1,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":0,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":-1,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":1.5,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":9223372036854775808,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":2,"finalError":"Retry cancelled"}`,
		`{"type":"auto_retry_end","success":false,"attempt":1,"finalError":"An unrelated failure"}`,
	} {
		t.Run(ended, func(t *testing.T) {
			t.Parallel()
			a := newPiAgentWithSink(agent.NewProviderServices(&agenttest.ControlSink{}))
			startPiRetryBackoff(a)
			a.HandleOutput([]byte(ended))
			a.Mu.Lock()
			assert.True(t, a.currentTurnActive, "unmatched evidence cannot end the retry turn")
			a.Mu.Unlock()
			a.HandleOutput([]byte(`{"type":"agent_settled"}`))
			a.Mu.Lock()
			assert.False(t, a.currentTurnActive)
			a.Mu.Unlock()
		})
	}
}
