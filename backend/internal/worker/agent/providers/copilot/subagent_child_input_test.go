package copilot

import (
	"cmp"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// copilotTaskListAnswer states the tasks that `session.tasks.list` answers
// by default: a shell task and another subagent beside the subagent of `task-1`,
// whose task ID differs from its agent ID.
const copilotTaskListAnswer = `{"tasks":[` +
	`{"type":"shell","id":"shell-1","toolCallId":"task-1"},` +
	`{"type":"agent","id":"other-task","toolCallId":"task-9"},` +
	`{"type":"agent","id":"worker-task","toolCallId":"task-1"}]}`

// copilotTaskScript states how the fake runtime answers each task request. An
// empty answer selects the default. An answer that starts with `{"error"`
// answers the request with that error.
type copilotTaskScript struct {
	list   string
	cancel string
	send   string
	// beforeCancel runs before the runtime answers `session.tasks.cancel`.
	beforeCancel func(*Agent)
}

// newCopilotTaskHarness starts a fake runtime that answers the task requests of
// script.
func newCopilotTaskHarness(t *testing.T, script copilotTaskScript) copilotRuntimeHarness {
	t.Helper()
	return newCopilotRuntimeHarness(t, map[string]string{
		"session.tasks.list":        cmp.Or(script.list, copilotTaskListAnswer),
		"session.tasks.cancel":      cmp.Or(script.cancel, `{"cancelled":true}`),
		"session.tasks.sendMessage": cmp.Or(script.send, `{"sent":true}`),
	}, func(a *Agent, method string) {
		if method == "session.tasks.cancel" && script.beforeCancel != nil {
			script.beforeCancel(a)
		}
	})
}

// pauseCopilotSubagent ends the first turn of the subagent `agent-1`, which
// leaves a background subagent idle.
func pauseCopilotSubagent(t *testing.T, a *Agent) {
	t.Helper()
	a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentCompleted, map[string]any{"toolCallId": "task-1"}))
}

// The state of the subagent decides which message route its tab takes, and a
// refused route sends nothing.
func TestCopilotChildMessageRoutesFollowTheSubagentState(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		mode      string
		idle      bool
		unknown   bool
		sendErr   error
		steerErr  error
		turnState agent.TurnState
	}{
		{name: "a running background subagent", mode: copilotExecutionBackground,
			sendErr: agent.ErrAgentBusy, turnState: agent.TurnState{Active: true, Steerable: true}},
		{name: "an idle background subagent", mode: copilotExecutionBackground, idle: true,
			steerErr: agent.ErrNoActiveTurn},
		{name: "a running foreground subagent", mode: "sync",
			sendErr: errCopilotForegroundSubagent, steerErr: errCopilotForegroundSubagent, turnState: agent.TurnState{Active: true}},
		{name: "an ended subagent", mode: copilotExecutionBackground, unknown: true,
			sendErr: errCopilotSubagentEnded, steerErr: agent.ErrNoActiveTurn},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newCopilotTaskHarness(t, copilotTaskScript{})
			startCopilotSubagent(t, h.agent, h.sink, test.mode)
			if test.idle {
				pauseCopilotSubagent(t, h.agent)
			}
			key := "agent-1"
			if test.unknown {
				key = "agent-unknown"
			}
			assert.Equal(t, test.turnState, h.agent.ActiveChildTurnState(key))
			before := len(*h.requests)
			if test.sendErr != nil {
				require.ErrorIs(t, h.agent.SendChildInput(key, "Hello.", nil), test.sendErr)
			}
			if test.steerErr != nil {
				require.ErrorIs(t, h.agent.SteerChildInput(key, "Hello.", nil), test.steerErr)
			}
			assert.Len(t, *h.requests, before, "a refused route sends nothing")
		})
	}
}

// A steer reaches a running background subagent through Copilot's task
// message. The subagent's task is the agent task that its spawning call
// started, and that task's ID is not the agent ID.
func TestCopilotSteerChildInputSendsTheTaskMessage(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	require.NoError(t, h.agent.SteerChildInput("agent-1", "Also check the tests.", nil))
	assert.Equal(t, []string{"session.tasks.list", "session.tasks.sendMessage"}, h.methods())
	assert.Equal(t, map[string]any{"sessionId": "session-1", "id": "worker-task", "message": "Also check the tests."}, h.lastParams(t))
}

