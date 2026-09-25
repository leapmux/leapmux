package acp

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
)

// newRetiringTestAgent returns the test agent over a peer whose session/new
// opens "session-2", with a sink that records control requests and rows.
func newRetiringTestAgent(t *testing.T) (*testAgent, *agenttest.ControlSink, func() []agenttest.RecordedRequest) {
	t.Helper()
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-2"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	return a, sink, requests
}

// syncTestPeer returns once the peer recorded every line that the agent wrote
// before the call. The peer reads the lines in order and answers this request
// only after it recorded each line before it, so a test that asserts that a
// line is ABSENT does not pass only because the peer did not read it yet.
func syncTestPeer(t *testing.T, a *testAgent) {
	t.Helper()
	_, err := a.SendRequest("test/sync", nil, 30*time.Second)
	require.NoError(t, err)
}

// permissionRequest is a session/request_permission that sessionID raises. An
// empty sessionID leaves the field out.
func permissionRequest(t *testing.T, id int, sessionID string) []byte {
	t.Helper()
	params := map[string]any{"toolCall": map[string]any{"toolCallId": "call_permission"}}
	if sessionID != "" {
		params["sessionId"] = sessionID
	}
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": acpMethodSessionRequestPermission, "params": params})
	require.NoError(t, err)
	return frame
}

// elicitationRequest is an MCP elicitation that sessionID raises.
func elicitationRequest(t *testing.T, id int, sessionID string) []byte {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "elicitation/create", "params": map[string]any{"sessionId": sessionID}})
	require.NoError(t, err)
	return frame
}

// indexOfMethod returns the position of the first recorded frame of method,
// or -1.
func indexOfMethod(requests []agenttest.RecordedRequest, method string) int {
	for i, request := range requests {
		if request.Method == method {
			return i
		}
	}
	return -1
}

// indexOfAnswer returns the position of the first response that the agent
// wrote for the request id, or -1.
func indexOfAnswer(t *testing.T, requests []agenttest.RecordedRequest, id int) int {
	t.Helper()
	for i, request := range requests {
		if request.Method != "" {
			continue
		}
		var frame struct {
			ID json.RawMessage `json:"id"`
		}
		require.NoError(t, json.Unmarshal([]byte(request.Raw), &frame))
		if string(frame.ID) == jsonNumber(id) {
			return i
		}
	}
	return -1
}

// jsonNumber spells an integer id as the wire carries it.
func jsonNumber(id int) string {
	encoded, err := json.Marshal(id)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

// sessionIDsOf returns the sessionId of each recorded frame of method.
func sessionIDsOf(requests []agenttest.RecordedRequest, method string) []string {
	var out []string
	for _, request := range requests {
		if request.Method == method {
			sessionID, _ := request.Params["sessionId"].(string)
			out = append(out, sessionID)
		}
	}
	return out
}

// A context clear abandons the outgoing session, whose turn can wait on a
// control request that no reader can answer any more. The clear answers each
// such request, retires its card, and cancels the turn, all BEFORE it opens
// the new session. Without that, the turn stayed blocked, and a later stop of
// the NEW session answered the old request and let the old turn go on where
// nobody could see it.
func TestACPClearContextReleasesTheOutgoingTurnBeforeTheNewSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	a.promptActive = true
	a.HandleOutput(permissionRequest(t, 30, "session-1"))
	a.HandleOutput(elicitationRequest(t, 31, "session-1"))
	require.Len(t, sink.PublishedControls(), 2)

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	syncTestPeer(t, a)

	lines := requests()
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(lines))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["30"])
	assert.JSONEq(t, `{"action":"cancel"}`, answers["31"])
	assert.ElementsMatch(t, []string{"jsonrpc:30", "jsonrpc:31"}, sink.CanceledControls(), "each card retires with its request")
	assert.Equal(t, []string{"session-1"}, sessionIDsOf(lines, MethodSessionCancel), "the clear cancels the outgoing turn")

	cancel := indexOfMethod(lines, MethodSessionCancel)
	newSession := indexOfMethod(lines, MethodSessionNew)
	require.NotEqual(t, -1, cancel)
	require.NotEqual(t, -1, newSession)
	assert.Less(t, indexOfAnswer(t, lines, 30), cancel, "the answers go first, because the agent blocks on them")
	assert.Less(t, cancel, newSession, "the outgoing turn ends before the new session opens")
	assert.Zero(t, a.OutstandingControlCountForTest())

	// A later stop belongs to the new session alone.
	a.promptActive = true
	require.NoError(t, a.Interrupt())
	syncTestPeer(t, a)
	assert.Equal(t, []string{"session-1", "session-2"}, sessionIDsOf(requests(), MethodSessionCancel))
	assert.Equal(t, indexOfAnswer(t, requests(), 30), indexOfAnswer(t, lines, 30), "no second answer reaches the old request")
}

