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

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
