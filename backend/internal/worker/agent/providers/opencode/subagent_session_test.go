package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func newChildSessionAgent(t *testing.T) (*Agent, *agenttest.Sink, *agenttest.Stdin) {
	t.Helper()
	stdin := &agenttest.Stdin{}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	a := &Agent{}
	a.AttachPeerForTest(ctx, cancel, stdin, "test-agent", "session-1")
	sink := &agenttest.Sink{}
	a.SetSinkForTest(agent.NewProviderServices(sink))
	*a.HooksForTest() = FamilyHooks(&a.FamilyBase)
	return a, sink, stdin
}

// taskPartEvent is the daemon event that states the part of a task call.
func taskPartEvent(t *testing.T, sessionID, callID, tool, status, childSession string) json.RawMessage {
	t.Helper()
	state := map[string]any{"status": status, "input": map[string]any{"prompt": "Count."}}
	if childSession != "" {
		state["metadata"] = map[string]any{"parentSessionId": sessionID, "sessionId": childSession}
	}
	raw, err := json.Marshal(map[string]any{
		"type": "message.part.updated",
		"properties": map[string]any{
			"sessionID": sessionID,
			"part": map[string]any{
				"id": "prt-" + callID, "messageID": "msg-1", "sessionID": sessionID,
				"type": "tool", "callID": callID, "tool": tool, "state": state,
			},
		},
	})
	require.NoError(t, err)
	return raw
}