// A clear of an idle session cancels no turn, because there is none. It still
// answers each open control request: a subagent of the session can wait on one
// while the main turn is idle, and nothing could answer it after the clear.
func TestACPClearContextOfAnIdleSessionAnswersItsRequestsAndCancelsNothing(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	a.AttachChildSession("child-session", "call_child")
	a.HandleOutput(permissionRequest(t, 35, "child-session"))
	require.Len(t, sink.PublishedControls(), 1)

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncTestPeer(t, a)

	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["35"])
	assert.Equal(t, []string{"jsonrpc:35"}, sink.CanceledControls())
	assert.Empty(t, sessionIDsOf(requests(), MethodSessionCancel))
}

// A control request that the retired session raises after the clear never
// reaches the reader. The base answers it at once with its cancel answer,
// because its turn waits on the answer and no card could ever give one.
func TestACPRefusesAControlRequestOfARetiredSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)

	a.HandleOutput(permissionRequest(t, 40, "session-1"))
	a.HandleOutput(elicitationRequest(t, 41, "session-1"))
	syncTestPeer(t, a)

	assert.Empty(t, sink.PublishedControls(), "no card of the retired session reaches the reader")
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["40"])
	assert.JSONEq(t, `{"action":"cancel"}`, answers["41"])
	assert.Zero(t, a.OutstandingControlCountForTest(), "a refused request leaves no record for a later stop")
}

// The refusal applies to a session that the agent does not serve, and to no
// other. The current session, a subagent session that a row routes, and a
// request that states no session all reach the reader.
func TestACPPublishesTheControlRequestsOfTheSessionsThatItServes(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	a.AttachChildSession("child-session", "call_child")

	a.HandleOutput(permissionRequest(t, 50, "session-1"))
	a.HandleOutput(permissionRequest(t, 51, "child-session"))
	a.HandleOutput(permissionRequest(t, 52, ""))
	syncTestPeer(t, a)

	assert.Len(t, sink.PublishedControls(), 3)
	assert.Empty(t, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests())), "the agent answers nothing on the reader's behalf")
}

// The worker accepts an answer only while the agent is in the session that it
// stored with the request. The sink learns a new main session some time after
// the base does: at the start, after the answer to session/new, and at a
// context clear, after the swap. A request of the main session in that interval
// took the session that the sink still held, so every answer to it was refused.
// The base therefore stores a request of its main session under that session. A
// request of a subagent session, and one that states no session, keep the
// session that the sink holds, because the request does not state which main
// session owns it.
func TestACPStoresAControlRequestOfTheMainSessionUnderThatSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	sink.UpdateSessionID("session-0")
	a.AttachChildSession("child-session", "call_child")

	a.HandleOutput(permissionRequest(t, 70, "session-1"))
	a.HandleOutput(permissionRequest(t, 71, "child-session"))
	a.HandleOutput(permissionRequest(t, 72, ""))
	syncTestPeer(t, a)

	sessions := map[string]string{}
	for _, record := range sink.PublishedControls() {
		sessions[record.RequestID] = record.AgentSessionID
	}
	assert.Equal(t, map[string]string{
		"jsonrpc:70": "session-1",
		"jsonrpc:71": "session-0",
		"jsonrpc:72": "session-0",
	}, sessions)
}

