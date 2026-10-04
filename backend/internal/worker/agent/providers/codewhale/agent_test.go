package codewhale

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// turnsRoute is the turn route of the test thread.
var turnsRoute = threadPath(testThreadID, threadRouteTurns)

// acceptTurns makes the runtime start each turn with id.
func acceptTurns(rt *fakeRuntime, id string) {
	rt.respondJSON(http.MethodPost, turnsRoute, http.StatusCreated, map[string]any{
		"thread": map[string]any{"id": testThreadID},
		"turn":   map[string]any{"id": id, "status": "queued"},
	})
}

func TestSendInputStartsATurn(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptTurns(rt, testTurnID)
	a, sink := newTestAgent(t, rt)

	require.NoError(t, a.SendInput("Say hello.", nil))
	assert.Equal(t, map[string]any{"prompt": "Say hello."}, rt.lastBody(t, http.MethodPost, turnsRoute))
	requests := rt.requestsTo(http.MethodPost, turnsRoute)
	assert.Equal(t, "Bearer "+testToken, requests[0].Auth)
	// The reply states the turn, so the flag moves before any event arrives.
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.Equal(t, testTurnID, a.turnID)
}

func TestSendInputSendsTheEffortUnlessItIsAuto(t *testing.T) {
	t.Parallel()
	for effort, want := range map[string]any{"high": "high", agent.EffortAuto: nil, "": nil} {
		rt := newFakeRuntime(t)
		acceptTurns(rt, testTurnID)
		a, _ := newTestAgent(t, rt)
		a.settings.effort = effort
		require.NoError(t, a.SendInput("Go.", nil))
		assert.Equal(t, want, rt.lastBody(t, http.MethodPost, turnsRoute)["reasoning_effort"], effort)
	}
}

func TestSendInputCarriesAttachments(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptTurns(rt, testTurnID)
	a, _ := newTestAgent(t, rt)

	require.NoError(t, a.SendInput("Look.", []*leapmuxv1.Attachment{
		{Filename: "notes.txt", MimeType: "text/plain", Data: []byte("alpha")},
		{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}},
	}))
	body := rt.lastBody(t, http.MethodPost, turnsRoute)
	assert.Contains(t, body["prompt"], "Look.")
	assert.Contains(t, body["prompt"], "alpha")
	assert.Equal(t, []any{map[string]any{"mime": "image/png", "dataBase64": base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G'})}}, body["images"])
}

func TestSendInputRefusesAnImageTheModelCannotRead(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	a.settings.model = "deepseek-flash"
	a.catalog = []providerModel{{ID: "deepseek-flash", ImageInput: capabilityUnsupported}}

	err := a.SendInput("Look.", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}})
	assert.ErrorContains(t, err, "does not accept images")
	assert.Empty(t, rt.requestsTo(http.MethodPost, turnsRoute), "nothing reached the runtime")
}

func TestSendInputRefusesWhileATurnRuns(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, sink := newTestAgent(t, rt)
	a.turnID = testTurnID

	err := a.SendInput("Another.", nil)
	agenttest.AssertBusyRefusalRepublishesTheTurn(t, &sink.Sink, a, err)
	assert.Empty(t, rt.requestsTo(http.MethodPost, turnsRoute))
}

func TestSendInputReadsAConflictAsBusy(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, turnsRoute, http.StatusConflict, "Thread already has an active turn")
	a, _ := newTestAgent(t, rt)

	err := a.SendInput("Hi.", nil)
	assert.ErrorIs(t, err, agent.ErrAgentBusy)
	assert.ErrorContains(t, err, "active turn")
}

func TestSendInputReadsALostReplyAsUncertain(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.handle(http.MethodPost, turnsRoute, func(w http.ResponseWriter, _ *http.Request) {
		// The connection drops after the runtime may have taken the turn.
		hijacker, ok := w.(http.Hijacker)
		require.True(t, ok)
		conn, _, err := hijacker.Hijack()
		require.NoError(t, err)
		_ = conn.Close()
	})
	a, _ := newTestAgent(t, rt)

	assert.ErrorIs(t, a.SendInput("Hi.", nil), agent.ErrDeliveryUncertain)
}

func TestSendInputReportsAnyOtherRefusal(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	rt.respondStatus(http.MethodPost, turnsRoute, http.StatusBadRequest, "image inputs require a model with explicitly supported image input")
	a, _ := newTestAgent(t, rt)

	err := a.SendInput("Hi.", nil)
	assert.NotErrorIs(t, err, agent.ErrAgentBusy)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
	assert.ErrorContains(t, err, "image inputs require")
}

