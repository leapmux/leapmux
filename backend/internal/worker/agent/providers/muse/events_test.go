package muse

import (
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
				sink: a.sink, items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("replacement"),
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
				items: make(map[string]*itemState), completed: make(map[string]bool), log: newNativeLog("child-session"),
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
	assert.Equal(t, 1, sink.NotificationCount())
	notification := sink.LastNotification()
	assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, notification.Source)
	assert.Equal(t, raw, notification.Content)
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.TurnActiveCalls)
	require.Empty(t, sink.Goals())
}
