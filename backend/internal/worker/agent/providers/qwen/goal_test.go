package qwen

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// advertiseCommands delivers the command set of the test session.
func advertiseCommands(t *testing.T, a *Agent, names ...string) {
	t.Helper()
	commands := make([]any, len(names))
	for i, name := range names {
		commands[i] = map[string]any{"name": name}
	}
	a.HandleOutput(sessionUpdate(t, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": commands}))
}

// goalState is one goal state, on the empty message that carries it.
func goalState(t *testing.T, goal map[string]any, activity string) []byte {
	t.Helper()
	state := map[string]any{"v": 2, "activity": activity}
	if goal != nil {
		state["goal"] = goal
	} else {
		state["goal"] = nil
	}
	return metaChunk(t, "", map[string]any{"goalState": state})
}

func TestQwenOffersGoalsOnlyWithTheGoalCommand(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	assert.Empty(t, a.SupportedGoalActions())
	_, err := a.PerformGoalAction(agent.GoalActionSet, "Ship it")
	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)

	advertiseCommands(t, a, "compress", "goal")

	assert.Equal(t, []agent.GoalAction{agent.GoalActionSet, agent.GoalActionClear, agent.GoalActionPause, agent.GoalActionResume}, a.SupportedGoalActions())
	assert.Equal(t, 1, sink.GoalCapabilityPublishes())
}

// Each action is the request that Qwen's own `/goal` command hands to its goal
// runtime, sent through the goal control, and it queues nothing. A set creates
// a goal when the session has none, and replaces the current one otherwise.
// The objective reaches Qwen as the user wrote it, trimmed: a request has no
// verb to confuse with it, so "pause" and a line break stay objective text.
func TestQwenGoalActionsUseTheGoalControl(t *testing.T) {
	t.Parallel()
	const current = `{"goalId":"g-1","revision":3,"objective":"Ship","status":"paused"}`
	for _, tc := range []struct {
		name      string
		goal      string
		action    agent.GoalAction
		objective string
		want      map[string]any
	}{
		{name: "a set with no goal", goal: "null", action: agent.GoalActionSet, objective: "  Ship the\nrelease  ",
			want: map[string]any{"action": "create", "objective": "Ship the\nrelease"}},
		{name: "a set of a verb word", goal: "null", action: agent.GoalActionSet, objective: "pause",
			want: map[string]any{"action": "create", "objective": "pause"}},
		{name: "a set over a goal", goal: current, action: agent.GoalActionSet, objective: "Ship again",
			want: map[string]any{"action": "replace", "objective": "Ship again", "expectedGoalId": "g-1", "expectedRevision": float64(3)}},
		{name: "a pause", goal: current, action: agent.GoalActionPause,
			want: map[string]any{"action": "pause", "expectedGoalId": "g-1", "expectedRevision": float64(3)}},
		{name: "a resume", goal: current, action: agent.GoalActionResume,
			want: map[string]any{"action": "resume", "expectedGoalId": "g-1", "expectedRevision": float64(3)}},
		{name: "a clear", goal: current, action: agent.GoalActionClear,
			want: map[string]any{"action": "clear", "expectedGoalId": "g-1", "expectedRevision": float64(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newQwenAgent(t, nil, qwenGoalPeer(tc.goal))
			advertiseCommands(t, a, "goal")

			outcome, err := a.PerformGoalAction(tc.action, tc.objective)
			require.NoError(t, err)
			assert.Empty(t, outcome.QueuedInput)
			syncPeer(t, a)
			reads := requestsFor(requests(), "qwen/control/session/goal/get")
			require.Len(t, reads, 1, "the action reads the goal that it names")
			assert.Equal(t, map[string]any{"sessionId": qwenTestSession}, reads[0].Params)
			sent := requestsFor(requests(), "qwen/control/session/goal/control")
			require.Len(t, sent, 1)
			assert.Equal(t, map[string]any{"sessionId": qwenTestSession, "request": tc.want}, sent[0].Params)
			assert.Empty(t, requestsFor(requests(), "session/prompt"), "no action is a prompt")
		})
	}
}

// A clear of a session with no goal has nothing to clear, as Qwen's own
// command answers. A pause or a resume of no goal is refused, and sends nothing.
func TestQwenGoalActionWithNoGoal(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		action  agent.GoalAction
		wantErr string
	}{
		{name: "clear", action: agent.GoalActionClear},
		{name: "pause", action: agent.GoalActionPause, wantErr: "there is no goal to pause"},
		{name: "resume", action: agent.GoalActionResume, wantErr: "there is no goal to resume"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newQwenAgent(t, nil, qwenGoalPeer("null"))
			advertiseCommands(t, a, "goal")

			outcome, err := a.PerformGoalAction(tc.action, "")
			if tc.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, tc.wantErr)
			}
			assert.Empty(t, outcome.QueuedInput)
			syncPeer(t, a)
			assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/control"))
		})
	}
}