func TestSendInputNeedsAThreadAndARunningAgent(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	a.threadID = ""
	assert.ErrorContains(t, a.SendInput("Hi.", nil), "no thread")

	stopped, _ := newTestAgent(t, nil)
	stopped.SetStoppedForTest(true)
	assert.ErrorContains(t, stopped.SendInput("Hi.", nil), "stopped")
}

func TestSendInputForSession(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	acceptTurns(rt, testTurnID)
	a, _ := newTestAgent(t, rt)

	agenttest.AssertRejectsMissingAndReplacedSessions(t, a)
	require.NoError(t, a.SendInputForSession(testThreadID, "Hi.", nil))
	assert.Len(t, rt.requestsTo(http.MethodPost, turnsRoute), 1)
}

func TestSteerInput(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	steer := turnPath(testThreadID, testTurnID, turnRouteSteer)
	rt.respondJSON(http.MethodPost, steer, http.StatusOK, map[string]any{"id": testTurnID})
	a, _ := newTestAgent(t, rt)
	assert.True(t, a.SupportsSteering())

	assert.ErrorIs(t, a.SteerInput("More.", nil), agent.ErrNoActiveTurn, "no turn runs")
	a.turnID = testTurnID
	require.NoError(t, a.SteerInput("More.", nil))
	assert.Equal(t, map[string]any{"prompt": "More."}, rt.lastBody(t, http.MethodPost, steer))

	err := a.SteerInput("Look.", []*leapmuxv1.Attachment{{Filename: "shot.png", MimeType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}})
	assert.ErrorContains(t, err, "cannot add an image")
}

func TestInterrupt(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := turnPath(testThreadID, testTurnID, turnRouteInterrupt)
	rt.respondJSON(http.MethodPost, route, http.StatusOK, map[string]any{"id": testTurnID})
	a, _ := newTestAgent(t, rt)

	require.NoError(t, a.Interrupt(), "no turn runs, so nothing is sent")
	assert.Empty(t, rt.requestsTo(http.MethodPost, route))
	a.turnID = testTurnID
	require.NoError(t, a.Interrupt())
	assert.Len(t, rt.requestsTo(http.MethodPost, route), 1)
	// The interrupt changes no state: the runtime's own turn end does.
	assert.Equal(t, testTurnID, a.turnID)
}

func TestInterruptOfATurnThatEndedIsNoError(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusNotFound, http.StatusConflict} {
		rt := newFakeRuntime(t)
		rt.respondStatus(http.MethodPost, turnPath(testThreadID, testTurnID, turnRouteInterrupt), status, "no turn")
		a, _ := newTestAgent(t, rt)
		a.turnID = testTurnID
		assert.NoError(t, a.Interrupt(), status)
	}
}

func TestCompactContext(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	compact := threadPath(testThreadID, threadRouteCompact)
	rt.respondJSON(http.MethodPost, compact, http.StatusOK, map[string]any{"turn": map[string]any{"id": "turn_compact"}})
	a, sink := newTestAgent(t, rt)

	require.NoError(t, a.CompactContext())
	assert.Equal(t, "turn_compact", a.turnID, "the compaction runs as a turn")
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.ErrorIs(t, a.CompactContext(), agent.ErrAgentBusy)
}

func TestClearContextRestartsTheAgent(t *testing.T) {
	t.Parallel()
	a, _ := newTestAgent(t, nil)
	_, err := a.ClearContext()
	assert.ErrorIs(t, err, agent.ErrContextClearUnsupported)
}

func TestTurnTokensRise(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	agenttest.AssertRisingTurnTokens(t, &sink.Sink, a)
}

func TestTurnFlagMovesOnlyOnTurnFrames(t *testing.T) {
	t.Parallel()
	agenttest.AssertTurnFrames(t, codewhaleTurnFrameCases(), func(t *testing.T, tc agenttest.TurnFrameCase) []bool {
		rt := newFakeRuntime(t)
		a, sink := newTestAgent(t, rt)
		// A turn runs, so a turn end has a turn to end and a start of ANOTHER turn
		// is a real change.
		a.turnID = testTurnID
		a.HandleOutput([]byte(tc.Line))
		return sink.TurnActives()
	})
}