// A context clear closes each registry row that the outgoing session opened,
// because no later update of that session reaches the base. Without that, a
// background subagent of the old session kept its row running until the
// process exited.
func TestACPClearContextClosesTheRowsOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call_background", ChildAgentKey: "call_background", Title: "Background helper", Status: bgtask.StatusRunning,
	})
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "shell:1", Kind: bgtask.KindShell, Title: "sleep 60", Status: bgtask.StatusRunning,
	})
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call_done", ChildAgentKey: "call_done", Title: "Finished helper", Status: bgtask.StatusRunning,
	})
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call_done", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly,
	})

	_, err := a.ClearContext()
	require.NoError(t, err)

	for _, rowKey := range []string{"call_background", "shell:1"} {
		row, ok := sink.BackgroundTask(rowKey)
		require.True(t, ok, rowKey)
		assert.Equal(t, bgtask.StatusStopped, row.Status, "%s ends with the session that ran it", rowKey)
	}
	done, ok := sink.BackgroundTask("call_done")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCompleted, done.Status, "a row that already ended keeps its own status")
}

// The subagent sessions of the outgoing session go with it: a late update of
// one reaches no transcript, and a late control request of one is refused.
func TestACPClearContextForgetsTheChildSessionsOfTheOutgoingSession(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call_child", ChildAgentKey: "call_child", Title: "Child", Status: bgtask.StatusRunning,
	})
	a.AttachChildSession("child-session", "call_child")
	childIDs := sink.ChildAgentIDs()
	require.Len(t, childIDs, 1)

	_, err := a.ClearContext()
	require.NoError(t, err)

	a.HandleSessionUpdateForTest(json.RawMessage(`{"sessionId":"child-session","update":{"sessionUpdate":"tool_call","toolCallId":"late_call","title":"Late","status":"pending"}}`))
	assert.Zero(t, sink.Child(childIDs[0]).MessageCount(), "a late update of the old subagent reaches no transcript")

	a.HandleOutput(permissionRequest(t, 60, "child-session"))
	syncTestPeer(t, a)
	assert.Empty(t, sink.PublishedControls())
	answers := agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, answers["60"])
}

// An agent that advertises session/close receives it for the outgoing session,
// after the new session opened. Grok Build, Goose, OpenCode, Kilo and Reasonix
// end the subagents and the background work of a session there, which
// session/cancel leaves running.
func TestACPClearContextClosesTheOutgoingSessionWhenTheAgentAdvertisesIt(t *testing.T) {
	t.Parallel()
	a, _, requests := newRetiringTestAgent(t)
	a.SetClosesSessionsForTest(true)

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncTestPeer(t, a)

	lines := requests()
	assert.Equal(t, []string{"session-1"}, sessionIDsOf(lines, MethodSessionClose))
	assert.Less(t, indexOfMethod(lines, MethodSessionNew), indexOfMethod(lines, MethodSessionClose),
		"the old session closes only once its replacement exists")
}

func TestACPClearContextSendsNoCloseToAnAgentThatAdvertisesNone(t *testing.T) {
	t.Parallel()
	a, _, requests := newRetiringTestAgent(t)

	_, err := a.ClearContext()
	require.NoError(t, err)
	syncTestPeer(t, a)

	assert.Empty(t, sessionIDsOf(requests(), MethodSessionClose))
}

// A provider whose agent offers another route to the work of a session takes
// the id of the outgoing session, once.
func TestACPClearContextHandsTheOutgoingSessionToTheProvider(t *testing.T) {
	t.Parallel()
	a, _, _ := newRetiringTestAgent(t)
	var retired []string
	a.hooks.RetireSession = func(sessionID string) { retired = append(retired, sessionID) }

	_, err := a.ClearContext()
	require.NoError(t, err)

	assert.Equal(t, []string{"session-1"}, retired)
}

