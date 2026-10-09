package mimo

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/quartz"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// interruptOnClock runs Interrupt while the abort-grace timer is trapped, and
// returns the grace that the timer asked the clock for. Interrupt arms the timer
// inside the call, and a trapped call waits for the test, so the call runs on
// its own goroutine.
func interruptOnClock(t *testing.T, a *Agent, clock *quartz.Mock) time.Duration {
	t.Helper()
	ctx := testutil.DeadlineContext(t)
	grace := clock.Trap().AfterFunc(mimoAbortGraceTimerTag)
	defer grace.Close()
	result := make(chan error, 1)
	go func() { result <- a.Interrupt(agent.StopContext{}) }()
	delay := testutil.WaitForTimer(t, ctx, grace)
	require.NoError(t, <-result)
	return delay
}

// assertNoTimer asserts that the agent armed no timer on its clock.
func assertNoTimer(t *testing.T, clock *quartz.Mock, message string) {
	t.Helper()
	_, pending := clock.Peek()
	assert.False(t, pending, message)
}

type heldAbortReply struct {
	arrived chan struct{}
	reply   chan int
}

func holdAbortReplies(t *testing.T, server *fakeServer, count int) []heldAbortReply {
	t.Helper()
	gates := make([]heldAbortReply, count)
	for i := range gates {
		gates[i] = heldAbortReply{arrived: make(chan struct{}), reply: make(chan int)}
		t.Cleanup(func() { close(gates[i].reply) })
	}
	var requests atomic.Int32
	server.handle("POST /session/ses_test/abort", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		index := int(requests.Add(1)) - 1
		if index >= len(gates) {
			writeJSON(w, http.StatusInternalServerError, `{"error":"unexpected abort"}`)
			return
		}
		gate := gates[index]
		close(gate.arrived)
		status, present := <-gate.reply
		if !present {
			status = http.StatusInternalServerError
		}
		if status == http.StatusOK {
			writeJSON(w, status, `true`)
			return
		}
		writeJSON(w, status, `{"error":"the controlled abort failed"}`)
	})
	return gates
}

func awaitAbortArrival(t *testing.T, gate heldAbortReply) {
	t.Helper()
	select {
	case <-gate.arrived:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("The controlled abort did not arrive.")
	}
}

func startInterrupt(a *Agent) <-chan error {
	result := make(chan error, 1)
	go func() { result <- a.Interrupt(agent.StopContext{}) }()
	return result
}

func awaitInterrupt(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("The controlled interrupt did not return.")
		return nil
	}
}

func beginPartialAnswer(t *testing.T, a *Agent, messageID, partID, text string) {
	t.Helper()
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, messageID, roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, partID, messageID, "", false),
		deltaEvent(t, partID, messageID, text))
}

func nativeAbortMarker(t *testing.T, kind, messageID string) []byte {
	t.Helper()
	errorBody := map[string]any{"name": contracts.MiMoErrorNameAborted, "data": map[string]any{"message": "Aborted"}}
	if kind == "session" {
		return eventJSON(t, contracts.MiMoEventSessionError, map[string]any{"sessionID": testSessionID, "error": errorBody})
	}
	return eventJSON(t, eventMessageUpdated, map[string]any{"sessionID": testSessionID, "info": map[string]any{
		"id": messageID, "sessionID": testSessionID, "role": roleAssistant, "agentID": mainActorID, "error": errorBody,
	}})
}

