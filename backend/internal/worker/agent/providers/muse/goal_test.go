package muse

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMuseGoalActionsSendTheExactNativeCommandAndSession(t *testing.T) {
	for _, test := range []struct {
		action agent.GoalAction
		method string
	}{
		{agent.GoalActionSet, methodGoalSet},
		{agent.GoalActionClear, methodGoalClear},
		{agent.GoalActionPause, methodGoalPause},
		{agent.GoalActionResume, methodGoalResume},
	} {
		t.Run(test.method, func(t *testing.T) {
			a, _ := testAgent(t)
			requests := 0
			a.SetStdinForTest(&museTestPeer{agent: a, reply: func(method string, raw json.RawMessage) agenttest.RPCReply {
				requests++
				assert.Equal(t, test.method, method)
				var params map[string]any
				require.NoError(t, json.Unmarshal(raw, &params))
				assert.Equal(t, "session", params["sessionId"])
				if test.action == agent.GoalActionSet {
					assert.Equal(t, "Keep the native objective", params["objective"])
				} else {
					assert.NotContains(t, params, "objective")
				}
				reply, err := json.Marshal(map[string]any{"commandId": params["commandId"], "status": "accepted"})
				require.NoError(t, err)
				return agenttest.RPCReply{Result: reply}
			}})
			_, err := a.PerformGoalAction(test.action, "  Keep the native objective \n")
			require.NoError(t, err)
			assert.Equal(t, 1, requests)
		})
	}
}

func TestMuseGoalActionsRejectAnEmptyObjectiveAndUnsupportedActionBeforeAWrite(t *testing.T) {
	a, _ := testAgent(t)
	requests := 0
	a.SetStdinForTest(&museTestPeer{agent: a, reply: func(string, json.RawMessage) agenttest.RPCReply {
		requests++
		return agenttest.RPCReply{}
	}})
	for _, objective := range []string{"", " \n\t "} {
		_, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		require.ErrorContains(t, err, "objective is empty")
	}
	_, err := a.PerformGoalAction(agent.GoalAction(99), "Native objective")
	require.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	assert.Zero(t, requests)
}

/** An agent whose sink holds the open native session, as the worker's sink does. */
func goalTestAgent(t *testing.T) (*Agent, *agenttest.Sink) {
	t.Helper()
	a, sink := testAgent(t)
	sink.UpdateSessionID("session")
	return a, sink
}

func goalEventParams(sessionID string, goal any) map[string]any {
	return map[string]any{
		"sessionId":  sessionID,
		"viewCursor": "goal-cursor",
		"sourceRange": map[string]any{
			"stream": map[string]any{"kind": "session", "id": sessionID},
			"first":  map[string]any{"id": "goal-record", "sequence": 1},
			"last":   map[string]any{"id": "goal-record", "sequence": 1},
		},
		"goal": goal,
	}
}

