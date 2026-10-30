package zcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestZCodeStop_ACancelledApprovalEndsTheTurnAsInterrupted(t *testing.T) {
	t.Parallel()
	assertZCodeApprovalStopCompletion(t, true, "Permission request cancelled", agent.MessageCompletionInterrupted)
}

func TestZCodeStop_ASuccessWithoutTheCancelReasonStaysEnded(t *testing.T) {
	t.Parallel()
	assertZCodeApprovalStopCompletion(t, true, "Plan mode", agent.MessageCompletionComplete)
}

func TestZCodeStop_ACancellationWithoutAStopDoesNotInterruptTheTurn(t *testing.T) {
	t.Parallel()
	assertZCodeApprovalStopCompletion(t, false, "Permission request cancelled", agent.MessageCompletionComplete)
}

// The read loop hands the stop reply to the waiting stop and goes on to the next
// frames at once, so ZCode's report of the cancelled approval and of the turn end can
// be handled before the stop arms its window. Delivering both before the reply puts
// the agent in that state every time. The stop still ended the turn.
func TestZCodeStop_ACancelledApprovalBeforeTheStopReplyEndsTheTurnAsInterrupted(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw=%v", raw), func(t *testing.T) {
			t.Parallel()
			a, sink, stdin := startZCodeApprovalTurn(t)
			done := make(chan error, 1)
			go func() {
				if raw {
					done <- a.SendRawInput([]byte(`{"id":31,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
				} else {
					done <- a.Interrupt(agent.StopContext{})
				}
			}()
			req := waitZCodeRequest(t, stdin, MethodSessionStop)
			endZCodeApprovalTurn(t, a, zcodePermissionCancelledReason)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, req), nil))
			require.NoError(t, <-done)

			assertZCodeApprovalTurnCompletion(t, sink, agent.MessageCompletionInterrupted)
			assert.False(t, a.stopWindow.armed(), "the turn already ended, so no window watches it")
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Zero(t, a.stopsInFlight, "the answered stop is no longer in flight")
		})
	}
}

// A stop that ZCode refused stopped nothing, so a later cancellation is not its work.
func TestZCodeStop_ARefusedStopLeavesNoStopInFlight(t *testing.T) {
	t.Parallel()
	a, sink, stdin := startZCodeApprovalTurn(t)
	refuseZCodeRequest(t, a, stdin, MethodSessionStop, -32603, "the stop failed")
	require.Error(t, a.Interrupt(agent.StopContext{}))
	endZCodeApprovalTurn(t, a, zcodePermissionCancelledReason)
	assertZCodeApprovalTurnCompletion(t, sink, agent.MessageCompletionComplete)
	a.Mu.Lock()
	defer a.Mu.Unlock()
	assert.Zero(t, a.stopsInFlight)
}

func assertZCodeApprovalStopCompletion(t *testing.T, stop bool, reason string, completion agent.MessageCompletion) {
	t.Helper()
	a, sink, stdin := startZCodeApprovalTurn(t)
	if stop {
		answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
		require.NoError(t, a.Interrupt(agent.StopContext{}))
	}
	// Recorded order: run6, stop reply, cancelled permission, then a success end.
	endZCodeApprovalTurn(t, a, reason)
	assertZCodeApprovalTurnCompletion(t, sink, completion)
}

// startZCodeApprovalTurn starts a turn that streamed some text and waits for a
// permission answer.
func startZCodeApprovalTurn(t *testing.T) (*Agent, *agenttest.Sink, *zcodeRecordedStdin) {
	t.Helper()
	sink := &agenttest.Sink{}
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(sink), stdin)
	a.afterFunc = (&zcodeCapturedTimer{}).afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	a.HandleOutput(zcodeEventLine(t, 1, contracts.ZCodeEventTurnStarted, `{}`))
	a.HandleOutput(zcodeEventLine(t, 2, contracts.ZCodeEventModelStreaming,
		`{"kind":"text_delta","assistantMessageId":"m1","delta":"partial answer"}`))
	a.HandleOutput(zcodeRequestLine(t, 7, contracts.ZCodeMethodRequestPermission, zcodePermissionParams))
	return a, sink, stdin
}

// endZCodeApprovalTurn sends what ZCode sends when the permission request ends: a
// denial for the reason, then a success end of the turn.
func endZCodeApprovalTurn(t *testing.T, a *Agent, reason string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"requestId": "req-1", "decision": "deny", "reason": reason})
	require.NoError(t, err)
	a.HandleOutput(zcodeEventLine(t, 3, contracts.ZCodeEventPermissionResolved, string(payload)))
	a.HandleOutput(zcodeEventLine(t, 4, contracts.ZCodeEventTurnCompleted, `{"resultType":"success"}`))
}

// assertZCodeApprovalTurnCompletion requires the streamed text and the turn end to
// end with the completion.
func assertZCodeApprovalTurnCompletion(t *testing.T, sink *agenttest.Sink, completion agent.MessageCompletion) {
	t.Helper()
	rows := sink.Messages()
	require.Len(t, rows, 2)
	_, text, textCompletion, ok := agenttest.DecodeAssembledMessage(rows[0].Content)
	require.True(t, ok)
	assert.Equal(t, "partial answer", text)
	assert.Equal(t, completion, textCompletion)
	assert.True(t, rows[1].TurnEnd)
	if completion == agent.MessageCompletionInterrupted {
		assert.Equal(t, completion, rows[1].Completion)
	} else {
		assert.Empty(t, rows[1].Completion)
	}
	assert.Contains(t, string(rows[1].Content), `"resultType":"success"`, "the native frame stays unchanged")
}

type zcodeStopInput struct {
	frames chan []byte
	fail   error
}

func (w *zcodeStopInput) Write(data []byte) (int, error) {
	if w.fail != nil {
		return 0, w.fail
	}
	w.frames <- bytes.Clone(data)
	return len(data), nil
}

func (*zcodeStopInput) Close() error { return nil }

func newZCodeStopAgent(t *testing.T) (*Agent, *zcodeStopInput) {
	t.Helper()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	w := &zcodeStopInput{frames: make(chan []byte, 8)}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "stop-test", Ctx: t.Context(), ProcessDone: make(chan struct{}), Stdin: w,
	})
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)
	t.Cleanup(a.cancelStoppedZCodeTurn)
	return a, w
}

func receiveZCodeStopFrame(t *testing.T, input *zcodeStopInput) []byte {
	t.Helper()
	select {
	case frame := <-input.frames:
		return frame
	case <-time.After(30 * time.Second):
		t.Fatal("the native stop did not reach stdin")
		return nil
	}
}

func replyZCodeStop(a *Agent, requestID int64, result string) {
	a.HandleOutput([]byte(fmt.Sprintf(`{"id":%d,"result":%s}`, requestID, result)))
}

func TestRawZCodeStopPreservesBytesAndSignedIntegerIDs(t *testing.T) {
	for _, requestID := range []int64{0, -1, math.MinInt64, math.MaxInt64} {
		t.Run(fmt.Sprint(requestID), func(t *testing.T) {
			t.Parallel()
			a, input := newZCodeStopAgent(t)
			raw := []byte(fmt.Sprintf(" \t{\"id\":%d,\"method\":\"session/stop\",\"params\":{\"sessionId\":\"sess-1\"},\"extra\":0} ", requestID))
			var ignored atomic.Int32
			done := make(chan error, 1)
			go func() { done <- a.SendRawInput(raw, agent.NewStopContext(func() { ignored.Add(1) })) }()
			assert.Equal(t, append(bytes.Clone(raw), '\n'), receiveZCodeStopFrame(t, input))
			replyZCodeStop(a, requestID, `{}`)
			require.NoError(t, <-done)
			a.Mu.Lock()
			a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
			a.Mu.Unlock()
			// The stopped turn speaking restarts the window and may prove the stop ignored.
			a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventModelStreaming})
			assert.Equal(t, int32(1), ignored.Load())
		})
	}
}

func TestRawZCodeStopRejectsInvalidIDsAndSessionsBeforeWriting(t *testing.T) {
	for _, raw := range []string{
		`{"method":"session/stop","params":{"sessionId":"sess-1"}}`,
		`{"id":null,"method":"session/stop","params":{"sessionId":"sess-1"}}`,
		`{"id":"1","method":"session/stop","params":{"sessionId":"sess-1"}}`,
		`{"id":1.5,"method":"session/stop","params":{"sessionId":"sess-1"}}`,
		`{"id":9223372036854775808,"method":"session/stop","params":{"sessionId":"sess-1"}}`,
		`{"id":1,"method":"session/stop","params":{"sessionId":"another-session"}}`,
		`{"id":1,"method":"session/stop"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			a, input := newZCodeStopAgent(t)
			require.Error(t, a.SendRawInput([]byte(raw), agent.StopContext{}))
			assert.Empty(t, input.frames)
			assert.False(t, a.stopWindow.armed())
		})
	}
}

func TestRawZCodeStopRejectsAnInFlightRequestID(t *testing.T) {
	t.Parallel()
	a, input := newZCodeStopAgent(t)
	channel, release, err := a.Register(int64(0))
	require.NoError(t, err)
	defer release()
	err = a.SendRawInput([]byte(`{"id":0,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
	require.ErrorContains(t, err, "pending reply")
	assert.Empty(t, input.frames)
	require.True(t, a.Deliver(0, json.RawMessage(`{"id":0,"result":{}}`)))
	assert.JSONEq(t, `{"id":0,"result":{}}`, string(<-channel))
}

func TestRawZCodeStopRetainsNativeRejectionAndWriteFailure(t *testing.T) {
	for _, failure := range []string{"native", "write"} {
		t.Run(failure, func(t *testing.T) {
			t.Parallel()
			a, input := newZCodeStopAgent(t)
			if failure == "write" {
				input.fail = errors.New("the native stdin rejected the write")
			}
			done := make(chan error, 1)
			go func() {
				done <- a.SendRawInput([]byte(`{"id":9,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
			}()
			if failure == "native" {
				receiveZCodeStopFrame(t, input)
				a.HandleOutput([]byte(`{"id":9,"error":{"code":77,"message":"the native turn refused the stop"}}`))
			}
			err := <-done
			require.Error(t, err)
			if failure == "native" {
				code, native := zcodeErrorCode(err)
				assert.True(t, native)
				assert.Equal(t, 77, code)
				assert.ErrorContains(t, err, "the native turn refused the stop")
			} else {
				assert.ErrorContains(t, err, "the native stdin rejected the write")
			}
			assert.False(t, a.stopWindow.armed())
		})
	}
}

func TestZCodeStopDoesNotOpenAWindowForAReplacementTurn(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			t.Parallel()
			a, input := newZCodeStopAgent(t)
			done := make(chan error, 1)
			go func() {
				if raw {
					done <- a.SendRawInput([]byte(`{"id":4,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
				} else {
					done <- a.Interrupt(agent.StopContext{})
				}
			}()
			frame := receiveZCodeStopFrame(t, input)
			var request struct {
				ID int64 `json:"id"`
			}
			require.NoError(t, json.Unmarshal(frame, &request))
			a.handleZCodeTurnStarted(zcodeEventEnvelope{})
			replyZCodeStop(a, request.ID, `{}`)
			require.NoError(t, <-done)
			assert.False(t, a.stopWindow.armed(), "the old reply cannot watch the replacement turn")
		})
	}
}

func TestZCodeStopReportsOnlyAcknowledgedAttempts(t *testing.T) {
	t.Parallel()
	a, input := newZCodeStopAgent(t)
	var first, second atomic.Int32
	firstDone := make(chan error, 1)
	go func() { firstDone <- a.Interrupt(agent.NewStopContext(func() { first.Add(1) })) }()
	frame := receiveZCodeStopFrame(t, input)
	var request struct {
		ID int64 `json:"id"`
	}
	require.NoError(t, json.Unmarshal(frame, &request))
	replyZCodeStop(a, request.ID, `{}`)
	require.NoError(t, <-firstDone)
	secondDone := make(chan error, 1)
	go func() { secondDone <- a.Interrupt(agent.NewStopContext(func() { second.Add(1) })) }()
	frame = receiveZCodeStopFrame(t, input)
	require.NoError(t, json.Unmarshal(frame, &request))
	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()
	a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventModelStreaming})
	assert.Equal(t, int32(1), first.Load())
	assert.Zero(t, second.Load(), "the second stop still waits for its native reply")
	replyZCodeStop(a, request.ID, `{}`)
	require.NoError(t, <-secondDone)
	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()
	a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventModelStreaming})
	a.refreshStoppedZCodeTurn(zcodeEventEnvelope{Type: contracts.ZCodeEventModelStreaming})
	assert.Equal(t, int32(1), first.Load())
	assert.Equal(t, int32(1), second.Load())
}

func TestRawZCodeStopRejectsAReplyWithoutAResultOrError(t *testing.T) {
	t.Parallel()
	a, input := newZCodeStopAgent(t)
	done := make(chan error, 1)
	go func() {
		done <- a.SendRawInput([]byte(`{"id":12,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
	}()
	receiveZCodeStopFrame(t, input)
	a.HandleOutput([]byte(`{"id":12}`))
	require.Error(t, <-done, "an incomplete reply does not confirm native stop acceptance")
	assert.False(t, a.stopWindow.armed())
}

// --- the guarded v4 stop ---

// zcodeStartExecTurn starts the foreground turn with the recorded execution id,
// as turn.started reports it for a real run.
func zcodeStartExecTurn(t *testing.T, a *Agent, turnID, executionID string) {
	t.Helper()
	a.HandleOutput(zcodeTurnEventLine(t, 0, turnID, contracts.ZCodeEventTurnStarted,
		fmt.Sprintf(`{"turnNumber":1,"input":"hi","foregroundExecutionId":%q}`, executionID)))
}

// zcodeStartBackgroundExecTurn starts a runtime turn that identifies the user
// execution it belongs to.
func zcodeStartBackgroundExecTurn(t *testing.T, a *Agent, turnID, executionID string) {
	t.Helper()
	a.HandleOutput(zcodeTurnEventLine(t, 0, turnID, contracts.ZCodeEventTurnStarted,
		fmt.Sprintf(`{"turnNumber":2,"inputSource":"goal_state_change","foregroundExecutionId":%q}`, executionID)))
}

// zcodeStopAck is the acknowledgement of an accepted stop.
func zcodeStopAck(command zcodeTestCommand) string {
	return fmt.Sprintf(`{"commandId":%q,"status":"accepted","revisionAtDecision":9}`, command.CommandID)
}

// waitZCodeStopCommand waits until stdin holds the agent's `stop` command and
// returns it. The command blocks its caller, so a test answers after this
// returns.
func waitZCodeStopCommand(t *testing.T, stdin *zcodeRecordedStdin) zcodeTestCommand {
	t.Helper()
	deadline := time.Now().Add(zcodeTestRPCTimeout)
	for time.Now().Before(deadline) {
		if commands := zcodeSentCommandsOf(t, stdin, zcodeCommandStop); len(commands) > 0 {
			return commands[0]
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("timed out waiting for the stop command")
	return zcodeTestCommand{}
}

// zcodeStopMethods returns the methods the agent sent, in order.
func zcodeStopMethods(t *testing.T, stdin *zcodeRecordedStdin) []string {
	t.Helper()
	var methods []string
	for _, req := range stdin.Requests(t) {
		if req.Method != "" {
			methods = append(methods, req.Method)
		}
	}
	return methods
}

// TestZCodeInterrupt_SendsAGuardedV4StopForTheUserTurn pins the exact `stop`
// command of a user turn. The payload carries the execution id the turn
// recorded: an unguarded v4 stop aborts whatever execution is current, which
// can be a NEWER one than the user stopped.
func TestZCodeInterrupt_SendsAGuardedV4StopForTheUserTurn(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartExecTurn(t, a, "turn-1", "exec-u")

	issuedAfter := a.Clock().Now().UnixMilli()
	done := make(chan error, 1)
	go func() { done <- a.Interrupt(agent.StopContext{}) }()
	command := waitZCodeStopCommand(t, stdin)
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(zcodeStopAck(command))))
	require.NoError(t, <-done)

	assert.Equal(t, MethodCommand, stdin.Requests(t)[0].Method)
	assert.Equal(t, zcodeCommandStop, command.Type)
	assert.Equal(t, zcodeCommandClientID, command.ClientID)
	assert.Equal(t, "sess-1", command.SessionID)
	assert.Nil(t, command.BaseRevision, "a stop is not a compare-and-set command")
	assert.True(t, strings.HasPrefix(command.CommandID, zcodeStopCommandPrefix), command.CommandID)
	assert.GreaterOrEqual(t, command.IssuedAt, issuedAfter, "issuedAt is the real clock value")
	assert.JSONEq(t, `{"expectedForegroundExecutionId":"exec-u"}`, string(command.Payload))
	assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin),
		"the guarded stop alone ends the request")
	assert.True(t, a.stopWindow.armed(), "an accepted stop arms the silence window")
	assert.Equal(t, 1, timer.armed)
}

// TestZCodeInterrupt_SendsAGuardedV4StopForTheLatestBackgroundTurn pins the
// payload when no user turn runs and the stop targets the latest background
// turn. Its recorded execution id is the guard, as for the user's own turn.
func TestZCodeInterrupt_SendsAGuardedV4StopForTheLatestBackgroundTurn(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartBackgroundExecTurn(t, a, "turn-bg", "exec-b")

	done := make(chan error, 1)
	go func() { done <- a.Interrupt(agent.StopContext{}) }()
	command := waitZCodeStopCommand(t, stdin)
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(zcodeStopAck(command))))
	require.NoError(t, <-done)

	assert.JSONEq(t, `{"expectedForegroundExecutionId":"exec-b"}`, string(command.Payload))
	assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin))
	assert.True(t, a.stopWindow.armed())
	assert.Equal(t, 1, timer.armed)
}