func TestFailedInterruptsKeepNaturalTextAndTurnComplete(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_normal", "part_normal", "The complete answer.")
	feed(a, textPartEvent(t, partTypeReasoning, "part_reasoning", "msg_normal", "The complete reasoning.", false))
	gates := holdAbortReplies(t, server, 2)
	first := startInterrupt(a)
	awaitAbortArrival(t, gates[0])
	second := startInterrupt(a)
	awaitAbortArrival(t, gates[1])
	feed(a, textPartEvent(t, partTypeText, "part_normal", "msg_normal", "The complete answer.", true),
		textPartEvent(t, partTypeReasoning, "part_reasoning", "msg_normal", "The complete reasoning.", true),
		statusEvent(t, contracts.MiMoStatusTypeIdle))
	for i, result := range []<-chan error{first, second} {
		gates[i].reply <- http.StatusInternalServerError
		require.ErrorContains(t, awaitInterrupt(t, result), "abort the MiMo turn")
	}
	rows := sink.Messages()
	require.Len(t, rows, 3)
	for _, row := range rows[:2] {
		_, _, completion := assembledText(t, row.Content)
		assert.Equal(t, string(agent.MessageCompletionComplete), completion)
	}
	assert.Equal(t, agent.MessageCompletionComplete, rows[2].Completion)
	assert.Empty(t, a.interruptRequests)
}

func TestNativeAbortMarkerSurvivesFailedAcknowledgement(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"session", "message"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_cut", "part_cut", "The actual partial answer.")
			gate := holdAbortReplies(t, server, 1)[0]
			result := startInterrupt(a)
			awaitAbortArrival(t, gate)
			feed(a, nativeAbortMarker(t, kind, "msg_cut"))
			gate.reply <- http.StatusInternalServerError
			require.Error(t, awaitInterrupt(t, result))
			feed(a, textPartEvent(t, partTypeText, "part_cut", "msg_cut", "The actual partial answer.", true),
				nativeAbortMarker(t, "message", "msg_cut"),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The actual partial answer.", text)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
		})
	}
}

func TestAcceptedInterruptBeforeEndMarksTextAndTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	beginPartialAnswer(t, a, "msg_cut", "part_cut", "The cut answer.")
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	feed(a, textPartEvent(t, partTypeText, "part_cut", "msg_cut", "The cut answer.", true), statusEvent(t, contracts.MiMoStatusTypeIdle))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, _, completion := assembledText(t, rows[0].Content)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
}

func TestAcceptedAcknowledgementAfterNativeEndKeepsItsEvidence(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"session", "message"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_cut", "part_cut", "The native cut answer.")
			gate := holdAbortReplies(t, server, 1)[0]
			result := startInterrupt(a)
			awaitAbortArrival(t, gate)
			feed(a, nativeAbortMarker(t, kind, "msg_cut"),
				textPartEvent(t, partTypeText, "part_cut", "msg_cut", "The native cut answer.", true),
				nativeAbortMarker(t, "message", "msg_cut"),
				statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 2, "the native evidence publishes before HTTP resolves")
			_, _, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
			gate.reply <- http.StatusOK
			require.NoError(t, awaitInterrupt(t, result))
			assert.Empty(t, a.interruptRequests)
		})
	}
}

func TestConcurrentAbortsKeepTheAcceptedAttempt(t *testing.T) {
	t.Parallel()
	for _, failFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "accept before failure", true: "failure before acceptance"}[failFirst], func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_cut", "part_cut", "The accepted cut.")
			gates := holdAbortReplies(t, server, 2)
			first := startInterrupt(a)
			awaitAbortArrival(t, gates[0])
			second := startInterrupt(a)
			awaitAbortArrival(t, gates[1])
			if failFirst {
				gates[0].reply <- http.StatusInternalServerError
				require.Error(t, awaitInterrupt(t, first))
			}
			gates[1].reply <- http.StatusOK
			require.NoError(t, awaitInterrupt(t, second))
			if !failFirst {
				gates[0].reply <- http.StatusInternalServerError
				require.Error(t, awaitInterrupt(t, first))
			}
			feed(a, textPartEvent(t, partTypeText, "part_cut", "msg_cut", "The accepted cut.", true), statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 2)
			_, _, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
			assert.Equal(t, agent.MessageCompletionInterrupted, rows[1].Completion)
		})
	}
}