// writtenRequest is one JSON-RPC request that the agent wrote to stdin.
type writtenRequest struct {
	ID     int64           `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func writtenRequests(stdin *agenttest.Stdin, method string) []writtenRequest {
	var requests []writtenRequest
	for _, line := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
		var request writtenRequest
		if json.Unmarshal([]byte(line), &request) == nil && request.Method == method {
			requests = append(requests, request)
		}
	}
	return requests
}

// awaitRequest waits for the request of method that the agent writes next, and
// returns it.
func awaitRequest(t *testing.T, stdin *agenttest.Stdin, method string, count int) writtenRequest {
	t.Helper()
	testutil.RequireEventually(t, func() bool { return len(writtenRequests(stdin, method)) >= count }, method)
	return writtenRequests(stdin, method)[count-1]
}

// followRunningChild opens the row of the task call call-1, states its child
// session, and answers the agent's load of that session.
func followRunningChild(t *testing.T, a *Agent, stdin *agenttest.Stdin) {
	t.Helper()
	a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count slowly to one hundred."))
	a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
	load := awaitRequest(t, stdin, "session/load", 1)
	assert.JSONEq(t, `{"sessionId":"ses-child","cwd":"","mcpServers":[]}`, string(load.Params))
	require.True(t, a.Deliver(load.ID, agenttest.JSONRPCResponse(load.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	testutil.RequireEventually(t, func() bool { return a.ActiveChildTurnState("call-1").Steerable }, "the load marks the child session steerable")
}

func clearChildSessionContext(t *testing.T, a *Agent, stdin *agenttest.Stdin) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := a.ClearContext()
		done <- err
	}()
	request := awaitRequest(t, stdin, "session/new", 1)
	require.True(t, a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)})))
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("context clear did not finish")
	}
}

func TestOpenCodeContextClearRetiresLoadedChildRoutes(t *testing.T) {
	t.Parallel()
	a, _, stdin := newChildSessionAgent(t)
	followRunningChild(t, a, stdin)
	clearChildSessionContext(t, a, stdin)
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-1"))
	assert.ErrorIs(t, a.SendChildInput("call-1", "old input", nil), errOpenCodeSubagentIdle)
	assert.ErrorIs(t, a.SteerChildInput("call-1", "old steer", nil), agent.ErrNoActiveTurn)
	assert.Empty(t, writtenRequests(stdin, "session/prompt"))
	_, served := a.SessionOwner("ses-child")
	assert.False(t, served)
}

func TestOpenCodeChildLoadPrecedesTheRootReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, stdin := newChildSessionAgent(t)
		defer func() {
			a.SimulateExitForTest()
			a.Stop()
			synctest.Wait()
		}()
		a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count."))
		a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
		load := awaitRequest(t, stdin, "session/load", 1)
		done := make(chan error, 1)
		go func() {
			_, err := a.ClearContext()
			done <- err
		}()
		synctest.Wait()
		assert.Empty(t, writtenRequests(stdin, "session/new"), "an admitted load must finish before root replacement")
		require.True(t, a.Deliver(load.ID, agenttest.JSONRPCResponse(load.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
		request := awaitRequest(t, stdin, "session/new", 1)
		require.True(t, a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)})))
		require.NoError(t, <-done)
		assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-1"))
		_, served := a.SessionOwner("ses-child")
		assert.False(t, served)
	})
}

func TestOpenCodeOutgoingFollowerCannotLoadAfterRootReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, stdin := newChildSessionAgent(t)
		defer func() {
			a.SimulateExitForTest()
			a.Stop()
			synctest.Wait()
		}()
		done := make(chan error, 1)
		go func() {
			_, err := a.ClearContext()
			done <- err
		}()
		request := awaitRequest(t, stdin, "session/new", 1)
		a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
		synctest.Wait()
		assert.Empty(t, writtenRequests(stdin, "session/load"), "a follower must not enter a root that is being replaced")
		require.True(t, a.Deliver(request.ID, agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)})))
		require.NoError(t, <-done)
		synctest.Wait()
		assert.Empty(t, writtenRequests(stdin, "session/load"), "the captured outgoing owner cannot become the replacement owner")
		assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-1"))
		_, served := a.SessionOwner("ses-child")
		assert.False(t, served)
	})
}

func TestOpenCodeLoadsTheChildSessionOfARunningTask(t *testing.T) {
	t.Parallel()
	a, _, stdin := newChildSessionAgent(t)
	followRunningChild(t, a, stdin)
	owner, found := a.SessionOwner("ses-child")
	assert.True(t, found, "the child session routes to the subagent's transcript")
	assert.NotNil(t, owner)
	// The same call stated again neither loads nor attaches twice.
	a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
	assert.Len(t, writtenRequests(stdin, "session/load"), 1)
}

func TestOpenCodeKeepsTheLoadedChildModelSeparateFromTheRoot(t *testing.T) {
	t.Parallel()
	a, _, stdin := newChildSessionAgent(t)
	a.SetCurrentModel("private/parent")
	a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count slowly."))
	a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
	load := awaitRequest(t, stdin, "session/load", 1)
	require.True(t, a.Deliver(load.ID, agenttest.JSONRPCResponse(load.ID, agenttest.RPCReply{Result: json.RawMessage(`{"configOptions":[{"id":"model","category":"model","type":"select","currentValue":"private/voice","options":[{"value":"private/voice","name":"Voice"}]}]}`)})))
	testutil.RequireEventually(t, func() bool { return a.ActiveChildTurnState("call-1").Steerable })
	a.childSessionsMu.Lock()
	model := a.childSessions["call-1"].modelID
	a.childSessionsMu.Unlock()
	assert.Equal(t, "private/voice", model)
	assert.Equal(t, "private/parent", a.CurrentModel())
}

func TestOpenCodeFollowsOnlyARunningTaskWithAChildSession(t *testing.T) {
	t.Parallel()
	for name, event := range map[string]func(*testing.T) json.RawMessage{
		"a pending task": func(t *testing.T) json.RawMessage {
			return taskPartEvent(t, "session-1", "call-1", "task", "pending", "ses-child")
		},
		"a running task without one": func(t *testing.T) json.RawMessage {
			return taskPartEvent(t, "session-1", "call-1", "task", "running", "")
		},
		"a completed task": func(t *testing.T) json.RawMessage {
			return taskPartEvent(t, "session-1", "call-1", "task", "completed", "ses-child")
		},
		"another tool": func(t *testing.T) json.RawMessage {
			return taskPartEvent(t, "session-1", "call-1", "bash", "running", "ses-child")
		},
		"another session": func(t *testing.T) json.RawMessage {
			return taskPartEvent(t, "session-2", "call-1", "task", "running", "ses-child")
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, _, _ := newChildSessionAgent(t)
			a.handleDaemonEvent(event(t))
			_, _, running := a.runningChildSession("call-1")
			assert.False(t, running)
			_, found := a.SessionOwner("ses-child")
			assert.False(t, found)
		})
	}
}

func TestOpenCodeChildSessionUpdatesReachTheSubagentTranscript(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newChildSessionAgent(t)
	followRunningChild(t, a, stdin)
	row, found := sink.BackgroundTask("call-1")
	require.True(t, found)
	require.NotEmpty(t, row.ChildAgentID)
	childUpdate := func(update string) []byte {
		return fmt.Appendf(nil, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"ses-child","update":%s}}`, update)
	}
	a.HandleOutput(childUpdate(`{"sessionUpdate":"agent_message_chunk","messageId":"child-message","content":{"type":"text","text":"Counting now."}}`))
	a.HandleOutput(childUpdate(`{"sessionUpdate":"tool_call","toolCallId":"child-read","title":"read","kind":"read","status":"pending","rawInput":{"filePath":"/work/note.txt"}}`))
	// The child's own model and command state belong to it, not to the parent.
	a.HandleOutput(childUpdate(`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"child-only"}]}`))
	child := sink.Child(row.ChildAgentID)
	var texts []string
	for _, message := range child.Messages() {
		var frame struct {
			Kind string `json:"kind"`
			Text string `json:"text"`
		}
		if json.Unmarshal(message.Content, &frame) == nil && frame.Kind == contracts.AssembledMessageKindText {
			texts = append(texts, frame.Text)
		}
	}
	assert.Equal(t, []string{"Counting now."}, texts, "the child's text reaches the child transcript")
	_, open := a.OpenToolSink("ses-child", "child-read")
	assert.True(t, open, "the child's tool call opens in the child transcript")
	assert.False(t, a.HasAvailableCommand("child-only"), "the child's commands do not reach the parent")
	for _, message := range sink.Messages() {
		assert.NotContains(t, string(message.Content), "Counting now.", "the parent transcript keeps none of the child's output")
	}
}