// TestZCodeInterrupt_UnknownExecutionIDUsesTheLegacyStop: an owner that
// recorded no execution id takes session/stop and sends NO v4 command at all --
// the v4 stop without its guard aborts whatever execution is current.
func TestZCodeInterrupt_UnknownExecutionIDUsesTheLegacyStop(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartTestTurn(t, a, `{"turnNumber":1,"input":"hi"}`)

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	assert.Equal(t, []string{MethodSessionStop}, zcodeStopMethods(t, stdin),
		"an unknown execution id selects the legacy stop and no unguarded v4 command")
	assert.True(t, a.stopWindow.armed())
	assert.Equal(t, 1, timer.armed)
}

// TestZCodeInterrupt_AcceptedAndDuplicateAcksArmOneWindow: both acknowledgements
// state that the stop was taken, so each arms exactly one silence window and
// neither needs a legacy follow-up.
func TestZCodeInterrupt_AcceptedAndDuplicateAcksArmOneWindow(t *testing.T) {
	for _, status := range []string{zcodeCommandAccepted, zcodeCommandDuplicate} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			timer := &zcodeCapturedTimer{}
			a.afterFunc = timer.afterFunc
			t.Cleanup(a.cancelStoppedZCodeTurn)
			zcodeStartExecTurn(t, a, "turn-1", "exec-u")

			done := make(chan error, 1)
			go func() { done <- a.Interrupt(agent.StopContext{}) }()
			command := waitZCodeStopCommand(t, stdin)
			a.HandleOutput(zcodeReplyLine(t, command.ID,
				json.RawMessage(fmt.Sprintf(`{"commandId":%q,"status":%q,"revisionAtDecision":9}`, command.CommandID, status))))
			require.NoError(t, <-done)

			assert.True(t, a.stopWindow.armed())
			assert.Equal(t, 1, timer.armed, "one accepted stop arms one window")
			assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin),
				"an accepted stop sends no legacy follow-up")
		})
	}
}