func TestOldAbortReplyDoesNotMarkAReplacementTurn(t *testing.T) {
	t.Parallel()
	for _, status := range []int{http.StatusOK, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_old", "part_old", "The old complete answer.")
			gate := holdAbortReplies(t, server, 1)[0]
			result := startInterrupt(a)
			awaitAbortArrival(t, gate)
			feed(a, textPartEvent(t, partTypeText, "part_old", "msg_old", "The old complete answer.", true), statusEvent(t, contracts.MiMoStatusTypeIdle))
			beginPartialAnswer(t, a, "msg_new", "part_new", "The replacement answer.")
			gate.reply <- status
			if status == http.StatusOK {
				require.NoError(t, awaitInterrupt(t, result))
			} else {
				require.Error(t, awaitInterrupt(t, result))
			}
			feed(a, textPartEvent(t, partTypeText, "part_new", "msg_new", "The replacement answer.", true), statusEvent(t, contracts.MiMoStatusTypeIdle))
			rows := sink.Messages()
			require.Len(t, rows, 4)
			for _, index := range []int{0, 2} {
				_, _, completion := assembledText(t, rows[index].Content)
				assert.Equal(t, string(agent.MessageCompletionComplete), completion)
				assert.Equal(t, agent.MessageCompletionComplete, rows[index+1].Completion)
			}
		})
	}
}

func TestIdleResolvesFinalPartsWithoutCompletionMetadata(t *testing.T) {
	t.Parallel()
	for _, accepted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unconfirmed", true: "accepted"}[accepted], func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_fallback", "part_z", "The first prefix.")
			feed(a, textPartEvent(t, partTypeReasoning, "part_a", "msg_fallback", "The reasoning prefix.", false))
			gate := holdAbortReplies(t, server, 1)[0]
			result := startInterrupt(a)
			awaitAbortArrival(t, gate)
			if accepted {
				gate.reply <- http.StatusOK
				require.NoError(t, awaitInterrupt(t, result))
			}
			feed(a, textPartEvent(t, partTypeText, "part_z", "msg_fallback", "The first full final.", true),
				textPartEvent(t, partTypeReasoning, "part_a", "msg_fallback", "The second full final.", true))
			assert.Empty(t, sink.Messages(), "final parts wait while the message outcome remains unknown")
			feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
			if !accepted {
				gate.reply <- http.StatusInternalServerError
				require.Error(t, awaitInterrupt(t, result))
			}
			rows := sink.Messages()
			require.Len(t, rows, 3)
			_, text, completion := assembledText(t, rows[0].Content)
			assert.Equal(t, "The first full final.", text)
			want := agent.MessageCompletionComplete
			if accepted {
				want = agent.MessageCompletionInterrupted
			}
			assert.Equal(t, string(want), completion)
			_, text, completion = assembledText(t, rows[1].Content)
			assert.Equal(t, "The second full final.", text)
			assert.Equal(t, string(want), completion)
			assert.Equal(t, want, rows[2].Completion)
		})
	}
}

func TestEmptyFinalPartsCompleteTheirProgressAfterAttribution(t *testing.T) {
	t.Parallel()
	for _, text := range []string{"", " \n\t "} {
		t.Run(text, func(t *testing.T) {
			t.Parallel()
			a, sink, server := newSinkTestAgent(t)
			beginPartialAnswer(t, a, "msg_empty", "part_empty", "")
			gate := holdAbortReplies(t, server, 1)[0]
			result := startInterrupt(a)
			awaitAbortArrival(t, gate)
			feed(a, textPartEvent(t, partTypeText, "part_empty", "msg_empty", text, true))
			for _, progress := range sink.ProgressUpdates() {
				assert.NotEqual(t, agent.ProgressModelComplete, progress.Operation)
			}
			feed(a, messageEvent(t, "msg_empty", roleAssistant, mainActorID, true))
			completions := 0
			for _, progress := range sink.ProgressUpdates() {
				if progress.Operation == agent.ProgressModelComplete {
					completions++
				}
			}
			assert.Equal(t, 1, completions)
			assert.Empty(t, assembledRows(sink))
			gate.reply <- http.StatusInternalServerError
			require.Error(t, awaitInterrupt(t, result))
		})
	}
}

func TestInterruptWithoutATurnSendsNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, server.allRequests())
	assertNoTimer(t, clock, "no abort means no check after the grace")
}

func TestInterruptAbortsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))

	assert.Equal(t, mimoAbortGrace, interruptOnClock(t, a, clock))
	assert.Len(t, server.requestsTo("POST /session/ses_test/abort"), 1)

	// MiMo ends an aborted turn at once, with an abort error and an idle.
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))
	messages := sink.Messages()
	require.NotEmpty(t, messages)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[len(messages)-1].Completion)

	// The grace ends, and the check reads the server's status: no turn runs.
	clock.Advance(mimoAbortGrace).MustWait(testutil.DeadlineContext(t))
	assert.Len(t, server.requestsTo("GET /session/status"), 1, "the check after the grace reads the server's status")
	assert.Equal(t, []bool{true, false}, sink.TurnActives(), "the check after the grace finds the turn already ended")
}

// An abort whose idle never arrives still ends the turn: the check after the
// grace compares the turn with the server's own status.
func TestInterruptEndsATurnWhoseIdleNeverArrives(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))

	interruptOnClock(t, a, clock)
	clock.Advance(mimoAbortGrace - time.Nanosecond).MustWait(testutil.DeadlineContext(t))
	assert.Equal(t, []bool{true}, sink.TurnActives(), "the grace has not ended yet")

	clock.Advance(time.Nanosecond).MustWait(testutil.DeadlineContext(t))
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
	messages := sink.Messages()
	require.Len(t, messages, 1)
	assert.True(t, messages[0].TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[0].Completion)
	assert.Equal(t, contracts.MiMoEventSessionStatus, rowTypes(t, messages)[0], "the divider reads as the idle MiMo would send")
}

// MiMo ends the text part that an abort cut with a final update that states the part's
// end, as it ends a finished part. That update can arrive while the abort request still
// waits for its answer. The text keeps the interruption marker all the same.
func TestInterruptMarksTheTextThatTheAbortCut(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
		deltaEvent(t, "prt_t", "msg_1", "Half a sen"),
	)
	server.handle("POST /session/ses_test/abort", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		feed(a, nativeAbortMarker(t, "session", "msg_1"), textPartEvent(t, partTypeText, "prt_t", "msg_1", "Half a sen", true),
			nativeAbortMarker(t, "message", "msg_1"))
		writeJSON(w, http.StatusOK, `true`)
	})

	interruptOnClock(t, a, clock)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))

	messages := sink.Messages()
	require.Len(t, messages, 2, "the cut text, then the divider")
	_, text, completion := assembledText(t, messages[0].Content)
	assert.Equal(t, "Half a sen", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.True(t, messages[1].TurnEnd)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[1].Completion)
}

// A part that ended before the reader stopped the turn finished, so it keeps no marker.
func TestInterruptKeepsATextThatEndedBeforeItComplete(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
		deltaEvent(t, "prt_t", "msg_1", "A whole sentence."),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "A whole sentence.", true),
	)

	interruptOnClock(t, a, clock)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle))

	messages := sink.Messages()
	require.Len(t, messages, 2)
	_, _, completion := assembledText(t, messages[0].Content)
	assert.Equal(t, string(agent.MessageCompletionComplete), completion)
}

