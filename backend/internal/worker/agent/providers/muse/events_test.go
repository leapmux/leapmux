package muse

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseForeignSessionEventsDoNotChangeRootState(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	goal := agent.GoalUpdate{Objective: "The current objective", Status: agent.GoalStatusActive}
	sink.UpsertGoal(goal)
	settings := a.SettingsSnapshot().SurfacedOptions
	for _, event := range []struct {
		method string
		params map[string]any
	}{
		{methodSessionModelChanged, map[string]any{"sessionId": "foreign", "modelId": "foreign-model"}},
		{methodSessionEffortChanged, map[string]any{"sessionId": "foreign", "reasoningEffort": "high"}},
		{methodSessionApprovalChanged, map[string]any{"sessionId": "foreign", "mode": contracts.MuseApprovalModeAllowAll}},
		{methodSessionGoalChanged, goalEventParams("foreign", map[string]any{"objective": "Foreign objective", "status": "paused", "percentComplete": 30})},
		{contracts.MuseMethodContextUsage, map[string]any{"sessionId": "foreign", "usedTokens": 20, "windowTokens": 100}},
	} {
		feed(t, a, event.method, event.params)
	}
	assert.Equal(t, settings, a.SettingsSnapshot().SurfacedOptions)
	assert.Equal(t, []agent.GoalUpdate{goal}, sink.Goals())
	assert.Zero(t, sink.GoalClears())
	assert.Zero(t, sink.SettingsRefreshCount())
	assert.Zero(t, sink.SessionInfoCount())
	assert.Zero(t, sink.NotificationCount())
}

func TestMuseRetiredSessionEventsDoNotChangeReplacementState(t *testing.T) {
	for _, event := range []struct {
		method string
		params map[string]any
	}{
		{methodSessionModelChanged, map[string]any{"sessionId": "session", "modelId": "retired-model"}},
		{methodSessionEffortChanged, map[string]any{"sessionId": "session", "reasoningEffort": "high"}},
		{methodSessionApprovalChanged, map[string]any{"sessionId": "session", "mode": contracts.MuseApprovalModeAllowAll}},
		{methodSessionGoalChanged, goalEventParams("session", map[string]any{"objective": "Retired objective", "status": "paused", "percentComplete": 30})},
		{contracts.MuseMethodContextUsage, map[string]any{"sessionId": "session", "usedTokens": 20, "windowTokens": 100}},
	} {
		t.Run(event.method, func(t *testing.T) {
			a, sink := testAgent(t)
			a.sessions["session"].retired = true
			a.sessionID = "replacement"
			a.sessions["replacement"] = &sessionState{
				sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog("replacement"),
			}
			goal := agent.GoalUpdate{Objective: "The replacement objective", Status: agent.GoalStatusActive}
			sink.UpsertGoal(goal)
			settings := a.SettingsSnapshot().SurfacedOptions

			feed(t, a, event.method, event.params)
			assert.Equal(t, settings, a.SettingsSnapshot().SurfacedOptions)
			assert.Equal(t, []agent.GoalUpdate{goal}, sink.Goals())
			assert.Zero(t, sink.GoalClears())
			assert.Zero(t, sink.SettingsRefreshCount())
			assert.Zero(t, sink.SessionInfoCount())
			assert.Empty(t, sink.TurnActiveCalls)
		})
	}
}

func TestMuseChildGoalEventsDoNotChangeTheRootGoal(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name string
		goal any
	}{
		{"write", map[string]any{"objective": "Child objective", "status": "active", "percentComplete": 0}},
		{"clear", nil},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			a, sink := testAgent(t)
			before := agent.GoalUpdate{Objective: "The root objective", Status: agent.GoalStatusActive}
			sink.UpsertGoal(before)
			a.sessions["child-session"] = &sessionState{
				sink: a.sink, childID: "native-child", childKey: "native-key", parentSessionID: "session",
				items: make(map[string]*itemState), completed: make(map[string]turnCompletionState), log: newNativeLog("child-session"),
			}
			feed(t, a, methodSessionGoalChanged, goalEventParams("child-session", scenario.goal))
			assert.Equal(t, []agent.GoalUpdate{before}, sink.Goals())
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func TestMuseUnknownNotificationPreservesItsExactNativeBytes(t *testing.T) {
	t.Parallel()
	a, sink := testAgent(t)
	raw := feed(t, a, "future/nativeEvent", map[string]any{
		"sessionId": "session", "viewCursor": "future-cursor", "counter": 0, "text": " \n Native text 文 \n ",
	})
	require.Len(t, sink.Messages(), 1)
	notification := sink.Messages()[0]
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, notification.Source)
	assert.Equal(t, raw, notification.Content)
	assert.Empty(t, sink.PersistedNotifications())
	assert.Empty(t, sink.TurnActiveCalls)
	require.Empty(t, sink.Goals())
}