// TestZCodeInterrupt_TargetChangedNoopSendsNoLegacyStop: the guarded stop
// answered `noop` with guard.stopTargetChanged states that the intended
// execution no longer exists. Nothing is left to stop, so no window may arm and
// no unguarded legacy stop may abort a newer execution.
func TestZCodeInterrupt_TargetChangedNoopSendsNoLegacyStop(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartExecTurn(t, a, "turn-1", "exec-u")

	done := make(chan error, 1)
	go func() { done <- a.Interrupt(agent.StopContext{}) }()
	command := waitZCodeStopCommand(t, stdin)
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(
		fmt.Sprintf(`{"commandId":%q,"status":%q,"reasonCode":%q,"revisionAtDecision":9}`,
			command.CommandID, zcodeCommandNoop, zcodeCommandGuardStopTargetChanged))))
	require.NoError(t, <-done)

	assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin),
		"a target that changed stops nothing, so no legacy stop may follow")
	assert.False(t, a.stopWindow.armed())
	assert.Zero(t, timer.armed)
}

// TestZCodeInterrupt_MethodNotFoundFallsBackToTheLegacyStop: an app-server with
// no command inbox still implements session/stop.
func TestZCodeInterrupt_MethodNotFoundFallsBackToTheLegacyStop(t *testing.T) {
	t.Parallel()
	stdin := &zcodeRecordedStdin{}
	a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartExecTurn(t, a, "turn-1", "exec-u")

	answerZCodeRequest(t, a, stdin, MethodSessionStop, `{}`)
	refuseZCodeRequest(t, a, stdin, MethodCommand, ErrMethodNotFound, "Method not found")
	require.NoError(t, a.Interrupt(agent.StopContext{}))

	assert.Equal(t, []string{MethodCommand, MethodSessionStop}, zcodeStopMethods(t, stdin),
		"a refused command inbox selects the legacy stop")
	assert.True(t, a.stopWindow.armed())
	assert.Equal(t, 1, timer.armed)
}