func TestMuseGoalEventsKeepKnownAndUnknownStatesDistinct(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		status string
		want   agent.GoalStatus
	}{
		{"active", agent.GoalStatusActive},
		{"paused", agent.GoalStatusPaused},
		{"completed", agent.GoalStatusDone},
		{"satisfied", agent.GoalStatusDone},
		{"blocked", agent.GoalStatusBlocked},
		{"failed", agent.GoalStatusBlocked},
		{"futureState", agent.GoalStatus(5)},
		{" active ", agent.GoalStatus(5)},
	} {
		t.Run(scenario.status, func(t *testing.T) {
			t.Parallel()
			a, sink := goalTestAgent(t)
			feed(t, a, methodSessionGoalChanged, goalEventParams("session", map[string]any{
				"objective": "Read the native objective", "status": scenario.status, "percentComplete": 0,
			}))
			goals := sink.Goals()
			require.Len(t, goals, 1)
			assert.Equal(t, scenario.want, goals[0].Status)
			assert.Equal(t, "Read the native objective", goals[0].Objective)
			assert.Equal(t, scenario.status, goals[0].StatusDetail)
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func TestMuseGoalEventsPreserveThePreviousGoalForInvalidFields(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name   string
		goal   any
		absent bool
	}{
		{name: "absent goal", absent: true},
		{name: "string goal", goal: "native"},
		{name: "array goal", goal: []any{}},
		{name: "number goal", goal: 0},
		{name: "empty object", goal: map[string]any{}},
		{name: "absent status", goal: map[string]any{"objective": "Native objective"}},
		{name: "null status", goal: map[string]any{"objective": "Native objective", "status": nil}},
		{name: "empty status", goal: map[string]any{"objective": "Native objective", "status": ""}},
		{name: "blank status", goal: map[string]any{"objective": "Native objective", "status": " \n\t "}},
		{name: "numeric status", goal: map[string]any{"objective": "Native objective", "status": 0}},
		{name: "absent objective", goal: map[string]any{"status": "active"}},
		{name: "null objective", goal: map[string]any{"objective": nil, "status": "active"}},
		{name: "empty objective", goal: map[string]any{"objective": "", "status": "active"}},
		{name: "blank objective", goal: map[string]any{"objective": " \n\t ", "status": "active"}},
		{name: "unreadable objective", goal: map[string]any{"objective": "\x00\x1b", "status": "active"}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			a, sink := goalTestAgent(t)
			before := agent.GoalUpdate{Objective: "The previous objective", Status: agent.GoalStatusPaused}
			sink.UpsertGoal(before)
			params := goalEventParams("session", scenario.goal)
			if scenario.absent {
				delete(params, "goal")
			}
			feed(t, a, methodSessionGoalChanged, params)
			assert.Equal(t, []agent.GoalUpdate{before}, sink.Goals())
			assert.Zero(t, sink.GoalClears())
		})
	}
}

func TestMuseGoalEventsClearOnlyAnExplicitNullGoal(t *testing.T) {
	t.Parallel()
	a, sink := goalTestAgent(t)
	before := agent.GoalUpdate{Objective: "The previous objective", Status: agent.GoalStatusActive}
	sink.UpsertGoal(before)
	feed(t, a, methodSessionGoalChanged, goalEventParams("session", nil))
	assert.Equal(t, []agent.GoalUpdate{before}, sink.Goals())
	assert.Equal(t, 1, sink.GoalClears())
}

func TestMuseUnknownGoalKeepsRawBytesAndNormalizedDisplayDetail(t *testing.T) {
	t.Parallel()
	for _, scenario := range []struct {
		name   string
		status string
		detail string
	}{
		{"oversized", strings.Repeat("x", 257), strings.Repeat("x", 256)},
		{"multibyte", strings.Repeat("文", 90), strings.Repeat("文", 85)},
		{"control bytes", "fu\x00ture\x1bState", "futureState"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			t.Parallel()
			a, sink := goalTestAgent(t)
			raw := feed(t, a, methodSessionGoalChanged, goalEventParams("session", map[string]any{
				"objective": "Native objective", "status": scenario.status, "percentComplete": 0,
			}))
			goals := sink.Goals()
			require.Len(t, goals, 1)
			projection := goals[0].Clean()
			assert.Equal(t, agent.GoalStatus(5), projection.Status)
			assert.Equal(t, scenario.detail, projection.StatusDetail)
			assert.LessOrEqual(t, len(projection.StatusDetail), 256)
			assert.True(t, utf8.ValidString(projection.StatusDetail))
			require.Equal(t, 1, sink.NotificationCount())
			retained := sink.LastNotification()
			assert.Equal(t, leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT, retained.Source)
			assert.Equal(t, raw, retained.Content)
			var event struct {
				Params struct {
					Goal struct {
						Status string `json:"status"`
					} `json:"goal"`
				} `json:"params"`
			}
			require.NoError(t, json.Unmarshal(retained.Content, &event))
			assert.Equal(t, scenario.status, event.Params.Goal.Status)
		})
	}
}

type museGoalObserverSink struct {
	agent.ProviderServices
	observe func()
}

func (sink *museGoalObserverSink) UpsertGoal(update agent.GoalUpdate) {
	sink.ProviderServices.UpsertGoal(update)
	if sink.observe != nil {
		sink.observe()
	}
}

func (sink *museGoalObserverSink) ClearGoal(snapshot bool) {
	sink.ProviderServices.ClearGoal(snapshot)
	if sink.observe != nil {
		sink.observe()
	}
}

// GoalWriterFor keeps the nested-output hook on the captured path the agent
// actually drives: the deferred write goes through the writer, not the sink's
// direct methods.
func (sink *museGoalObserverSink) GoalWriterFor(captured agent.CapturedTranscript) (agent.CapturedGoalWriter, error) {
	writer, err := sink.ProviderServices.GoalWriterFor(captured)
	if err != nil {
		return nil, err
	}
	return &museGoalObserverWriter{writer: writer, observe: sink.observe}, nil
}