func TestMuseMethodVerdictsStoreNothing(t *testing.T) {
	for _, method := range []string{
		"session/branchChanged", "session/nameChanged", "session/permissionProfileChanged", "session/viewHealthChanged",
		"turn/retracted", "turn/foregroundCompleted", "skill/changed", "account/changed", "account/loginCompleted",
		"mcpServer/oauthLoginCompleted", "session/listChanged", "session/started", "session/closed", "session/statusChanged",
	} {
		t.Run(method, func(t *testing.T) {
			a, sink := testAgent(t)
			feed(t, a, method, map[string]any{"sessionId": "session"})
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
			assert.Empty(t, sink.TurnLifecycle())
		})
	}
}

func TestMuseTurnUnqueuedEndsTheMatchingTurn(t *testing.T) {
	for _, turnID := range []string{"turn", "other", ""} {
		t.Run(turnID, func(t *testing.T) {
			a, sink := testAgent(t)
			a.startTurn("session", "turn")
			feed(t, a, "turn/unqueued", map[string]any{"sessionId": "session", "turnId": turnID})
			if turnID == "turn" {
				assert.Empty(t, a.sessions["session"].turnID)
				assert.Equal(t, []bool{true, false}, sink.TurnActiveCalls)
			} else {
				assert.Equal(t, "turn", a.sessions["session"].turnID)
				assert.Equal(t, []bool{true}, sink.TurnActiveCalls)
			}
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestMuseLogGapMarksTheLogUnavailable(t *testing.T) {
	for _, positions := range []string{`{}`, `{"after":null,"next":null}`, `{"after":5,"next":{}}`, `{"after":{"id":"a","sequence":-1},"next":{"id":"b","sequence":2}}`} {
		t.Run(positions, func(t *testing.T) {
			a, sink := testAgent(t)
			a.sessions["session"].log.subscriptionID = 7
			params := map[string]any{}
			require.NoError(t, json.Unmarshal([]byte(positions), &params))
			params["subscriptionId"] = 7
			feed(t, a, "log/gap", params)
			assert.Equal(t, "the Muse log reported a gap", a.sessions["session"].log.unavailable)
			assert.Empty(t, sink.Messages())
			assert.Empty(t, sink.PersistedNotifications())
		})
	}
}

func TestMuseLogGapOfAnotherSubscriptionDoesNothing(t *testing.T) {
	a, _ := testAgent(t)
	a.sessions["session"].log.subscriptionID = 7
	for _, id := range []int{0, -1, 8} {
		feed(t, a, "log/gap", map[string]any{"subscriptionId": id})
	}
	assert.Empty(t, a.sessions["session"].log.unavailable)
}

func TestMuseModelRouteUnservedPersists(t *testing.T) {
	a, sink := testAgent(t)
	raw := feed(t, a, "session/modelRouteUnserved", map[string]any{"sessionId": "session", "installedProviderId": "login", "modelId": "model"})
	require.Len(t, sink.PersistedNotifications(), 1)
	assert.Equal(t, raw, sink.PersistedNotifications()[0].Content)
}

func TestMuseMalformedSettingEventKeepsTheCurrentSetting(t *testing.T) {
	for _, event := range []struct{ method, field string }{
		{methodSessionModelChanged, "modelId"},
		{methodSessionEffortChanged, "reasoningEffort"},
		{methodSessionApprovalChanged, "mode"},
	} {
		for _, value := range []any{nil, 0, true, []any{}, map[string]any{}} {
			a, sink := testAgent(t)
			before := a.SettingsSnapshot().SurfacedOptions
			feed(t, a, event.method, map[string]any{"sessionId": "session", event.field: value})
			assert.Equal(t, before, a.SettingsSnapshot().SurfacedOptions)
			assert.Zero(t, sink.SettingsRefreshCount())
		}
	}
}