// TestZCodeInterrupt_OtherV4RefusalsDoNotFallThrough: every refusal that does
// not state "this inbox cannot stop" returns the error and sends no second
// stop. A fall-through stop could abort work the user never stopped.
func TestZCodeInterrupt_OtherV4RefusalsDoNotFallThrough(t *testing.T) {
	for _, tc := range []struct {
		name string
		ack  func(zcodeTestCommand) string
	}{
		{"an app-server error", nil},
		{"a failed acknowledgement", func(command zcodeTestCommand) string {
			return fmt.Sprintf(`{"commandId":%q,"status":"failed","reasonCode":"fault.command.executionFailed","revisionAtDecision":9}`, command.CommandID)
		}},
		{"a rejected acknowledgement", func(command zcodeTestCommand) string {
			return fmt.Sprintf(`{"commandId":%q,"status":"rejected","reasonCode":"busy","revisionAtDecision":9}`, command.CommandID)
		}},
		{"a noop of another guard", func(command zcodeTestCommand) string {
			return fmt.Sprintf(`{"commandId":%q,"status":"noop","reasonCode":"guard.other","revisionAtDecision":9}`, command.CommandID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			timer := &zcodeCapturedTimer{}
			a.afterFunc = timer.afterFunc
			t.Cleanup(a.cancelStoppedZCodeTurn)
			zcodeStartExecTurn(t, a, "turn-1", "exec-u")

			done := make(chan error, 1)
			go func() { done <- a.Interrupt(agent.StopContext{}) }()
			if tc.ack == nil {
				refuseZCodeRequest(t, a, stdin, MethodCommand, -32603, "the stop failed")
			} else {
				answerZCodeCommand(t, a, stdin, zcodeCommandStop, 1, tc.ack)
			}
			require.Error(t, <-done)

			assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin),
				"no unguarded stop may follow a refusal")
			assert.False(t, a.stopWindow.armed())
			assert.Zero(t, timer.armed)
			a.Mu.Lock()
			defer a.Mu.Unlock()
			assert.Zero(t, a.stopsInFlight)
		})
	}
}