func TestFailedInterruptLeavesTheLaterTurnInterruptIntact(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	firstArrived := make(chan struct{})
	releaseFirst := make(chan struct{})
	defer close(releaseFirst)
	var attempts atomic.Int32
	server.handle("POST /session/ses_test/abort", func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		if attempts.Add(1) == 1 {
			close(firstArrived)
			<-releaseFirst
			writeJSON(w, http.StatusInternalServerError, `{"error":"the first abort failed"}`)
			return
		}
		writeJSON(w, http.StatusOK, `true`)
	})
	firstResult := make(chan error, 1)
	go func() { firstResult <- a.Interrupt(agent.StopContext{}) }()
	select {
	case <-firstArrived:
	case <-testutil.DeadlineContext(t).Done():
		t.Fatal("The first abort did not reach the server.")
	}
	feed(a, statusEvent(t, contracts.MiMoStatusTypeIdle), statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_2", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t2", "msg_2", "", false),
		deltaEvent(t, "prt_t2", "msg_2", "Second partial answer"))
	interruptOnClock(t, a, clock)
	releaseFirst <- struct{}{}
	require.Error(t, <-firstResult)
	feed(a, textPartEvent(t, partTypeText, "prt_t2", "msg_2", "Second partial answer", true), statusEvent(t, contracts.MiMoStatusTypeIdle))
	messages := sink.Messages()
	require.Len(t, messages, 3)
	assert.Equal(t, agent.MessageCompletionComplete, messages[0].Completion, "the old turn ended naturally before its abort failed")
	_, text, completion := assembledText(t, messages[1].Content)
	assert.Equal(t, "Second partial answer", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.Equal(t, agent.MessageCompletionInterrupted, messages[2].Completion)
}

func TestInterruptThatFailsKeepsTheTurn(t *testing.T) {
	t.Parallel()
	a, sink, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy))
	server.respond("POST /session/ses_test/abort", http.StatusInternalServerError, `{}`)

	assert.ErrorContains(t, a.Interrupt(agent.StopContext{}), "abort the MiMo turn")
	assert.Empty(t, a.interruptRequests)
	assertNoTimer(t, clock, "the abort stopped nothing, so no check follows")
	assert.Equal(t, []bool{true}, sink.TurnActives())
}

// A turn with no session has nothing that an abort could address.
func TestInterruptWithoutASessionSendsNothing(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	clock := useMockClock(t, a)
	a.turnActive = true
	a.sessionID = ""

	require.NoError(t, a.Interrupt(agent.StopContext{}))
	assert.Empty(t, server.allRequests())
	assert.Empty(t, a.interruptRequests)
	assertNoTimer(t, clock, "no abort means no check after the grace")
}

func TestInterruptOnAStoppedAgent(t *testing.T) {
	t.Parallel()
	a, server := newTestAgent(t, nil)
	a.SetStoppedForTest(true)
	assert.ErrorContains(t, a.Interrupt(agent.StopContext{}), "stopped")
	assert.Empty(t, server.allRequests())
}

// feedUnfinishedTurn feeds a turn that stops in the middle: the main agent and
// a background subagent each stream part of a text and run a command that did
// not end.
func feedUnfinishedTurn(t *testing.T, a *Agent) {
	t.Helper()
	spawnActor(t, a, contracts.MiMoActorActionSpawn, true)
	feed(a,
		textPartEvent(t, partTypeText, "prt_t", "msg_p", "", false),
		deltaEvent(t, "prt_t", "msg_p", "Half a sen"),
		toolPartEvent(t, "prt_1", "msg_p", contracts.MiMoToolBash, "call-1", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "sleep 100"}}),
		messageEvent(t, "msg_c2", roleAssistant, actorID, false),
		textPartEvent(t, partTypeText, "prt_c", "msg_c2", "", false),
		deltaEvent(t, "prt_c", "msg_c2", "Sub half"),
		toolPartEvent(t, "prt_c3", "msg_c2", contracts.MiMoToolBash, "call-sub-01", toolState{Status: contracts.MiMoToolStatusRunning,
			Input: map[string]any{"command": "sleep 200"}}),
	)
}