func codewhaleTurnFrameCases() []agenttest.TurnFrameCase {
	cases := []agenttest.TurnFrameCase{
		{Name: "turn.started", Line: string(turnStartedEvent(1, "turn_next")), Moves: true},
		{Name: "turn.completed", Line: string(turnCompletedEvent(1, testTurnID, contracts.CodewhaleTurnStatusCompleted)), Moves: true},
		{Name: "turn.lifecycle", Line: string(runtimeEvent(1, "turn.lifecycle", testTurnID, "", map[string]any{"status": "in_progress"}))},
		{Name: "turn.usage", Line: string(runtimeEvent(1, "turn.usage", testTurnID, "", map[string]any{"usage": map[string]any{"input_tokens": 10, "output_tokens": 2}}))},
		{Name: "turn.steered", Line: string(runtimeEvent(1, "turn.steered", testTurnID, "", map[string]any{"input": "more"}))},
		{Name: "turn.steer_dropped", Line: string(runtimeEvent(1, "turn.steer_dropped", testTurnID, "", map[string]any{"input": "more", "reason": "ended"}))},
		{Name: "turn.interrupt_requested", Line: string(runtimeEvent(1, "turn.interrupt_requested", testTurnID, "", map[string]any{}))},
		{Name: "item.started", Line: string(toolStartEvent(1, "item_1", "call_1", "bash", map[string]any{"command": "ls"}))},
		{Name: "item.delta", Line: string(runtimeEvent(1, "item.delta", testTurnID, "item_2", map[string]any{"delta": "hi", "kind": "agent_message"}))},
		{Name: "item.completed", Line: string(itemEvent(1, "item.completed", "item_2", "agent_message", "Hello.", nil))},
		{Name: "item.failed", Line: string(itemEvent(1, "item.failed", "item_3", "error", "boom", nil))},
		{Name: "item.interrupted", Line: string(itemEvent(1, "item.interrupted", "item_2", "agent_message", "Hel", nil))},
		{Name: "item.canceled", Line: string(itemEvent(1, "item.canceled", "item_2", "agent_message", "", nil))},
		{Name: "approval.required", Line: string(approvalEvent(1, "ap1", "call_1", "bash"))},
		{Name: "approval.decided", Line: string(runtimeEvent(1, "approval.decided", testTurnID, "", map[string]any{"approval_id": "ap1"}))},
		{Name: "approval.timeout", Line: string(runtimeEvent(1, "approval.timeout", testTurnID, "", map[string]any{"approval_id": "ap1", "timeout_secs": 300}))},
		{Name: "user_input.required", Line: string(userInputEvent(1, "q1"))},
		{Name: "user_input.answered", Line: string(runtimeEvent(1, "user_input.answered", testTurnID, "", map[string]any{"id": "q1"}))},
		{Name: "user_input.canceled", Line: string(runtimeEvent(1, "user_input.canceled", testTurnID, "", map[string]any{"id": "q1"}))},
		{Name: "thread.started", Line: string(runtimeEvent(1, "thread.started", "", "", map[string]any{"thread": map[string]any{"id": testThreadID}}))},
		{Name: "thread.updated", Line: string(runtimeEvent(1, "thread.updated", "", "", map[string]any{"thread": map[string]any{"id": testThreadID, "mode": "plan"}}))},
		{Name: "thread_goal_updated", Line: string(runtimeEvent(1, "thread_goal_updated", "", "", map[string]any{"goal": map[string]any{"objective": "Ship", "status": "active"}}))},
		{Name: "thread_goal_cleared", Line: string(runtimeEvent(1, "thread_goal_cleared", "", "", map[string]any{"thread_id": testThreadID}))},
		{Name: "sandbox.denied", Line: string(runtimeEvent(1, "sandbox.denied", testTurnID, "", map[string]any{"tool_name": "bash"}))},
		{Name: "runtime.store_failure", Line: string(runtimeEvent(1, "runtime.store_failure", "", "", map[string]any{"message": "disk full"}))},
		{Name: "model.tools.snapshot", Line: string(runtimeEvent(1, "model.tools.snapshot", "", "", map[string]any{}))},
		{Name: "agent.spawned", Line: string(runtimeEvent(1, "agent.spawned", "", "", map[string]any{}))},
		{Name: "tool_call.requested", Line: string(runtimeEvent(1, "tool_call.requested", "", "", map[string]any{}))},
		{Name: "an event from a later release", Line: string(runtimeEvent(1, "turn.a_later_event", testTurnID, "", map[string]any{"turn": map[string]any{"id": "turn_other"}}))},
		{Name: "not an event", Line: `{"hello":"world"}`},
	}
	return cases
}