// zcodePausedStdin delays the first write until the test releases it, so a test
// can move the world between Interrupt's capture of the stop target and the
// request that identifies it.
type zcodePausedStdin struct {
	zcodeRecordedStdin
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
}

func (w *zcodePausedStdin) Write(p []byte) (int, error) {
	w.once.Do(func() {
		w.entered <- struct{}{}
		<-w.gate
	})
	return w.zcodeRecordedStdin.Write(p)
}

// TestZCodeInterrupt_ATargetChangeCannotProduceAnUnguardedStop: the stop target
// can change between Interrupt's capture and the request that identifies it. The
// request keeps the guard of the captured execution -- never an unguarded stop
// that aborts the NEWER execution -- and a late acknowledgement opens no window
// for the newer generation.
func TestZCodeInterrupt_ATargetChangeCannotProduceAnUnguardedStop(t *testing.T) {
	t.Parallel()
	a := newZCodeTestAgent(t, agent.NewProviderServices(&agenttest.Sink{}))
	paused := &zcodePausedStdin{entered: make(chan struct{}, 1), gate: make(chan struct{})}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "stop-test", Ctx: t.Context(), ProcessDone: make(chan struct{}), Stdin: paused,
	})
	timer := &zcodeCapturedTimer{}
	a.afterFunc = timer.afterFunc
	t.Cleanup(a.cancelStoppedZCodeTurn)
	zcodeStartExecTurn(t, a, "turn-1", "exec-1")

	done := make(chan error, 1)
	go func() { done <- a.Interrupt(agent.StopContext{}) }()
	select {
	case <-paused.entered:
	case <-time.After(30 * time.Second):
		t.Fatal("the guarded stop did not reach stdin")
	}
	// A new user turn takes the foreground while the stop request waits to be
	// written.
	a.HandleOutput(zcodeTurnEventLine(t, 1, "turn-2", contracts.ZCodeEventTurnStarted,
		`{"turnNumber":2,"input":"next","foregroundExecutionId":"exec-2"}`))
	close(paused.gate)

	stdin := &paused.zcodeRecordedStdin
	command := waitZCodeStopCommand(t, stdin)
	assert.JSONEq(t, `{"expectedForegroundExecutionId":"exec-1"}`, string(command.Payload),
		"the request keeps the captured execution's guard through the target change")
	a.HandleOutput(zcodeReplyLine(t, command.ID, json.RawMessage(zcodeStopAck(command))))
	require.NoError(t, <-done)

	assert.Equal(t, []string{MethodCommand}, zcodeStopMethods(t, stdin),
		"the change produces no second, unguarded stop")
	assert.False(t, a.stopWindow.armed(),
		"a late acknowledgement cannot open a window for the newer generation")
	assert.Zero(t, timer.armed)
}