// A message wakes an idle background subagent, which reads it in its next turn.
func TestCopilotSendChildInputWakesAnIdleBackgroundSubagent(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	pauseCopilotSubagent(t, h.agent)
	require.NoError(t, h.agent.SendChildInput("agent-1", "Now check the docs.", nil))
	assert.Equal(t, []string{"session.tasks.list", "session.tasks.sendMessage"}, h.methods())
	assert.Equal(t, map[string]any{"sessionId": "session-1", "id": "worker-task", "message": "Now check the docs."}, h.lastParams(t))
}

// Each failure of the task message reaches the caller, so the queue keeps the
// message rather than reporting it delivered.
func TestCopilotChildMessageFailures(t *testing.T) {
	t.Parallel()
	attachment := []*leapmuxv1.Attachment{{Filename: "notes.txt", Data: []byte("notes")}}
	for _, test := range []struct {
		name        string
		script      copilotTaskScript
		attachments []*leapmuxv1.Attachment
		wantErr     error
		wantText    string
		wantMethods []string
	}{
		{name: "an attachment", attachments: attachment,
			wantText: "text alone", wantMethods: []string{}},
		{name: "no task for the subagent", script: copilotTaskScript{list: `{"tasks":[{"type":"agent","id":"other-task","toolCallId":"task-9"}]}`},
			wantErr: agent.ErrChildRouteNotReady, wantMethods: []string{"session.tasks.list"}},
		{name: "an empty task list", script: copilotTaskScript{list: `{"tasks":[]}`},
			wantErr: agent.ErrChildRouteNotReady, wantMethods: []string{"session.tasks.list"}},
		{name: "a task with no ID", script: copilotTaskScript{list: `{"tasks":[{"type":"agent","id":"","toolCallId":"task-1"}]}`},
			wantErr: agent.ErrChildRouteNotReady, wantMethods: []string{"session.tasks.list"}},
		{name: "a refused task list", script: copilotTaskScript{list: `{"error":{"code":-32603,"message":"no tasks"}}`},
			wantText: "list the Copilot tasks", wantMethods: []string{"session.tasks.list"}},
		{name: "a malformed task list", script: copilotTaskScript{list: `{"tasks":"none"}`},
			wantText: "read the Copilot task list", wantMethods: []string{"session.tasks.list"}},
		{name: "a refused task message", script: copilotTaskScript{send: `{"error":{"code":-32603,"message":"send refused"}}`},
			wantText: "send the message to the Copilot subagent", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
		{name: "a lost response", script: copilotTaskScript{send: copilotRuntimeResponseLost},
			wantErr: agent.ErrDeliveryUncertain, wantText: "send the message to the Copilot subagent", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
		{name: "a malformed answer", script: copilotTaskScript{send: `{"sent":"yes"}`},
			wantErr: agent.ErrDeliveryUncertain, wantText: "read the Copilot task message", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
		{name: "an answer without a receipt", script: copilotTaskScript{send: `{}`},
			wantErr: agent.ErrDeliveryUncertain, wantText: "read the Copilot task message", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
		{name: "an undelivered message with a reason", script: copilotTaskScript{send: `{"sent":false,"error":"the task ended"}`},
			wantText: "the task ended", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
		{name: "an undelivered message without a reason", script: copilotTaskScript{send: `{"sent":false}`},
			wantText: "Copilot did not deliver it", wantMethods: []string{"session.tasks.list", "session.tasks.sendMessage"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newCopilotTaskHarness(t, test.script)
			startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
			pauseCopilotSubagent(t, h.agent)
			err := h.agent.SendChildInput("agent-1", "Hello.", test.attachments)
			require.Error(t, err)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			} else {
				require.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
			}
			assert.Contains(t, err.Error(), test.wantText)
			assert.Equal(t, test.wantMethods, h.methods())
		})
	}
}

// A stopped agent sends no message to a subagent.
func TestCopilotChildMessageRefusesAStoppedAgent(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	pauseCopilotSubagent(t, h.agent)
	h.stop()
	err := h.agent.SendChildInput("agent-1", "Hello.", nil)
	require.ErrorContains(t, err, "agent is stopped")
	assert.Empty(t, *h.requests)
}

// Copilot's task cancel stops the running turn of any subagent, foreground or
// background, and Copilot then ends the subagent with a cancelled completion.
func TestCopilotInterruptChildCancelsTheSubagentTask(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{copilotExecutionBackground, "sync"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			h := newCopilotTaskHarness(t, copilotTaskScript{})
			startCopilotSubagent(t, h.agent, h.sink, mode)
			require.NoError(t, h.agent.InterruptChild("agent-1", agent.StopContext{}))
			assert.Equal(t, []string{"session.tasks.list", "session.tasks.cancel"}, h.methods())
			assert.Equal(t, map[string]any{"sessionId": "session-1", "id": "worker-task"}, h.lastParams(t))
		})
	}
}

// A subagent with no running turn has nothing to stop, so the interrupt sends
// nothing and reports success.
func TestCopilotInterruptChildWithoutARunningTurn(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{})
	startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	require.NoError(t, h.agent.InterruptChild("agent-unknown", agent.StopContext{}), "an ended subagent")
	pauseCopilotSubagent(t, h.agent)
	require.NoError(t, h.agent.InterruptChild("agent-1", agent.StopContext{}), "an idle subagent")
	assert.Empty(t, *h.requests)
}

// A normal completion can reach the reader while tasks.cancel is in flight.
// The background child then stays present but idle, so cancelled:false is a
// successful interrupt and the completed turn leaves its row paused.
func TestCopilotInterruptChildAcceptsACompletionDuringCancel(t *testing.T) {
	t.Parallel()
	h := newCopilotTaskHarness(t, copilotTaskScript{
		cancel:       `{"cancelled":false}`,
		beforeCancel: func(a *Agent) { pauseCopilotSubagent(t, a) },
	})
	_, child := startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
	require.NoError(t, h.agent.InterruptChild("agent-1", agent.StopContext{}))
	row, ok := copilotBackgroundRow(t, h.sink, "agent-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusPaused, row.Status)
	assert.Contains(t, h.agent.children, "agent-1")
	assert.Equal(t, agent.TurnState{}, h.agent.ActiveChildTurnState("agent-1"))
	require.NotEmpty(t, child.TurnStates())
	assert.Equal(t, agent.TurnState{}, child.TurnStates()[len(child.TurnStates())-1])
}

// A cancel that Copilot refuses is a failure only while the subagent still
// runs: a subagent that ended during the request needs no cancel.
func TestCopilotInterruptChildFailures(t *testing.T) {
	t.Parallel()
	endTheSubagent := func(a *Agent) {
		a.HandleOutput(nativeCopilotEvent(t, "agent-1", contracts.CopilotEventSubagentCompleted, map[string]any{"toolCallId": "task-1", "cancelled": true}))
	}
	for _, test := range []struct {
		name     string
		script   copilotTaskScript
		wantErr  error
		wantText string
	}{
		{name: "an uncancelled running task", script: copilotTaskScript{cancel: `{"cancelled":false}`},
			wantText: "did not cancel the subagent task worker-task"},
		{name: "an uncancelled task that ended", script: copilotTaskScript{cancel: `{"cancelled":false}`, beforeCancel: endTheSubagent}},
		{name: "no task for the subagent", script: copilotTaskScript{list: `{"tasks":[]}`},
			wantErr: agent.ErrChildRouteNotReady},
		{name: "a refused cancel", script: copilotTaskScript{cancel: `{"error":{"code":-32603,"message":"cancel refused"}}`},
			wantText: "cancel the Copilot subagent"},
		{name: "a malformed cancel answer", script: copilotTaskScript{cancel: `{"cancelled":"yes"}`},
			wantText: "read the Copilot task cancel"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h := newCopilotTaskHarness(t, test.script)
			startCopilotSubagent(t, h.agent, h.sink, copilotExecutionBackground)
			err := h.agent.InterruptChild("agent-1", agent.StopContext{})
			if test.wantErr == nil && test.wantText == "" {
				require.NoError(t, err)
				row, _ := copilotBackgroundRow(t, h.sink, "agent-1")
				assert.Equal(t, bgtask.StatusStopped, row.Status)
				return
			}
			require.Error(t, err)
			if test.wantErr != nil {
				require.ErrorIs(t, err, test.wantErr)
			}
			assert.Contains(t, err.Error(), test.wantText)
		})
	}
}