func TestOpenCodeSteersARunningChild(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newChildSessionAgent(t)
	assert.ErrorIs(t, a.SendChildInput("call-1", "Also count backwards.", nil), errOpenCodeSubagentIdle)
	assert.ErrorIs(t, a.SteerChildInput("call-1", "Also count backwards.", nil), agent.ErrNoActiveTurn)
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-1"))

	a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count slowly to one hundred."))
	a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
	assert.Equal(t, agent.TurnState{Active: true}, a.ActiveChildTurnState("call-1"), "a running child takes a steer once its session is loaded")
	assert.ErrorIs(t, a.SteerChildInput("call-1", "Also count backwards.", nil), agent.ErrChildRouteNotReady)
	load := awaitRequest(t, stdin, "session/load", 1)
	require.True(t, a.Deliver(load.ID, agenttest.JSONRPCResponse(load.ID, agenttest.RPCReply{Result: json.RawMessage(`{}`)})))
	testutil.RequireEventually(t, func() bool { return a.ActiveChildTurnState("call-1").Steerable })

	assert.ErrorIs(t, a.SendChildInput("call-1", "Also count backwards.", nil), agent.ErrAgentBusy, "the queue steers a running child")
	require.NoError(t, a.SteerChildInput("call-1", "Also count backwards.", nil))
	prompt := awaitRequest(t, stdin, "session/prompt", 1)
	assert.JSONEq(t, `{"sessionId":"ses-child","prompt":[{"type":"text","text":"Also count backwards."}]}`, string(prompt.Params))

	// A refused steer states the refusal in the child's transcript.
	require.True(t, a.Deliver(prompt.ID, agenttest.JSONRPCResponse(prompt.ID, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"the session is busy"}`)})))
	row, found := sink.BackgroundTask("call-1")
	require.True(t, found)
	child := sink.Child(row.ChildAgentID)
	testutil.RequireEventually(t, func() bool { return len(child.LeapMuxNotifications()) == 1 })
	assert.Equal(t, contracts.NotificationTypeAgentError, child.LeapMuxNotifications()[0][contracts.NotificationFieldType])
	assert.Contains(t, child.LeapMuxNotifications()[0][contracts.NotificationFieldError], "the session is busy")
	assert.Empty(t, sink.LeapMuxNotifications(), "the parent states nothing")

	// The task's end ends the subagent's turn: a later message is refused.
	a.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-child", "Counted."))
	assert.ErrorIs(t, a.SendChildInput("call-1", "One more.", nil), errOpenCodeSubagentIdle)
	assert.ErrorIs(t, a.SteerChildInput("call-1", "One more.", nil), agent.ErrNoActiveTurn)
	assert.Equal(t, agent.TurnState{}, a.ActiveChildTurnState("call-1"))
}

func TestOpenCodeLateChildSteerRefusalReachesTheParentAfterRouteTeardown(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newChildSessionAgent(t)
	followRunningChild(t, a, stdin)
	require.NoError(t, a.SteerChildInput("call-1", "Also count backwards.", nil))
	prompt := awaitRequest(t, stdin, "session/prompt", 1)

	// The task closes while its detached prompt still waits for the RPC reply.
	a.HandleToolCallUpdateForTest(openCodeTaskResult(t, "call-1", "ses-child", "Counted."))
	_, found := a.SessionOwner("ses-child")
	require.False(t, found, "the closed task retired the child-session route")

	require.True(t, a.Deliver(prompt.ID, agenttest.JSONRPCResponse(prompt.ID, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"the session ended"}`)})))
	testutil.RequireEventually(t, func() bool { return len(sink.LeapMuxNotifications()) == 1 }, "the late refusal reaches the parent")
	notifications := sink.LeapMuxNotifications()
	assert.Equal(t, contracts.NotificationTypeAgentError, notifications[0][contracts.NotificationFieldType])
	assert.Contains(t, notifications[0][contracts.NotificationFieldError], "the session ended")
}