func TestTurnStartOfAnEndedTurnDoesNotReopenIt(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(turnCompletedEvent(2, testTurnID, contracts.CodewhaleTurnStatusCompleted))
	// The POST reply of the same turn arrives after its end.
	assert.False(t, a.markTurnStarted(testTurnID))
	assert.Empty(t, a.turnID)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

func TestTurnSetForgetsTheOldest(t *testing.T) {
	t.Parallel()
	var set turnSet
	set.add("")
	for i := range turnSetCapacity + 1 {
		set.add(string(rune('a' + i)))
	}
	set.add("b")
	assert.Len(t, set.ids, turnSetCapacity)
	assert.False(t, set.contains("a"), "the oldest turn is forgotten")
	assert.True(t, set.contains(string(rune('a'+turnSetCapacity))))
	assert.False(t, set.contains(""))
}

func TestStopSettlesWhatTheTurnLeftOpen(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	interrupt := turnPath(testThreadID, testTurnID, turnRouteInterrupt)
	rt.respondJSON(http.MethodPost, interrupt, http.StatusOK, map[string]any{"id": testTurnID})
	a, sink := newTestAgent(t, rt)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", "bash", map[string]any{"command": "sleep 9"}))
	a.HandleOutput(approvalEvent(3, "ap1", "call_1", "bash"))
	a.HandleOutput(runtimeEvent(4, "item.delta", testTurnID, "item_2", map[string]any{"delta": "partial", "kind": "agent_message"}))

	a.Stop()

	assert.Len(t, rt.requestsTo(http.MethodPost, interrupt), 1, "the running turn is interrupted before the process ends")
	assert.Contains(t, sink.CanceledControls(), "approval:ap1")
	last, _ := sink.LastTurnActive()
	assert.False(t, last)
	partial, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "partial", agent.MessageCompletionInterrupted)
	require.NoError(t, err)
	var retained, streamed bool
	for _, message := range sink.Messages() {
		if message.SpanID == "call_1" && message.Closing {
			retained = true
			assert.Equal(t, agent.MessageCompletionInterrupted, message.Completion)
			assert.Equal(t, "item.started", decodeJSON(t, message.Content)["event"], "the retained row is the call's own start")
		}
		if string(message.Content) == string(partial) {
			streamed = true
		}
	}
	assert.True(t, retained, "the running call is closed as incomplete")
	assert.True(t, streamed, "the streamed text is kept")
	// A second Stop is harmless.
	a.Stop()
}

// shortText cuts a line for a log, and the log must stay valid UTF-8.
func TestShortTextNeverSplitsARune(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "abc", shortText("  abc  ", 10), "shortText trims the text first")
	assert.Equal(t, "ab", shortText("abcdef", 2))
	// "é" is two bytes. A cut after its first byte moves back to the rune start.
	cut := shortText("héllo", 2)
	assert.Equal(t, "h", cut)
	assert.True(t, utf8.ValidString(cut))
	assert.Equal(t, "hé", shortText("héllo", 3))
	assert.Empty(t, shortText("abc", 0))
	assert.Empty(t, shortText("abc", -1), "a negative limit keeps nothing and does not panic")
}

func TestSteerInputNeedsARunningAgent(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	a, _ := newTestAgent(t, rt)
	a.turnID = testTurnID
	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.SteerInput("More.", nil), "stopped")
	assert.Empty(t, rt.requestsTo(http.MethodPost, turnPath(testThreadID, testTurnID, turnRouteSteer)))
	assert.Zero(t, openSteers(a), "a refused steer leaves no ticket")

	b, _ := newTestAgent(t, rt)
	b.threadID = ""
	b.turnID = testTurnID
	assert.ErrorIs(t, b.SteerInput("More.", nil), agent.ErrNoActiveTurn, "an agent with no thread steers nothing")
}

func TestInterruptReportsWhatItCannotStop(t *testing.T) {
	t.Parallel()
	rt := newFakeRuntime(t)
	route := turnPath(testThreadID, testTurnID, turnRouteInterrupt)
	rt.respondStatus(http.MethodPost, route, http.StatusInternalServerError, "boom")
	a, _ := newTestAgent(t, rt)
	a.turnID = testTurnID
	err := a.Interrupt()
	assert.ErrorContains(t, err, "boom", "a refusal other than an ended turn reaches the caller")

	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.Interrupt(), "stopped")
	assert.Len(t, rt.requestsTo(http.MethodPost, route), 1, "a stopped agent sends nothing")
}

