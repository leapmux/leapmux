package reasonix

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Reasonix reports its whole status on every change, so the same phase and the
// same totals arrive many times per turn. Only what MOVED says anything.
func TestReasonixStatusUpdateReportsUsageAndPhaseChanges(t *testing.T) {
	t.Parallel()

	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{
		"phase":"implementing",
		"usage":{"cumulative":{"totalTokens":1500,"promptTokens":1200,"completionTokens":300,
		"cacheHitTokens":800,"cacheMissTokens":400,"estimatedCost":0.0042,"currency":"USD"}}}}`))

	// The native usage_update owns the context gauge; the vendor snapshot supplies cost only.
	last := sink.LastSessionInfo()
	assert.NotContains(t, last, contracts.SessionInfoKeyContextUsage)
	assert.Equal(t, 0.0042, last[contracts.SessionInfoKeyTotalCostUsd])
	// A working phase says what every other surface already says.
	assert.Empty(t, sink.Notifications())

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"paused"}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"paused"}}`))
	notifications := sink.Notifications()
	require.Len(t, notifications, 1, "a restated phase states nothing new")
	assert.Equal(t, contracts.NotificationTypeAgentStatus, notifications[0]["type"])
	assert.Equal(t, "Paused", notifications[0]["text"])
}

func TestReasonixCumulativeUsageDoesNotReplaceTheContextGauge(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")
	a.HandleUpdateForTest(json.RawMessage(`{"sessionUpdate":"usage_update","used":12040,"size":128000}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"usage":{"cumulative":{"promptTokens":90000,"completionTokens":500,"cacheHitTokens":30000,"cacheMissTokens":60000,"estimatedCost":0.42,"currency":"USD"}}}}`))
	values := sink.SessionInfoValues(contracts.SessionInfoKeyContextUsage)
	require.Len(t, values, 1, "cumulative usage must not overwrite the current request's context")
	assert.Equal(t, int64(12040), values[0].(map[string]interface{})[contracts.ContextUsageFieldInputTokens])
	assert.Equal(t, 0.42, sink.LastSessionInfo()[contracts.SessionInfoKeyTotalCostUsd])
}

func TestReasonixUsageDoesNotLabelOtherCurrenciesAsUSD(t *testing.T) {
	t.Parallel()
	for _, currency := range []string{"CNY", "EUR", ""} {
		t.Run(currency, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			a.handleReasonixStatusUpdate(json.RawMessage(`{"status":{"usage":{"cumulative":{"estimatedCost":12,"currency":"` + currency + `"}}}}`))
			assert.Empty(t, sink.SessionInfoValues(contracts.SessionInfoKeyTotalCostUsd))
		})
	}
}

// The permission wait has its own card and waiting_input is the goal card's
// own state, so neither phase writes a row.
func TestReasonixPermissionAndInputPhasesStateNothing(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")

	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"waiting_permission"}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","status":{"phase":"waiting_input"}}`))

	assert.Empty(t, sink.Notifications())
}

func TestReasonixUsagePreservesZeroAndRejectsInvalidCosts(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		cost string
		want []any
	}{
		{"zero", "0", []any{float64(0), float64(0)}},
		{"positive", "0.42", []any{0.42, 0.42}},
		{"negative", "-1", nil},
		{"absent", "null", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := &Agent{}
			a.SetSinkForTest(agent.NewProviderServices(sink))
			for range 2 {
				a.handleReasonixStatusUpdate(json.RawMessage(`{"status":{"usage":{"cumulative":{"estimatedCost":` + tc.cost + `,"currency":"USD"}}}}`))
			}
			assert.Equal(t, tc.want, sink.SessionInfoValues(contracts.SessionInfoKeyTotalCostUsd))
		})
	}
}