// A session/new that fails leaves the old session current, so the old session
// is not retired: its rows stay open and the agent receives no close.
func TestACPFailedClearContextRetiresNothing(t *testing.T) {
	t.Parallel()
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Error: json.RawMessage(`{"code":-32603,"message":"no session for you"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	a.SetClosesSessionsForTest(true)
	var retired []string
	a.hooks.RetireSession = func(sessionID string) { retired = append(retired, sessionID) }
	a.ApplySubagentObservation(&SubagentObservation{RowKey: "call_background", Title: "Background helper", Status: bgtask.StatusRunning})

	_, err := a.ClearContext()
	require.Error(t, err)
	syncTestPeer(t, a)

	assert.Equal(t, "session-1", a.CurrentSessionID())
	row, ok := sink.BackgroundTask("call_background")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusRunning, row.Status)
	assert.Empty(t, sessionIDsOf(requests(), MethodSessionClose))
	assert.Empty(t, retired)
}

// An agent that answers session/new with the id of the session that it already
// runs still serves that session, so the clear retires nothing of it.
func TestACPClearContextRetiresNoSessionThatTheAgentReopened(t *testing.T) {
	t.Parallel()
	a, requests := newTestAgentForRPCWithResponder(t, func(method string) agenttest.RPCReply {
		if method == MethodSessionNew {
			return agenttest.RPCReply{Result: json.RawMessage(`{"sessionId":"session-1"}`)}
		}
		return agenttest.RPCReply{Result: json.RawMessage(`{}`)}
	})
	sink := &agenttest.ControlSink{}
	a.sink = agent.NewProviderServices(sink)
	a.SetClosesSessionsForTest(true)
	var retired []string
	a.hooks.RetireSession = func(sessionID string) { retired = append(retired, sessionID) }
	a.AttachChildSession("child-session", "call_child")

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-1", sessionID)
	syncTestPeer(t, a)

	assert.Empty(t, sessionIDsOf(requests(), MethodSessionClose), "no close reaches the session that the agent serves")
	assert.Empty(t, retired)
	assert.True(t, a.ServesSession("child-session"))
}

func TestACPAdvertisesSessionClose(t *testing.T) {
	t.Parallel()
	for response, want := range map[string]bool{
		`{"agentCapabilities":{"sessionCapabilities":{"list":{},"resume":{},"close":{}}}}`: true,
		`{"agentCapabilities":{"sessionCapabilities":{"close":true}}}`:                     true,
		`{"agentCapabilities":{"sessionCapabilities":{"list":{},"resume":{}}}}`:            false,
		`{"agentCapabilities":{"sessionCapabilities":{"close":null}}}`:                     false,
		`{"agentCapabilities":{"sessionCapabilities":{"close":false}}}`:                    false,
		`{"agentCapabilities":{}}`: false,
		`{}`:                       false,
		`not json`:                 false,
	} {
		assert.Equal(t, want, advertisesSessionClose([]byte(response)), response)
	}
}

// answerlessRequest publishes a control request of the current session that
// carries no cancel answer, as a provider's own dialog does.
func answerlessRequest(t *testing.T, a *testAgent, id int) {
	t.Helper()
	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": "provider/question", "params": map[string]any{"sessionId": "session-1"}})
	require.NoError(t, err)
	a.PublishSessionControlRequest(providerkit.ParseLine(frame), nil)
}

// By default a stop retires every open request, one with no cancel answer
// included. Cursor's question is such a request: its turn owns it, and Cursor
// defines no outcome for a withdrawn one, so the card retires with no answer.
func TestACPInterruptRetiresAnAnswerlessRequestByDefault(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	answerlessRequest(t, a, 70)
	a.promptActive = true

	require.NoError(t, a.Interrupt())
	syncTestPeer(t, a)

	assert.Equal(t, []string{"jsonrpc:70"}, sink.CanceledControls())
	assert.Equal(t, -1, indexOfAnswer(t, requests(), 70), "the request takes no answer")
	assert.Zero(t, a.OutstandingControlCountForTest())
}

// A provider whose answerless requests outlive the turn keeps them open across
// a stop and across a context clear, and still answers the rest.
func TestACPKeepsAnAnswerlessRequestThatOutlivesTheTurn(t *testing.T) {
	t.Parallel()
	for name, stop := range map[string]func(*testAgent) error{
		"interrupt":     func(a *testAgent) error { return a.Interrupt() },
		"context clear": func(a *testAgent) error { _, err := a.ClearContext(); return err },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, sink, requests := newRetiringTestAgent(t)
			a.hooks.AnswerlessControlsOutliveTurns = true
			answerlessRequest(t, a, 71)
			a.HandleOutput(permissionRequest(t, 72, "session-1"))
			a.promptActive = true

			require.NoError(t, stop(a))
			syncTestPeer(t, a)

			assert.Equal(t, []string{"jsonrpc:72"}, sink.CanceledControls())
			assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"}}`, agenttest.JSONRPCResultsByID(t, rawLinesOf(requests()))["72"])
			assert.Equal(t, -1, indexOfAnswer(t, requests(), 71))
			assert.True(t, a.OutstandingControlForTest("jsonrpc:71"), "the reader can still answer it")
		})
	}
}