// A goal read that fails, or that states no goal snapshot, refuses the action
// before any control request. A refused control request reaches the caller
// with the cause that Qwen states in the error's data.
func TestQwenGoalActionFailures(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name        string
		get         agenttest.RPCReply
		control     agenttest.RPCReply
		wantErr     string
		wantControl int
	}{
		{name: "a failed read", get: agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"Internal error","data":{"details":"no runtime"}}`)},
			wantErr: "read the Qwen goal: json-rpc error -32603: Internal error: {\"details\":\"no runtime\"}"},
		{name: "a read with no snapshot", get: agenttest.RPCReply{Result: json.RawMessage(`{}`)},
			wantErr: "the answer states no goal snapshot"},
		{name: "a read of a goal with no revision", get: agenttest.RPCReply{Result: json.RawMessage(`{"snapshot":{"v":2,"goal":{"goalId":"g-1"}}}`)},
			wantErr: "the goal states no id or revision"},
		{name: "a refused control",
			get:         agenttest.RPCReply{Result: json.RawMessage(`{"snapshot":{"v":2,"goal":{"goalId":"g-1","revision":1,"status":"active"}}}`)},
			control:     agenttest.RPCReply{Error: json.RawMessage(`{"code":-32009,"message":"Goal version does not match the current session Goal","data":{"errorKind":"goal_conflict"}}`)},
			wantErr:     `qwen goal pause: json-rpc error -32009: Goal version does not match the current session Goal: {"errorKind":"goal_conflict"}`,
			wantControl: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newQwenAgent(t, nil, func(request agenttest.RecordedRequest) agenttest.RPCReply {
				switch request.Method {
				case "qwen/control/session/goal/get":
					return tc.get
				case "qwen/control/session/goal/control":
					return tc.control
				}
				return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
			})
			advertiseCommands(t, a, "goal")

			_, err := a.PerformGoalAction(agent.GoalActionPause, "")
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantErr)
			syncPeer(t, a)
			assert.Len(t, requestsFor(requests(), "qwen/control/session/goal/control"), tc.wantControl)
		})
	}
}

// An action that the provider does not list sends no request: it does not read
// the goal either, because the read serves an action that Qwen can run.
func TestQwenGoalActionRefusesAnUnknownAction(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenGoalPeer(`{"goalId":"g-1","revision":1,"status":"active"}`))
	advertiseCommands(t, a, "goal")

	_, err := a.PerformGoalAction(agent.GoalAction(99), "")

	assert.ErrorIs(t, err, agent.ErrGoalControlUnsupported)
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/get"))
	assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/control"))
}

// An agent that cannot carry the action fails at once with the plain cause, as
// a prompt of the base does. Qwen would answer a request with no session id
// with a protocol error that hides the cause.
func TestQwenGoalActionOfAnAgentWithNoLiveSessionFailsAtOnce(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		prepare func(a *Agent)
		wantErr string
	}{
		{name: "a stopped agent", prepare: func(a *Agent) { a.SetStoppedForTest(true) }, wantErr: "agent is stopped"},
		{name: "an agent with no session", prepare: func(a *Agent) { a.SetSessionIDForTest("") }, wantErr: "agent has no active session"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a, _, requests := newQwenAgent(t, nil, qwenGoalPeer(`{"goalId":"g-1","revision":1,"status":"active"}`))
			advertiseCommands(t, a, "goal")
			tc.prepare(a)

			_, err := a.PerformGoalAction(agent.GoalActionPause, "")

			// The action awaits each request that it sends, so the peer
			// recorded one before the call returned.
			require.EqualError(t, err, tc.wantErr)
			assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/get"))
			assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/control"))
		})
	}
}

func TestQwenGoalStatusMapping(t *testing.T) {
	t.Parallel()
	for wire, want := range map[string]agent.GoalStatus{
		"active": agent.GoalStatusActive, "paused": agent.GoalStatusPaused, "blocked": agent.GoalStatusBlocked,
		"usage_limited": agent.GoalStatusBlocked, "complete": agent.GoalStatusDone, "something-new": agent.GoalStatusBlocked,
	} {
		assert.Equal(t, want, qwenGoalStatus(wire), wire)
	}
	assert.Empty(t, qwenGoalStatusDetail("active", "running", "ignored"))
	assert.Equal(t, "verifying", qwenGoalStatusDetail("active", "verifying", ""))
	assert.Equal(t, "usage limited: out of tokens", qwenGoalStatusDetail("usage_limited", "idle", "out of tokens"))
	assert.Equal(t, "Three turns made no progress", qwenGoalStatusDetail("paused", "idle", " Three turns made no progress "))
	assert.Empty(t, qwenGoalStatusDetail("complete", "idle", "done"))
	assert.Equal(t, "Waiting for CI", qwenGoalStatusDetail("blocked", "idle", "Waiting for CI"))
	assert.Equal(t, "usage limited", qwenGoalStatusDetail("usage_limited", "idle", "  "), "a blank reason adds nothing")
	assert.Equal(t, "verifying", qwenGoalStatusDetail("active", "verifying", "a reason of an active goal"),
		"an active goal keeps no reason: the reason belongs to a pause or a block")
	assert.Empty(t, qwenGoalStatusDetail("paused", "verifying", ""), "only an active goal verifies")
}

func TestQwenGoalStateReachesTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	created := time.Date(2026, 9, 23, 18, 16, 49, 930_000_000, time.UTC)
	a.HandleOutput(goalState(t, map[string]any{
		"goalId": "9789df2d", "revision": 1, "objective": "Reply with DONE", "status": "paused",
		"turnCount": 3, "activeTimeMs": 4393, "tokensUsed": 330, "tokenBudget": 30000000,
		"createdAt": created.UnixMilli(), "lastReason": "Three goal turns recorded nothing",
	}, "idle"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, "9789df2d", goal.NativeID)
	assert.Equal(t, "Reply with DONE", goal.Objective)
	assert.Equal(t, agent.GoalStatusPaused, goal.Status)
	assert.Equal(t, "Three goal turns recorded nothing", goal.StatusDetail)
	assert.True(t, goal.CreatedAt.Equal(created))
	require.NotNil(t, goal.Iterations)
	assert.Equal(t, int32(3), *goal.Iterations)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(4), *goal.TimeUsedSeconds)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(330), *goal.TokensUsed)
	require.NotNil(t, goal.TokenBudget)
	assert.Equal(t, int64(30000000), *goal.TokenBudget)
}

func TestQwenGoalStateWithNoGoalClearsTheCard(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, nil, "idle"))
	a.HandleOutput(goalState(t, map[string]any{"goalId": "", "objective": "x", "status": "active"}, "idle"))
	assert.Equal(t, 2, sink.GoalClears())
	assert.Equal(t, []bool{false, false}, sink.GoalClearSnapshots())
}

func TestQwenUnreadableGoalStateChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(metaChunk(t, "", map[string]any{"goalState": "text"}))
	_, ok := sink.LastGoal()
	assert.False(t, ok)
	assert.Zero(t, sink.GoalClears())
}

func TestQwenGoalStateLeavesAbsentCountersAbsent(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, map[string]any{"goalId": "g-1", "objective": "Ship", "status": "active"}, "running"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	assert.Equal(t, agent.GoalStatusActive, goal.Status)
	assert.Empty(t, goal.StatusDetail)
	assert.True(t, goal.CreatedAt.IsZero(), "a goal that states no creation time has none")
	assert.Nil(t, goal.Iterations)
	assert.Nil(t, goal.TimeUsedSeconds)
	assert.Nil(t, goal.TokensUsed)
	assert.Nil(t, goal.TokenBudget)
}

func TestQwenGoalStateCountsWholeSeconds(t *testing.T) {
	t.Parallel()
	a, sink, _ := newQwenAgent(t, nil, nil)
	a.HandleOutput(goalState(t, map[string]any{
		"goalId": "g-1", "objective": "Ship", "status": "active", "activeTimeMs": 999, "createdAt": -5, "turnCount": 0, "tokensUsed": 0,
	}, "running"))

	goal, ok := sink.LastGoal()
	require.True(t, ok)
	require.NotNil(t, goal.TimeUsedSeconds)
	assert.Equal(t, int64(0), *goal.TimeUsedSeconds, "a goal that ran for less than a second used no whole second")
	assert.True(t, goal.CreatedAt.IsZero(), "a creation time before the epoch is no time that Qwen states")
	require.NotNil(t, goal.Iterations, "a count of zero is a count, not an absent one")
	assert.Equal(t, int32(0), *goal.Iterations)
	require.NotNil(t, goal.TokensUsed)
	assert.Equal(t, int64(0), *goal.TokensUsed)
}

func TestQwenGoalSetRefusesABlankObjective(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenGoalPeer("null"))
	advertiseCommands(t, a, "goal")
	for _, objective := range []string{"", " \n\t "} {
		outcome, err := a.PerformGoalAction(agent.GoalActionSet, objective)
		assert.Error(t, err, "%q", objective)
		assert.Empty(t, outcome.QueuedInput, "a refused goal queues nothing")
	}
	syncPeer(t, a)
	assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/get"), "a refused goal reads nothing")
	assert.Empty(t, requestsFor(requests(), "qwen/control/session/goal/control"), "a refused goal sends nothing")
}

// qwenGoalPeer answers Qwen's goal control the way Qwen Code 0.24.7 does:
// `qwen/control/session/goal/get` states the goal of the session, and
// `qwen/control/session/goal/control` acts on it. Every other request reads `{}`.
func qwenGoalPeer(goal string) func(agenttest.RecordedRequest) agenttest.RPCReply {
	return func(request agenttest.RecordedRequest) agenttest.RPCReply {
		switch request.Method {
		case "qwen/control/session/goal/get":
			return agenttest.RPCReply{Result: json.RawMessage(`{"snapshot":{"v":2,"goal":` + goal + `,"activity":"running"},"active":null}`)}
		case "qwen/control/session/goal/control", "qwen/control/session/goal/clear":
			return agenttest.RPCReply{Result: json.RawMessage(`{"snapshot":{"v":2,"goal":null,"activity":"idle"}}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	}
}