func TestZCodeStop_ReceiptTracksAProvisionalOwnerThroughNativeAdoption(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw %t", raw), func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			a.afterFunc = (&zcodeCapturedTimer{}).afterFunc
			a.createProvisionalZCodeTurn("sess-1", "input", 1)
			done := make(chan error, 1)
			go func() {
				if raw {
					done <- a.SendRawInput([]byte(`{"id":-31,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
				} else {
					done <- a.Interrupt(agent.StopContext{})
				}
			}()
			request := waitZCodeRequest(t, stdin, MethodSessionStop)
			a.HandleOutput(zcodeTurnEventLine(t, 1, "native", contracts.ZCodeEventTurnStarted, `{"inputId":"input","turnNumber":1}`))
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
			require.NoError(t, <-done)
			a.Mu.Lock()
			assert.True(t, a.stopWindow.armed())
			assert.Equal(t, "native", a.stopWindow.turnID)
			a.Mu.Unlock()
		})
	}
}

func TestZCodeStop_OldCompactionReceiptCannotStopItsSuccessor(t *testing.T) {
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprintf("raw %t", raw), func(t *testing.T) {
			t.Parallel()
			stdin := &zcodeRecordedStdin{}
			a := newZCodeTestAgentWithStdin(t, agent.NewProviderServices(&agenttest.Sink{}), stdin)
			a.afterFunc = (&zcodeCapturedTimer{}).afterFunc
			compactA := make(chan error, 1)
			go func() { compactA <- a.CompactContext() }()
			first := waitZCodeRequest(t, stdin, MethodSessionCompact)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, first), json.RawMessage(`{"compact":{"state":"accepted"}}`)))
			require.NoError(t, <-compactA)
			stop := make(chan error, 1)
			go func() {
				if raw {
					stop <- a.SendRawInput([]byte(`{"id":-31,"method":"session/stop","params":{"sessionId":"sess-1"}}`), agent.StopContext{})
				} else {
					stop <- a.Interrupt(agent.StopContext{})
				}
			}()
			request := waitZCodeRequest(t, stdin, MethodSessionStop)
			a.finishZCodeCompaction("sess-1")
			next := make(chan error, 1)
			go func() { next <- a.CompactContext() }()
			require.Eventually(t, func() bool { return len(stdin.RequestsFor(MethodSessionCompact)) == 2 }, 30*time.Second, time.Millisecond)
			second := stdin.RequestsFor(MethodSessionCompact)[1]
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, second), json.RawMessage(`{"compact":{"state":"accepted"}}`)))
			require.NoError(t, <-next)
			a.HandleOutput(zcodeReplyLine(t, zcodeSentRequestID(t, request), json.RawMessage(`{}`)))
			require.NoError(t, <-stop)
			a.Mu.Lock()
			assert.False(t, a.stopWindow.armed(), "an old stop cannot acquire the successor compaction")
			assert.Equal(t, "sess-1", a.compactionSessionID)
			a.Mu.Unlock()
		})
	}
}