// A retired session's request that carries no cancel answer takes a JSON-RPC
// error. The protocol defines no outcome for it, and the error releases the
// agent without a decision that the reader never made.
func TestACPRefusesAnAnswerlessRequestOfARetiredSessionWithAnError(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)

	frame, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 73, "method": "provider/question", "params": map[string]any{"sessionId": "session-1"}})
	require.NoError(t, err)
	a.PublishSessionControlRequest(providerkit.ParseLine(frame), nil)
	syncTestPeer(t, a)

	assert.Empty(t, sink.PublishedControls())
	answer := indexOfAnswer(t, requests(), 73)
	require.NotEqual(t, -1, answer)
	var response struct {
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code int `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(requests()[answer].Raw), &response))
	assert.Empty(t, response.Result)
	assert.Equal(t, acpStaleSessionRequestError, response.Error.Code)
}

// The copy of the open rows ignores a row with no key, moves a row that the
// registry re-keyed only when both keys are real, and forgets what takeAll
// returns.
func TestACPOpenRows(t *testing.T) {
	t.Parallel()
	var rows acpOpenRows
	rows.closed("never-opened")
	assert.Empty(t, rows.takeAll(), "an empty copy holds nothing")

	rows.opened("")
	rows.opened("a")
	rows.opened("b")
	rows.opened("a")
	rows.renamed("missing", "c")
	rows.renamed("b", "")
	rows.renamed("a", "a2")
	rows.closed("never-opened")

	assert.ElementsMatch(t, []string{"a2", "b"}, rows.takeAll())
	assert.Empty(t, rows.takeAll(), "takeAll forgets what it returns")
}

// A context clear ends each row of the outgoing session with the transcript of
// its child, and releases the child agent, as a close that the agent reported
// does. The row routes nothing afterwards.
func TestACPClearContextReleasesTheChildAgentsOfTheOutgoingRows(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	probe := &registryProbeSink{ProviderServices: a.sink}
	a.sink = probe
	a.ApplySubagentObservation(&SubagentObservation{
		RowKey: "call_background", ChildAgentKey: "call_background", Title: "Background helper", Status: bgtask.StatusRunning,
	})
	require.True(t, a.FeedChildUpdate("call_background", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Half done."}}`)))

	_, err := a.ClearContext()
	require.NoError(t, err)

	assert.Equal(t, []string{"child-of-call_background"}, probe.releasedChildren())
	messages := sink.Child("child-of-call_background").Messages()
	require.Len(t, messages, 1)
	_, text, completion, ok := decodeAssembled(messages[0].Content)
	require.True(t, ok)
	assert.Equal(t, "Half done.", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion, "the clear stopped the child")
	assert.False(t, a.FeedChildUpdate("call_background", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"late"}}`)),
		"the row of the retired session routes nothing")
}

// A child that the registry resolved, because a process before a worker restart
// created its row, is in no open row of this agent. Its conversation still ends
// with the session that fed it, as a stop.
func TestACPClearContextEndsAChildThatTheRegistryResolved(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	_, err := sink.EnsureChildAgent("call_old", "call_old", "Old helper")
	require.NoError(t, err)
	require.True(t, a.FeedChildUpdate("call_old", json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Cut off."}}`)))
	require.Empty(t, sink.Child("child-of-call_old").Messages(), "the text waits for the end of its segment")

	_, err = a.ClearContext()
	require.NoError(t, err)

	messages := sink.Child("child-of-call_old").Messages()
	require.Len(t, messages, 1)
	_, text, completion, ok := decodeAssembled(messages[0].Content)
	require.True(t, ok)
	assert.Equal(t, "Cut off.", text)
	assert.Equal(t, agent.MessageCompletionInterrupted, completion)
}

