package kilo

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/opencode/opencodetest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newKiloGoalAgent(sink *agenttest.Sink) *Agent {
	a := newAgent()
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session-1")
	return a
}

// kiloGoalEvent builds one session.updated properties payload whose info
// carries the supplied session id and metadata.
func kiloGoalEvent(t *testing.T, sessionID string, metadata any) (string, json.RawMessage) {
	t.Helper()
	properties, err := json.Marshal(map[string]any{
		"sessionID": sessionID,
		"info":      map[string]any{"id": sessionID, "metadata": metadata},
	})
	require.NoError(t, err)
	return opencode.EventSessionUpdated, properties
}

// The statuses and the fallbacks come from Kilo's own GoalState.read
// (kilo/session/goal/state.ts): a known word wins, then active:true, then
// paused. goal-1.log supplies the active frame at line 63 and the paused frame
// with its reason at line 109.
func TestKiloGoal_ReportsTheNativeGoalFromSessionMetadata(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		goal any
		want agent.GoalUpdate
	}{
		{
			name: "active, from goal-1.log line 63",
			goal: map[string]any{"text": "GOAL-PROBE reply with the single word OK", "status": "active", "active": true},
			want: agent.GoalUpdate{Objective: "GOAL-PROBE reply with the single word OK", Status: agent.GoalStatusActive},
		},
		{
			name: "paused keeps Kilo's reason, from goal-1.log line 109",
			goal: map[string]any{"text": "GOAL-PROBE reply with the single word OK", "status": "paused", "active": false, "reason": "No successful action or explicit completion report. Review the conversation before resuming."},
			want: agent.GoalUpdate{Objective: "GOAL-PROBE reply with the single word OK", Status: agent.GoalStatusPaused, StatusDetail: "No successful action or explicit completion report. Review the conversation before resuming."},
		},
		{
			name: "complete",
			goal: map[string]any{"text": "Ship the release", "status": "complete"},
			want: agent.GoalUpdate{Objective: "Ship the release", Status: agent.GoalStatusDone},
		},
		{
			name: "blocked",
			goal: map[string]any{"text": "Ship the release", "status": "blocked", "reason": "missing credentials"},
			want: agent.GoalUpdate{Objective: "Ship the release", Status: agent.GoalStatusBlocked, StatusDetail: "missing credentials"},
		},
		{
			name: "waiting reports active with the wait label",
			goal: map[string]any{"text": "Wait for the deploy", "status": "waiting", "wait": map[string]any{"kind": "cron", "id": "nightly", "label": "Nightly build"}},
			want: agent.GoalUpdate{Objective: "Wait for the deploy", Status: agent.GoalStatusActive, StatusDetail: "waiting: Nightly build"},
		},
		{
			name: "a wait label defaults to its id",
			goal: map[string]any{"text": "Wait for the deploy", "status": "waiting", "wait": map[string]any{"kind": "wakeup", "id": "wakeup-7"}},
			want: agent.GoalUpdate{Objective: "Wait for the deploy", Status: agent.GoalStatusActive, StatusDetail: "waiting: wakeup-7"},
		},
		{
			name: "an invalid wait keeps the reason instead",
			goal: map[string]any{"text": "Wait for the deploy", "status": "waiting", "reason": "armed no wait", "wait": map[string]any{"kind": "later", "id": "wakeup-7"}},
			want: agent.GoalUpdate{Objective: "Wait for the deploy", Status: agent.GoalStatusActive, StatusDetail: "armed no wait"},
		},
		{
			name: "an absent status with active true falls back to active",
			goal: map[string]any{"text": "Legacy frame", "active": true},
			want: agent.GoalUpdate{Objective: "Legacy frame", Status: agent.GoalStatusActive},
		},
		{
			name: "an absent status with active false falls back to paused",
			goal: map[string]any{"text": "Legacy frame", "active": false},
			want: agent.GoalUpdate{Objective: "Legacy frame", Status: agent.GoalStatusPaused},
		},
		{
			name: "an unknown status word falls back the same way",
			goal: map[string]any{"text": "Future word", "status": "hibernating", "active": true},
			want: agent.GoalUpdate{Objective: "Future word", Status: agent.GoalStatusActive},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			sink := &agenttest.Sink{}
			a := newKiloGoalAgent(sink)
			eventType, properties := kiloGoalEvent(t, "session-1", map[string]any{"kilo.goal": testCase.goal})
			a.MemberEvents(eventType, properties)
			goal, ok := sink.LastGoal()
			require.True(t, ok, "the native metadata report must reach the goal")
			assert.Equal(t, testCase.want, goal)
			assert.Zero(t, sink.GoalClears())
		})
	}
}

// goal-1.log line 120 clears the goal by writing an empty metadata object.
// Each malformed report means the same: the session holds no goal.
func TestKiloGoal_ClearsWhenTheMetadataHoldsNoGoal(t *testing.T) {
	t.Parallel()
	for _, metadata := range []any{
		map[string]any{},
		nil,
		map[string]any{"kilo.goal": nil},
		map[string]any{"kilo.goal": "paused"},
		map[string]any{"kilo.goal": map[string]any{"text": "   "}},
		map[string]any{"kilo.goal": map[string]any{"status": "active"}},
	} {
		sink := &agenttest.Sink{}
		a := newKiloGoalAgent(sink)
		eventType, properties := kiloGoalEvent(t, "session-1", metadata)
		a.MemberEvents(eventType, properties)
		assert.Equal(t, 1, sink.GoalClears(), "metadata %v must clear the goal", metadata)
		assert.Empty(t, sink.Goals())
	}
}