// Qwen starts the next round of a goal the moment the round before it ends, so
// the worker never finds the agent idle while the goal runs. A Pause that waits
// in the queue for an idle agent reached Qwen only after the goal paused
// itself, and Qwen then refused it: "Only an active Goal can be paused". The
// pause must take effect while the round runs, through Qwen's own goal control.
func TestQwenGoalPauseTakesEffectWhileAGoalRoundRuns(t *testing.T) {
	t.Parallel()
	a, _, requests := newQwenAgent(t, nil, qwenGoalPeer(`{"goalId":"g-1","revision":2,"objective":"Ship","status":"active"}`))
	advertiseCommands(t, a, "goal")
	a.HandleOutput(startTurn(t, nil, qwenTestSession, "goal"))
	require.True(t, a.AgentTurnActive(), "a goal round runs")

	outcome, err := a.PerformGoalAction(agent.GoalActionPause, "")
	require.NoError(t, err)
	assert.Empty(t, outcome.QueuedInput, "a pause that waits for an idle agent never reaches a running goal")
	syncPeer(t, a)
	sent := requestsFor(requests(), "qwen/control/session/goal/control")
	require.Len(t, sent, 1)
	assert.Equal(t, map[string]any{
		"sessionId": qwenTestSession,
		"request":   map[string]any{"action": "pause", "expectedGoalId": "g-1", "expectedRevision": float64(2)},
	}, sent[0].Params)
}