// A row whose close failed stays open, so the next context clear closes it.
func TestACPClearContextClosesARowWhoseCloseFailed(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	probe := &registryProbeSink{ProviderServices: a.sink}
	a.sink = probe
	a.ApplySubagentObservation(&SubagentObservation{RowKey: "call_flaky", Title: "Flaky helper", Status: bgtask.StatusRunning})
	probe.setFailClose(true)
	a.ApplySubagentObservation(&SubagentObservation{RowKey: "call_flaky", Status: bgtask.StatusCompleted, CloseRow: true, Mode: ModeCloseOnly})
	row, ok := sink.BackgroundTask("call_flaky")
	require.True(t, ok)
	require.Equal(t, bgtask.StatusRunning, row.Status, "the registry refused the close")
	probe.setFailClose(false)

	_, err := a.ClearContext()
	require.NoError(t, err)

	row, ok = sink.BackgroundTask("call_flaky")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

// A row that the provider re-keyed closes under its new key at a context clear.
// The old key identifies no row any more, so a close of it would change nothing
// and leave the row running.
func TestACPClearContextClosesARenamedRowUnderItsNewKey(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	a.ApplySubagentObservation(&SubagentObservation{RowKey: "call-1", Title: "helper", Status: bgtask.StatusRunning})
	a.ApplySubagentObservation(&SubagentObservation{RowKey: "ses-1", RenameFrom: "call-1", Status: bgtask.StatusRunning})

	_, err := a.ClearContext()
	require.NoError(t, err)

	assert.Equal(t, []string{"ses-1"}, agenttest.RowKeys(&sink.Sink))
	row, ok := sink.BackgroundTask("ses-1")
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusStopped, row.Status)
}

// The provider reads each control request that passes BEFORE the reader sees
// its card, so a withdrawal that follows the publication finds it recorded. A
// request that the base refuses reaches neither.
func TestACPControlRequestObserverReadsEachPublishedRequestFirst(t *testing.T) {
	t.Parallel()
	a, sink, _ := newRetiringTestAgent(t)
	var observed []string
	var publishedBefore []int
	a.hooks.ControlRequestObserver = func(line *providerkit.ParsedLine) {
		observed = append(observed, line.Method)
		publishedBefore = append(publishedBefore, len(sink.PublishedControls()))
	}

	a.HandleOutput(permissionRequest(t, 80, "session-1"))
	a.HandleOutput(elicitationRequest(t, 81, "session-1"))
	answerlessRequest(t, a, 82)

	assert.Equal(t, []string{acpMethodSessionRequestPermission, "elicitation/create", "provider/question"}, observed)
	assert.Equal(t, []int{0, 1, 2}, publishedBefore, "the observer runs before each publication")
	require.Len(t, sink.PublishedControls(), 3)

	_, err := a.ClearContext()
	require.NoError(t, err)
	a.HandleOutput(permissionRequest(t, 83, "session-1"))
	syncTestPeer(t, a)

	assert.Len(t, observed, 3, "a refused request reaches no observer")
	assert.Len(t, sink.PublishedControls(), 3)
}

// A frame of a retired session that carries no id asks for no answer, so the
// refusal writes nothing, and the reader sees no card.
func TestACPRefusesANotificationOfARetiredSessionWithoutAnAnswer(t *testing.T) {
	t.Parallel()
	a, sink, requests := newRetiringTestAgent(t)
	_, err := a.ClearContext()
	require.NoError(t, err)
	syncTestPeer(t, a)
	before := len(requests())

	a.HandleOutput([]byte(`{"jsonrpc":"2.0","method":"session/request_permission","params":{"sessionId":"session-1","toolCall":{"toolCallId":"call_permission"}}}`))
	syncTestPeer(t, a)

	assert.Empty(t, sink.PublishedControls())
	written := requests()[before:]
	require.Len(t, written, 1, "the agent wrote nothing but the sync request")
	assert.Equal(t, "test/sync", written[0].Method)
}

// An agent whose handshake never opened a session has no outgoing session: the
// clear cancels no turn, closes no session, and hands the provider nothing to
// retire.
func TestACPClearContextOfNoSessionRetiresNothing(t *testing.T) {
	t.Parallel()
	a, _, requests := newRetiringTestAgent(t)
	a.SetSessionIDForTest("")
	a.SetClosesSessionsForTest(true)
	a.promptActive = true
	var retired []string
	a.hooks.RetireSession = func(sessionID string) { retired = append(retired, sessionID) }

	sessionID, err := a.ClearContext()
	require.NoError(t, err)
	require.Equal(t, "session-2", sessionID)
	syncTestPeer(t, a)

	assert.Empty(t, sessionIDsOf(requests(), MethodSessionCancel))
	assert.Empty(t, sessionIDsOf(requests(), MethodSessionClose))
	assert.Empty(t, retired)
}

// rawLinesOf joins the frames that the agent wrote, one per line.
func rawLinesOf(requests []agenttest.RecordedRequest) string {
	var out string
	for _, request := range requests {
		out += request.Raw + "\n"
	}
	return out
}