func TestOpenCodeLateChildSteerRefusalKeepsItsOutgoingTranscript(t *testing.T) {
	t.Parallel()
	a, sink, stdin := newChildSessionAgent(t)
	followRunningChild(t, a, stdin)
	row, found := sink.BackgroundTask("call-1")
	require.True(t, found)
	child := sink.Child(row.ChildAgentID)
	require.NoError(t, a.SteerChildInput("call-1", "An outgoing child message.", nil))
	prompt := awaitRequest(t, stdin, "session/prompt", 1)
	clearChildSessionContext(t, a, stdin)
	require.True(t, a.Deliver(prompt.ID, agenttest.JSONRPCResponse(prompt.ID, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"the retired child refused the message"}`)})))
	testutil.RequireEventually(t, func() bool {
		return len(child.LeapMuxNotifications())+len(sink.LeapMuxNotifications()) > 0
	})
	assert.Empty(t, sink.LeapMuxNotifications(), "an outgoing refusal cannot enter the replacement root")
	notices := child.LeapMuxNotifications()
	require.Len(t, notices, 1)
	assert.Contains(t, notices[0][contracts.NotificationFieldError], "the retired child refused the message")
}

func TestOpenCodeAFailedChildLoadLeavesTheChildUnsteerable(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a, _, stdin := newChildSessionAgent(t)
		a.HandleToolCallForTest(openCodeTaskRequest(t, "call-1", "", "Count."))
		a.handleDaemonEvent(taskPartEvent(t, "session-1", "call-1", "task", "running", "ses-child"))
		load := awaitRequest(t, stdin, "session/load", 1)
		require.True(t, a.Deliver(load.ID, agenttest.JSONRPCResponse(load.ID, agenttest.RPCReply{Error: json.RawMessage(`{"code":-32602,"message":"session not found"}`)})))
		synctest.Wait()
		assert.ErrorIs(t, a.SteerChildInput("call-1", "Steer.", nil), agent.ErrChildRouteNotReady)
		assert.Equal(t, agent.TurnState{Active: true}, a.ActiveChildTurnState("call-1"))
		a.SimulateExitForTest()
		a.Stop()
		synctest.Wait()
	})
}