// assertUnfinishedTurnClosed asserts that the rows of feedUnfinishedTurn were
// finished with completion, that the turn ended, and that the subagent's row
// took status.
func assertUnfinishedTurnClosed(t *testing.T, sink *agenttest.Sink, completion agent.MessageCompletion, status bgtask.Status) {
	t.Helper()
	messages := sink.Messages()
	require.Len(t, messages, 5, "the spawn call's two rows, the command's opener, the unfinished text, the command's closer")
	assert.Equal(t, "prt_1", messages[2].SpanID)
	assert.False(t, messages[2].Closing)
	_, text, textCompletion := assembledText(t, messages[3].Content)
	assert.Equal(t, "Half a sen", text)
	assert.Equal(t, string(completion), textCompletion)
	assert.Equal(t, "prt_1", messages[4].SpanID)
	assert.True(t, messages[4].Closing)
	assert.Equal(t, completion, messages[4].Completion)
	assert.Contains(t, sink.ClosedSpans(), "prt_1")

	childRows := sink.Child("child-of-" + spawnSpanID).Messages()
	require.Len(t, childRows, 4, "the prompt, the command's opener, the unfinished text, the command's closer")
	_, text, textCompletion = assembledText(t, childRows[2].Content)
	assert.Equal(t, "Sub half", text)
	assert.Equal(t, string(completion), textCompletion)
	assert.Equal(t, "prt_c3", childRows[3].SpanID)
	assert.True(t, childRows[3].Closing)
	assert.Equal(t, completion, childRows[3].Completion)
	assert.Equal(t, status, backgroundTask(t, sink, spawnSpanID).Status)

	active, published := sink.LastTurnActive()
	require.True(t, published)
	assert.False(t, active, "a process that ended runs no turn")
	assert.Contains(t, sink.ProgressUpdates(), agent.ResetProgress())
}

// Stop ends the turn that runs. What the turn streamed and never finished
// stays in the transcript, marked as interrupted.
func TestStopFinishesTheUnfinishedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	// Stop waits for the exit that its signal causes.
	a.SimulateExitForTest()
	feedUnfinishedTurn(t, a)
	require.Len(t, sink.Messages(), 3, "before the stop, only the command's opener follows the spawn")

	a.Stop()
	assertUnfinishedTurnClosed(t, sink, agent.MessageCompletionInterrupted, bgtask.StatusStopped)
	assert.False(t, a.turnActive)
}

// A process that exits on its own leaves its turn unfinished too. Its rows
// state the failure.
func TestUnexpectedExitFinishesTheUnfinishedTurn(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	feedUnfinishedTurn(t, a)
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	assertUnfinishedTurnClosed(t, sink, agent.MessageCompletionError, bgtask.StatusFailed)
}

// The worker calls Wait after Stop. The second call finds nothing left open.
func TestWaitAfterStopFinishesNothingTwice(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.SimulateExitForTest()
	feedUnfinishedTurn(t, a)

	a.Stop()
	rows, childRows := len(sink.Messages()), len(sink.Child("child-of-"+spawnSpanID).Messages())
	require.NoError(t, a.Wait())
	assert.Len(t, sink.Messages(), rows)
	assert.Len(t, sink.Child("child-of-"+spawnSpanID).Messages(), childRows)
}

// A failure that the turn end would state as its divider has no turn end when
// the process exits first. It stays in the transcript as a notification, since
// it can be the reason for the exit.
func TestExitPersistsAFailureThatNoTurnEndReported(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "the provider closed the connection")
	feed(a, statusEvent(t, contracts.MiMoStatusTypeBusy), failure)
	require.Zero(t, sink.NotificationCount(), "inside a turn, the failure waits for the turn end")
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	assert.Empty(t, a.pendingFailures)
	assert.Equal(t, []bool{true, false}, sink.TurnActives())
}

