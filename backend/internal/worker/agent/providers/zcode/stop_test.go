package zcode

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	a.turnActive = true
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
			a.refreshStoppedZCodeTurn()
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
	a.refreshStoppedZCodeTurn()
	assert.Equal(t, int32(1), first.Load())
	assert.Zero(t, second.Load(), "the second stop still waits for its native reply")
	replyZCodeStop(a, request.ID, `{}`)
	require.NoError(t, <-secondDone)
	a.Mu.Lock()
	a.stopWindow.armedAt = time.Now().Add(-zcodeStopIgnoredGrace - time.Second)
	a.Mu.Unlock()
	a.refreshStoppedZCodeTurn()
	a.refreshStoppedZCodeTurn()
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
