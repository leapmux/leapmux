package zcode

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeManualCompactionUsesNativeSessionRequest(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	compactor, supported := any(a).(agent.ContextCompactor)
	require.True(t, supported, "ZCode must dispatch manual compaction through session/compact")
	answerZCodeRequest(t, a, stdin, "session/compact", `{"response":"","compact":{"state":"accepted"}}`)
	require.NoError(t, compactor.CompactContext())

	requests := stdin.Requests(t)
	require.Len(t, requests, 1)
	require.Equal(t, "session/compact", requests[0].Method)
	var params struct {
		SessionID string `json:"sessionId"`
		InputID   string `json:"inputId"`
	}
	require.NoError(t, json.Unmarshal(requests[0].Params, &params))
	require.Equal(t, "sess-1", params.SessionID)
	require.NotEmpty(t, params.InputID)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active)
	require.ErrorIs(t, a.SendInput("later turn", nil), agent.ErrAgentBusy)
	require.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)

	a.HandleOutput(zcodeStateLine(t, ScopeSession, "session_compacted", `{"status":"idle"}`))
	active, published = sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
	require.Empty(t, sink.Messages(), "the state.updated reason is no row; the lifecycle events own the compaction rows")

	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"operationId":"c1","status":"completed","trigger":"manual","phase":"standalone_turn",`+
			`"display":"separator","preCompactTokenCount":180000,"postCompactTokenCount":12000}`))
	require.Len(t, sink.PersistedNotifications(), 1, "the compaction lifecycle is the row")
}

func TestZCodeManualCompactionEndsTheLiveStateAtTheFinalStateReason(t *testing.T) {
	t.Parallel()
	for _, reason := range []string{contracts.ZCodeStateReasonSessionCompacted, contracts.ZCodeStateReasonSessionCompactFailed, contracts.ZCodeStateReasonSessionCompactCancelled} {
		for _, manual := range []bool{false, true} {
			sink := &agenttest.Sink{}
			a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
			if manual {
				a.compactionSessionID = "sess-1"
			}
			a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
				`{"operationId":"c1","status":"started","trigger":"auto"}`))
			a.HandleOutput(zcodeStateLine(t, ScopeSession, reason, `{"status":"idle"}`))
			assert.Equal(t, []any{map[string]any{"trigger": "auto"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))
			assert.Empty(t, a.compactionSessionID)
		}
	}
}

func TestZCodeManualCompactionRejectsInvalidReceipt(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{}`)
	require.ErrorIs(t, a.CompactContext(), agent.ErrDeliveryUncertain)
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestZCodeManualCompactionPreservesNativeFailure(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	answerZCodeRequest(t, a, stdin, MethodSessionCompact, `{"compact":{"state":"accepted"}}`)
	require.NoError(t, a.CompactContext())
	a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompactFailed, `{"status":"idle"}`))
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
	require.Empty(t, sink.Messages(), "the state.updated reason is no row; the lifecycle events own the compaction rows")
}

func TestZCodeManualCompactionRefusalReleasesTheQueue(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	refuseZCodeRequest(t, a, stdin, MethodSessionCompact, -32603, "compaction refused")
	require.ErrorContains(t, a.CompactContext(), "compaction refused")
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestZCodeManualCompactionInterruptEndsASilentSummary(t *testing.T) {
	stdin := &zcodeRecordedStdin{}
	sink := &agenttest.Sink{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	a.Mu.Lock()
	a.compactionSessionID = "sess-1"
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventSessionUpdated,
		`{"operationId":"c1","status":"started","trigger":"manual"}`))

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	require.Equal(t, 1, timer.armed)
	timer.fire()
	assert.Equal(t, []any{map[string]any{"trigger": "manual"}, nil}, sink.SessionInfoValues(contracts.SessionInfoKeyCompaction))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Empty(t, a.compactionSessionID)
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.JSONEq(t, `{"type":"`+contracts.NotificationTypeInterrupted+`"}`, string(sink.PersistedNotifications()[0].Content))
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.False(t, active)
}

func TestZCodeManualCompactionIgnoresAReplacedSessionReply(t *testing.T) {
	sink := &agenttest.Sink{}
	a := newZCodeTestAgent(t, agent.NewProviderServices(sink))
	a.Mu.Lock()
	a.sessionID = "sess-new"
	a.compactionSessionID = "sess-new"
	a.Mu.Unlock()
	a.PublishTurnActive()
	a.HandleOutput(zcodeStateLine(t, ScopeSession, contracts.ZCodeStateReasonSessionCompacted, `{"status":"idle"}`))

	a.Mu.Lock()
	defer a.Mu.Unlock()
	require.Equal(t, "sess-new", a.compactionSessionID)
	require.Empty(t, sink.Messages())
	active, published := sink.LastTurnActive()
	require.True(t, published)
	require.True(t, active)
}