// A report that names another session changes nothing: the handler reads the
// session that the info object carries, not only the one the envelope states.
func TestKiloGoal_IgnoresAReportOfAnotherSession(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	a := newKiloGoalAgent(sink)
	eventType, properties := kiloGoalEvent(t, "session-2", map[string]any{
		"kilo.goal": map[string]any{"text": "Another session goal", "status": "active"},
	})
	a.MemberEvents(eventType, properties)
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// The daemon bridge reaches the goal reader through the agent constructor's
// binding, and the family filter keeps another session's event away from it.
// goal-1.log supplies the whole sequence: line 63 sets the goal, line 109
// pauses it with a reason, and line 120 clears it.
func TestKiloGoal_ReportsThroughTheDaemonBridge(t *testing.T) {
	t.Parallel()
	daemon := opencodetest.NewFakeDaemon(t, opencode.EventRoute, opencode.QuestionRoot)
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	sink := &agenttest.Sink{}
	a := newAgent()
	a.SetSinkForTest(agent.NewProviderServices(sink))
	a.SetSessionIDForTest("session-1")
	var reports atomic.Int64
	inner := a.MemberEvents
	a.MemberEvents = func(eventType string, properties json.RawMessage) {
		inner(eventType, properties)
		reports.Add(1)
	}
	a.BeginQuestionsForTest(ctx, a.Sink(), "agent-1", daemon.Server.URL)
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-other\",\"info\":{\"id\":\"session-other\",\"metadata\":{\"kilo.goal\":{\"text\":\"Another session goal\",\"status\":\"active\"}}}}}\n\n")
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"session-1\",\"metadata\":{\"kilo.goal\":{\"text\":\"GOAL-PROBE reply with the single word OK\",\"status\":\"active\",\"active\":true}}}}}\n\n")
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"session-1\",\"metadata\":{\"kilo.goal\":{\"text\":\"GOAL-PROBE reply with the single word OK\",\"status\":\"paused\",\"active\":false,\"reason\":\"No successful action or explicit completion report. Review the conversation before resuming.\"}}}}}\n\n")
	daemon.Send("data: {\"type\":\"session.updated\",\"properties\":{\"sessionID\":\"session-1\",\"info\":{\"id\":\"session-1\",\"metadata\":{}}}}\n\n")
	require.Eventually(t, func() bool { return reports.Load() == 3 }, time.Second, time.Millisecond)
	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalUpdate{
		Objective:    "GOAL-PROBE reply with the single word OK",
		Status:       agent.GoalStatusPaused,
		StatusDetail: "No successful action or explicit completion report. Review the conversation before resuming.",
	}, goal)
	assert.Equal(t, 1, sink.GoalClears(), "the empty metadata at goal-1.log line 120 clears the goal")
}

// Kilo's `/goal` is the one text route that carries all four verbs. The command
// is Kilo's own addition to the OpenCode base it forks, which is why OpenCode
// has none. The verbs still queue for delivery; the goal itself now comes from
// the native session metadata, so no delivery observer exists.
func TestKiloGoal_QueuesTheAdvertisedGoalCommandForEveryVerb(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	// No current session: the advertised-command frame names no session, and
	// the dispatcher refuses an unowned update once a session is current.
	ag := newAgent()
	ag.SetSinkForTest(agent.NewProviderServices(sink))

	assert.Empty(t, ag.SupportedGoalActions(), "a build that lists no goal command offers no goal control")
	acptest.AdvertiseCommands(t, ag, "compact", "goal")
	assert.Equal(t,
		[]agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume},
		ag.SupportedGoalActions())

	setOutcome, err := ag.PerformGoalAction(agent.GoalActionSet, "  keep\n NOTES.md accurate ")
	require.NoError(t, err)
	assert.Equal(t, "/goal keep NOTES.md accurate", setOutcome.QueuedInput)

	pauseOutcome, err := ag.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal pause", pauseOutcome.QueuedInput)

	resumeOutcome, err := ag.PerformGoalAction(agent.GoalActionResume, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal resume", resumeOutcome.QueuedInput)

	clearOutcome, err := ag.PerformGoalAction(agent.GoalActionClear, "")
	require.NoError(t, err)
	assert.Equal(t, "/goal clear", clearOutcome.QueuedInput)

	// No command changes local state: only the native metadata report does.
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

// The command is positional, so a one-word objective that equals a verb would reach Kilo
// as that verb. Refuse it rather than queue a goal Kilo does not install.
func TestKiloGoal_RefusesAnObjectiveThatReadsAsAVerb(t *testing.T) {
	t.Parallel()
	a := newKiloGoalAgent(&agenttest.Sink{})
	for _, objective := range []string{"pause", "Resume", "CLEAR"} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.ErrorIs(t, err, agent.ErrGoalObjectiveIsCommand, "objective %q", objective)
	}
}