type museGoalObserverWriter struct {
	writer  agent.CapturedGoalWriter
	observe func()
}

func (w *museGoalObserverWriter) UpsertGoal(update agent.GoalUpdate) error {
	if w.observe != nil {
		w.observe()
	}
	return w.writer.UpsertGoal(update)
}

func (w *museGoalObserverWriter) ClearGoal() error {
	if w.observe != nil {
		w.observe()
	}
	return w.writer.ClearGoal()
}

func TestMuseGoalObserversReleaseDispatchBeforeNestedNativeOutput(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		goal       any
		wantGoals  int
		wantClears int
	}{
		{"set", map[string]any{"objective": "Keep the native goal observer", "status": "active"}, 1, 0},
		{"clear", nil, 0, 1},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			a, sink := goalTestAgent(t)
			nested, err := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/todoListChanged",
				"params": map[string]any{
					"sessionId": "session", "viewCursor": "goal-observer-todo", "revision": 1, "sourceTool": "write_todos",
					"items": []any{map[string]any{"text": "MUSE_GOAL_OBSERVER_REENTRY", "status": "pending"}},
					"sourceRange": map[string]any{
						"stream": map[string]any{"kind": "session", "id": "session"},
						"first":  map[string]any{"id": "goal-observer-todo-record", "sequence": 2},
						"last":   map[string]any{"id": "goal-observer-todo-record", "sequence": 2},
					},
				},
			})
			require.NoError(t, err)
			entered, calls := false, 0
			observer := &museGoalObserverSink{ProviderServices: a.sink}
			observer.observe = func() {
				calls++
				if !assert.True(t, a.dispatchMu.TryLock(), "the native goal observer retains the dispatch mutex") {
					return
				}
				a.dispatchMu.Unlock()
				if !assert.True(t, a.stateMu.TryLock(), "the native goal observer retains the state mutex") {
					return
				}
				a.stateMu.Unlock()
				if !assert.True(t, a.controlMu.TryLock(), "the native goal observer retains the control mutex") {
					return
				}
				a.controlMu.Unlock()
				a.HandleOutput(nested)
				entered = true
			}
			a.sink = observer
			a.sessions["session"].sink = observer
			feed(t, a, methodSessionGoalChanged, goalEventParams("session", scenario.goal))
			assert.True(t, entered, "the goal observer must enter the real nested output path")
			assert.Equal(t, 1, calls)
			assert.Len(t, sink.Goals(), scenario.wantGoals)
			assert.Equal(t, scenario.wantClears, sink.GoalClears())
			stored := 0
			for _, notification := range sink.PersistedNotifications() {
				if string(notification.Content) == string(nested) {
					stored++
				}
			}
			assert.Equal(t, 1, stored, "the nested native notification must persist exactly once")
		})
	}
}

func TestMuseGoalWriterRefusesAfterContextReplacement(t *testing.T) {
	t.Parallel()
	a, sink := goalTestAgent(t)
	// The goal observation arrives under the open session; the write runs in
	// the same flush but only after the observation. A context replacement
	// between the two installs a new native session fact on the sink -- the
	// authority fact the capture froze -- and the old native goal data must
	// not gain the new session's authority.
	replaced := false
	observer := &museGoalObserverSink{ProviderServices: a.sink}
	observer.observe = func() {
		if !replaced {
			replaced = true
			sink.UpdateSessionID("replacement")
		}
	}
	a.sink = observer
	a.sessions["session"].sink = observer
	feed(t, a, methodSessionGoalChanged, goalEventParams("session", map[string]any{"objective": "The replaced session goal", "status": "active"}))
	assert.True(t, replaced, "the observer must run between observation and write")
	assert.Empty(t, sink.Goals())
	assert.Zero(t, sink.GoalClears())
}

func TestMuseGoalWriterSurvivesLaterTurnsInTheSameSession(t *testing.T) {
	t.Parallel()
	a, sink := goalTestAgent(t)
	feed(t, a, methodSessionGoalChanged, goalEventParams("session", map[string]any{"objective": "The same-session goal", "status": "active"}))
	// Ordinary later turns in the same native session change no authority
	// fact; the deferred write still lands.
	a.Mu.Lock()
	a.TurnToolUses++
	a.Mu.Unlock()
	for _, action := range a.takeDeferredGoalActions() {
		action()
	}
	require.Len(t, sink.Goals(), 1)
	assert.Equal(t, "The same-session goal", sink.Goals()[0].Objective)
}