func TestCompactContextRefusals(t *testing.T) {
	t.Parallel()
	compact := threadPath(testThreadID, threadRouteCompact)

	stopped, _ := newTestAgent(t, nil)
	stopped.SetStoppedForTest(true)
	assert.ErrorContains(t, stopped.CompactContext(), "stopped")

	threadless, _ := newTestAgent(t, nil)
	threadless.threadID = ""
	assert.ErrorContains(t, threadless.CompactContext(), "no thread")

	conflict := newFakeRuntime(t)
	conflict.respondStatus(http.MethodPost, compact, http.StatusConflict, "Thread already has an active turn")
	busy, busySink := newTestAgent(t, conflict)
	assert.ErrorIs(t, busy.CompactContext(), agent.ErrAgentBusy, "a turn the agent has not seen yet holds the thread")
	assert.Empty(t, busy.turnID)
	assert.Empty(t, busySink.TurnActives())

	failing := newFakeRuntime(t)
	failing.respondStatus(http.MethodPost, compact, http.StatusInternalServerError, "boom")
	broken, _ := newTestAgent(t, failing)
	err := broken.CompactContext()
	assert.ErrorContains(t, err, "boom")
	assert.NotErrorIs(t, err, agent.ErrAgentBusy)
	assert.NotErrorIs(t, err, agent.ErrDeliveryUncertain)
}

func TestTheTurnFlagIgnoresAStartThatChangesNothing(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	assert.False(t, a.markTurnStarted(""), "a start with no turn id")
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.TurnToolUses = 2
	assert.False(t, a.markTurnStarted(testTurnID), "the turn that already runs")
	assert.Equal(t, 2, a.TurnToolUses, "a repeated start keeps the turn's tool count")
	assert.Equal(t, []bool{true}, sink.TurnActives())
}

// The runtime runs one turn at a time, so the end of another turn is stale: it
// does not end the turn that runs, and a late start of that turn opens nothing.
func TestATurnEndOfAnotherTurnKeepsTheRunningOne(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(turnCompletedEvent(2, "turn_other", contracts.CodewhaleTurnStatusCompleted))
	assert.Equal(t, testTurnID, a.turnID)
	assert.Equal(t, []bool{true}, sink.TurnActives())
	assert.False(t, a.markTurnStarted("turn_other"), "the agent remembers the ended turn")
}

// A process that dies on its own ends what it left open as an error, not as
// an interrupt that the reader asked for.
func TestWaitSettlesWhatADeadProcessLeftOpen(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	a.HandleOutput(turnStartedEvent(1, testTurnID))
	a.HandleOutput(toolStartEvent(2, "item_1", "call_1", contracts.CodewhaleToolBash, map[string]any{"command": "sleep 9"}))
	a.HandleOutput(approvalEvent(3, "ap1", "call_1", contracts.CodewhaleToolBash))
	a.HandleOutput(runtimeEvent(4, "item.delta", testTurnID, "item_2", map[string]any{"delta": "partial", "kind": "agent_message"}))

	a.stopProcess()
	require.NoError(t, a.Wait())

	partial, err := agent.MarshalAssembledMessage(agent.AssembledMessageKindText, "partial", agent.MessageCompletionError)
	require.NoError(t, err)
	var closing []agenttest.Message
	var streamed bool
	for _, message := range sink.Messages() {
		if message.SpanID == "call_1" && message.Closing {
			closing = append(closing, message)
		}
		if string(message.Content) == string(partial) {
			streamed = true
		}
	}
	require.Len(t, closing, 1, "the exit closes the running call once")
	assert.Equal(t, agent.MessageCompletionError, closing[0].Completion)
	assert.True(t, streamed, "the exit keeps the streamed text, and its row states the crash")
	assert.Equal(t, []string{"approval:ap1"}, sink.CanceledControls())
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	assert.Empty(t, a.turnID)
}

// The stream goroutine is the normal caller of the dispatch, and a test or an
// out-of-band feed can call it at the same time. The dispatch lock serializes
// them, so every event lands once and the race detector finds nothing.
func TestConcurrentDispatchPersistsEveryEvent(t *testing.T) {
	t.Parallel()
	a, sink := newTestAgent(t, nil)
	const feeders = 16
	var wg sync.WaitGroup
	for i := range feeders {
		wg.Add(2)
		go func() {
			defer wg.Done()
			// The dispatch never deduplicates an unnumbered event, so each one lands.
			a.HandleOutput([]byte(`{"event":"item.completed","thread_id":"` + testThreadID + `","payload":{"item":{"id":"item_` + strconv.Itoa(i) + `","kind":"agent_message","detail":"Message ` + strconv.Itoa(i) + `."}}}`))
		}()
		go func() {
			defer wg.Done()
			_ = a.OptionGroups()
			a.PublishTurnActive()
		}()
	}
	wg.Wait()
	messages := sink.Messages()
	require.Len(t, messages, feeders)
	seen := make(map[string]bool, feeders)
	for _, message := range messages {
		seen[string(message.Content)] = true
	}
	assert.Len(t, seen, feeders, "each event lands exactly once")
}
