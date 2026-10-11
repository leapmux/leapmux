package providerkit

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type observerRequestWriter func([]byte) (int, error)

func (write observerRequestWriter) Write(data []byte) (int, error) { return write(data) }

func newObserverTestProcess(t *testing.T, writer io.Writer) *JSONRPCProcess {
	t.Helper()
	done := make(chan struct{})
	connection := &JSONRPCProcess{Process: NewProcessFrom(ProcessConfig{
		Ctx: t.Context(), Cancel: func() {}, ProcessDone: done, Stdin: agenttest.NopStdin(writer),
	})}
	t.Cleanup(func() { close(done); connection.Stop() })
	return connection
}

func TestJSONRPCObservesDecodedRepliesBeforeTheReaderContinues(t *testing.T) {
	t.Parallel()
	for _, payload := range []string{
		`{"result":0}`, `{"result":false}`, `{"result":""}`, `{"result":null}`,
		`{"result":{"id":9007199254740993}}`,
		`{"error":{"code":-32600,"message":"Native refusal"}}`,
		`{"result":{},"error":{"code":-32600,"message":"Both fields"}}`, `{}`,
	} {
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			var observed atomic.Int32
			var observedResult json.RawMessage
			var observedError error
			var connection *JSONRPCProcess
			connection = newObserverTestProcess(t, observerRequestWriter(func(data []byte) (int, error) {
				var request jsonrpcMessage
				if err := json.Unmarshal(data, &request); err != nil {
					return 0, err
				}
				raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,` + payload[1:])
				if payload == `{}` {
					raw = json.RawMessage(`{"jsonrpc":"2.0","id":1}`)
				}
				assert.True(t, connection.Deliver(request.ID, raw))
				assert.Equal(t, int32(1), observed.Load(), "the reader sees the observation before its next frame")
				return len(data), nil
			}))
			result, err := connection.SendRequestObserved("session/set_mode", json.RawMessage(`{}`), time.Minute, func(result json.RawMessage, err error) {
				observedResult, observedError = result, err
				observed.Add(1)
			})
			assert.Equal(t, observedResult, result)
			if observedError == nil {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, observedError.Error())
			}
			assert.Equal(t, int32(1), observed.Load())
		})
	}
}

func TestJSONRPCWriteFailureDoesNotObserveAReply(t *testing.T) {
	t.Parallel()
	cause := errors.New("the pipe closed")
	connection := newObserverTestProcess(t, observerRequestWriter(func([]byte) (int, error) { return 0, cause }))
	var observed atomic.Int32
	_, err := connection.SendRequestObserved("test", nil, time.Minute, func(json.RawMessage, error) { observed.Add(1) })
	require.ErrorIs(t, err, cause)
	assert.Zero(t, observed.Load())
	assert.False(t, connection.Deliver(1, json.RawMessage(`{"result":true}`)))
}

func TestJSONRPCCancellationRemovesTheReplyObserver(t *testing.T) {
	t.Parallel()
	written := make(chan struct{}, 1)
	connection := newObserverTestProcess(t, observerRequestWriter(func(data []byte) (int, error) { written <- struct{}{}; return len(data), nil }))
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	connection.ctx = ctx
	var observed atomic.Int32
	finished := make(chan error, 1)
	go func() {
		_, err := connection.SendRequestObserved("test", nil, 0, func(json.RawMessage, error) { observed.Add(1) })
		finished <- err
	}()
	select {
	case <-written:
	case <-t.Context().Done():
		t.Fatal("the request did not reach the writer")
	}
	cancel()
	require.ErrorIs(t, <-finished, context.Canceled)
	assert.False(t, connection.Deliver(1, json.RawMessage(`{"result":true}`)))
	assert.Zero(t, observed.Load())
}

func TestJSONRPCTimeoutRemovesTheReplyObserver(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	ctx := testutil.DeadlineContext(t)
	trap := clock.Trap().NewTimer(AwaitResponseTimerTag, "test")
	defer trap.Close()
	connection := newObserverTestProcess(t, io.Discard)
	connection.clock = clock
	var observed atomic.Int32
	finished := make(chan error, 1)
	go func() {
		_, err := connection.SendRequestObserved("test", nil, time.Minute, func(json.RawMessage, error) { observed.Add(1) })
		finished <- err
	}()
	trap.MustWait(ctx).MustRelease(ctx)
	clock.Advance(time.Minute).MustWait(ctx)
	require.EqualError(t, <-finished, "timeout waiting for test response")
	assert.False(t, connection.Deliver(1, json.RawMessage(`{"result":true}`)))
	assert.Zero(t, observed.Load())
}