// Native MCP health updates produce no conversation rows or notifications.
func TestReasonixMCPHealthUpdateStaysOutOfTheConversation(t *testing.T) {
	t.Parallel()
	a, sink := newReasonixNoticeAgent(t)
	a.HooksForTest().ExtraMethod = a.handleExtraMethod
	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"_reasonix.io/session/mcpStatus_update","params":{"schemaVersion":1,"sessionId":"session-1","servers":[]}}`))
	assert.Empty(t, sink.Messages(), "native MCP health is session metadata, not a chat row")
	assert.Empty(t, sink.PersistedNotifications())
}

func TestReasonixStatusRejectsOlderSnapshots(t *testing.T) {
	t.Parallel()
	sink := &agenttest.ControlSink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")
	a.ObserveCurrentMode("goal")
	a.handleReasonixStatusUpdate(json.RawMessage(`{"schemaVersion":1,"sessionId":"session","sequence":12,"status":{"schemaVersion":1,"sequence":12,"mode":"goal","phase":"paused","goal":{"status":"none","objective":"Retain this goal","runtime":{"stopCause":"manual","turnsUsed":2}},"usage":{"cumulative":{"estimatedCost":0.42,"currency":"USD"}}}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"schemaVersion":1,"sessionId":"session","sequence":11,"status":{"schemaVersion":1,"sequence":11,"mode":"goal","phase":"review_ready","goal":{"status":"running","objective":"Retain this goal","runtime":{"turnsUsed":1}},"usage":{"cumulative":{"estimatedCost":0.1,"currency":"USD"}}}}`))
	require.Len(t, sink.Goals(), 1)
	goal, present := sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.EqualValues(t, 2, *goal.Iterations)
	assert.Equal(t, []any{0.42}, sink.SessionInfoValues(contracts.SessionInfoKeyTotalCostUsd))
	assert.Len(t, sink.Notifications(), 1)
}

func TestReasonixStatusAcceptsEqualSequencesAndResetsForANewSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","sequence":12,"status":{"mode":"goal","goal":{"status":"none"}}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","sequence":12,"status":{"mode":"goal","goal":{"status":"running","objective":"Accepted at the same sequence"}}}`))
	goal, present := sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "Accepted at the same sequence", goal.Objective)
	a.SetSessionIDForTest("next")
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"next","sequence":0,"status":{"mode":"goal","goal":{"status":"running","objective":"New session"}}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","sequence":99,"status":{"mode":"goal","goal":{"status":"running","objective":"Old session"}}}`))
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"next","sequence":1,"status":{"mode":"goal","goal":{"status":"running","objective":"New session advanced"}}}`))
	require.Len(t, sink.Goals(), 3)
	goal, present = sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "New session advanced", goal.Objective)
}

func TestReasonixStatusCannotRestoreAGoalAfterANativeModeClear(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session")
	a.ObserveCurrentMode("goal")
	a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","sequence":12,"status":{"mode":"goal","goal":{"status":"running","objective":"Previous goal"}}}`))
	a.ObserveCurrentMode("normal")
	a.acknowledgeReasonixGoalClear()
	for _, sequence := range []string{"12", "13"} {
		a.handleReasonixStatusUpdate(json.RawMessage(`{"sessionId":"session","sequence":` + sequence + `,"status":{"mode":"goal","goal":{"status":"running","objective":"Previous goal"}}}`))
	}
	assert.Len(t, sink.Goals(), 1)
	assert.Equal(t, 1, sink.GoalClears())
}

func TestReasonixStatusNotificationsFollowSessionReplacement(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := &Agent{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("previous")
	a.HooksForTest().ModeChannel = acp.ModeChannelPermissionMode
	a.BeginSessionUpdatesForTest()
	a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: json.RawMessage(`{"sessionId":"previous","sequence":20,"status":{"mode":"goal","goal":{"status":"running","objective":"Previous session"}}}`)})
	next := json.RawMessage(`{"sessionId":"next","sequence":0,"status":{"mode":"goal","goal":{"status":"running","objective":"Next session"}}}`)
	a.handleExtraMethod(&providerkit.ParsedLine{Method: reasonixMethodStatusUpdate, Params: next})
	assert.Empty(t, sink.Goals(), "vendor notifications must wait with standard session updates")
	copy(next, []byte(`{"sessionId":"mutated"}`))
	a.SetSessionIDForTest("next")
	err := a.ApplyPermissionModeStartup(&acp.SessionResult{
		CurrentModeID: "goal", Modes: []acp.ModeInfo{{ID: "normal"}, {ID: "goal"}},
	}, agent.Options{Options: map[string]string{agent.OptionIDPermissionMode: "goal"}}, "normal", "")
	require.NoError(t, err)
	require.Len(t, sink.Goals(), 1)
	goal, present := sink.LastGoal()
	require.True(t, present)
	assert.Equal(t, "Next session", goal.Objective)
}