// The main agent's failed message claims its failure for the turn end. When the
// process exits first, that claimed failure is the notification.
func TestExitPersistsTheTurnsOwnFailure(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	failure := sessionErrorEvent(t, "APIError", "bad request")
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		failure,
		failedMessageEvent(t, "msg_1", mainActorID, "APIError", "bad request"),
	)
	require.NotNil(t, a.turnFailure, "the message claimed the failure for the turn end")
	a.SimulateExitForTest()

	require.NoError(t, a.Wait())
	require.Equal(t, 1, sink.NotificationCount())
	assert.JSONEq(t, string(failure), string(sink.LastNotification().Content))
	assert.Nil(t, a.turnFailure)
	assert.Empty(t, sink.Messages(), "no turn end follows the exit")
}

// Stop waits for the stream goroutine, so no event reaches a handler after the
// finish. A goroutine that does not end holds Stop only up to the wait's limit,
// and Stop then finishes the turn all the same.
func TestStopWaitsForTheStreamOnlyUpToItsLimit(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, sink, _ := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	a.SimulateExitForTest()
	// A stream goroutine that its cancel does not end.
	a.streamCancel = func() {}
	feed(a,
		statusEvent(t, contracts.MiMoStatusTypeBusy),
		messageEvent(t, "msg_1", roleAssistant, mainActorID, false),
		textPartEvent(t, partTypeText, "prt_t", "msg_1", "", false),
		deltaEvent(t, "prt_t", "msg_1", "Half"),
	)
	wait := clock.Trap().NewTimer(mimoStreamStopTimerTag)
	defer wait.Close()

	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	assert.Equal(t, mimoStreamStopWait, testutil.WaitForTimer(t, ctx, wait))
	clock.Advance(mimoStreamStopWait - time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
		t.Fatal("Stop returned before the wait's limit")
	default:
	}
	assert.Empty(t, sink.Messages(), "nothing is finished while the stream can still dispatch")

	clock.Advance(time.Nanosecond).MustWait(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not return at the wait's limit")
	}
	messages := sink.Messages()
	require.Len(t, messages, 1)
	_, text, completion := assembledText(t, messages[0].Content)
	assert.Equal(t, "Half", text)
	assert.Equal(t, string(agent.MessageCompletionInterrupted), completion)
	assert.False(t, a.turnActive)
}

// A stream goroutine that ends at once ends the wait at once: Stop arms the
// limit and stops it again.
func TestStopDoesNotWaitForAStreamThatEnded(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	a, _, server := newSinkTestAgent(t)
	clock := useMockClock(t, a)
	startTestStream(t, a, server)
	a.SimulateExitForTest()
	wait, stopWait := testutil.NewTimerTraps(t, clock, mimoStreamStopTimerTag)

	stopped := make(chan struct{})
	go func() {
		a.Stop()
		close(stopped)
	}()
	testutil.WaitForTimer(t, ctx, wait)
	stopWait.MustWait(ctx).MustRelease(ctx)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("Stop did not return when the stream ended")
	}
	_, pending := clock.Peek()
	assert.False(t, pending)
}

// A stop with no turn persists nothing.
func TestStopWithoutATurnPersistsNothing(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.manualCompactionID = "prt_old"
	a.manualCompactionReady = true
	a.manualFollowupSending = true
	a.manualFollowupBusy = true
	a.sessionSwitching = true
	a.SimulateExitForTest()

	a.Stop()
	assert.Empty(t, sink.Messages())
	assert.Empty(t, sink.ChildAgentIDs(), "a flush opens no transcript")
	assert.Empty(t, a.manualCompactionID)
	assert.False(t, a.manualCompactionReady)
	assert.False(t, a.manualFollowupSending)
	assert.False(t, a.manualFollowupBusy)
	assert.False(t, a.sessionSwitching)
}

func TestStopWithPendingCompactionStartPublishesInactive(t *testing.T) {
	t.Parallel()
	a, sink, _ := newSinkTestAgent(t)
	a.compactionAck = make(chan struct{})
	a.SimulateExitForTest()

	a.Stop()
	assert.Nil(t, a.compactionAck)
	active, published := sink.LastTurnActive()
	assert.True(t, published)
	assert.False(t, active, "the stopped process owns no pending compaction turn")
}
