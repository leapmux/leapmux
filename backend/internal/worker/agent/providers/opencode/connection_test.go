package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func joinOpenCodeStream(t *testing.T, stream *openCodeStream) {
	t.Helper()
	stream.stop()
	deadline, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case <-stream.done:
		stream.wait()
	case <-deadline.Done():
		t.Error("the OpenCode event stream did not stop")
	}
}

func TestOpenCodeStreamRestoresBeforeDispatchingBufferedEvents(t *testing.T) {
	t.Parallel()
	for _, requested := range []bool{false, true} {
		t.Run(fmt.Sprintf("requested=%t", requested), func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			clock := testutil.NewQuartzMock(t)
			reconnect := clock.Trap().NewTimer(openCodeReconnectTimerTag)
			defer reconnect.Close()
			dropFirst := make(chan struct{})
			restoreEntered := make(chan struct{})
			restoreRelease := make(chan struct{})
			dispatched := make(chan string, 1)
			var connections atomic.Int64
			native := `{ "type": "session.text.delta", "data": {"sessionID":"ses_owner","delta":"after reconnect"} }`
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/event", r.URL.Path)
				assert.Equal(t, "text/event-stream", r.Header.Get("Accept"))
				n := connections.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\n")
				if n == 2 {
					_, _ = fmt.Fprintf(w, "data: %s\n\n", native)
				}
				w.(http.Flusher).Flush()
				if n == 1 {
					select {
					case <-dropFirst:
					case <-r.Context().Done():
					}
				} else {
					<-r.Context().Done()
				}
			})
			stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{
				rpc: rpc, clock: clock, agentID: "agent",
				restoreSessions: func(ctx context.Context) error {
					close(restoreEntered)
					select {
					case <-restoreRelease:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				},
				dispatch: func(_ context.Context, raw json.RawMessage) error { dispatched <- string(raw); return nil },
			})
			require.NoError(t, err)
			t.Cleanup(func() { joinOpenCodeStream(t, stream) })
			require.NoError(t, stream.awaitConnected(ctx, 30*time.Second))
			if requested {
				stream.requestRecovery()
			} else {
				close(dropFirst)
			}
			clock.Advance(testutil.WaitForTimer(t, ctx, reconnect)).MustWait(ctx)
			select {
			case <-restoreEntered:
			case <-ctx.Done():
				t.Fatal("the reconnect did not restore sessions")
			}
			select {
			case <-dispatched:
				t.Fatal("a live event passed the restoration barrier")
			default:
			}
			close(restoreRelease)
			select {
			case raw := <-dispatched:
				assert.Equal(t, native, raw)
			case <-ctx.Done():
				t.Fatal("the buffered event did not arrive")
			}
			assert.EqualValues(t, 2, connections.Load())
		})
	}
}

func TestOpenCodeStreamReconnectDelayIncreasesToItsMaximum(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reconnect := clock.Trap().NewTimer(openCodeReconnectTimerTag)
	defer reconnect.Close()
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\n")
	})
	stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{
		rpc: rpc, clock: clock, dispatch: func(context.Context, json.RawMessage) error { return nil },
	})
	require.NoError(t, err)
	t.Cleanup(func() { joinOpenCodeStream(t, stream) })
	for _, delay := range []time.Duration{100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond, 800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond, 5 * time.Second, 5 * time.Second} {
		require.Equal(t, delay, testutil.WaitForTimer(t, ctx, reconnect))
		clock.Advance(delay).MustWait(ctx)
	}
}

func TestOpenCodeStreamResetsDelayAfterAStableConnection(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	clock := testutil.NewQuartzMock(t)
	reconnect := clock.Trap().NewTimer(openCodeReconnectTimerTag)
	defer reconnect.Close()
	stableEntered := make(chan struct{})
	dropStable := make(chan struct{})
	var connections atomic.Int64
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		n := connections.Add(1)
		_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\n")
		w.(http.Flusher).Flush()
		if n == 3 {
			close(stableEntered)
			select {
			case <-dropStable:
			case <-r.Context().Done():
			}
		}
	})
	stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{rpc: rpc, clock: clock, dispatch: func(context.Context, json.RawMessage) error { return nil }})
	require.NoError(t, err)
	t.Cleanup(func() { joinOpenCodeStream(t, stream) })
	for _, delay := range []time.Duration{openCodeReconnectFirst, 2 * openCodeReconnectFirst} {
		require.Equal(t, delay, testutil.WaitForTimer(t, ctx, reconnect))
		clock.Advance(delay).MustWait(ctx)
	}
	select {
	case <-stableEntered:
	case <-ctx.Done():
		t.Fatal("the stable connection did not open")
	}
	clock.Advance(openCodeStableConnection).MustWait(ctx)
	close(dropStable)
	assert.Equal(t, openCodeReconnectFirst, testutil.WaitForTimer(t, ctx, reconnect))
}

func TestOpenCodeStreamPreservesPermanentHTTPFailure(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{rpc: rpc, clock: testutil.NewQuartzMock(t), dispatch: func(context.Context, json.RawMessage) error { return nil }})
	require.NoError(t, err)
	t.Cleanup(func() { joinOpenCodeStream(t, stream) })
	err = stream.awaitConnected(ctx, 30*time.Second)
	var refused *providerkit.HTTPStatusError
	require.ErrorAs(t, err, &refused)
	assert.Equal(t, http.StatusUnauthorized, refused.StatusCode)
}

func TestOpenCodeStreamRejectsMalformedOrUngreetedEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ body, reason string }{
		{"data: {\n\n", "decode an OpenCode event"},
		{"data: {\"type\":\"session.text.delta\"}\n\n", "before its stream greeting"},
		{"data: {\"type\":\"server.connected\"}\n\ndata: {\"type\":\"server.connected\"}\n\n", "repeated"},
		{"data: {\"type\":\"server.connected\"}\n\ndata: {}\n\n", "no type"},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, tc.body) })
			stream := &openCodeStream{rpc: rpc, connected: make(chan struct{}), dispatch: func(context.Context, json.RawMessage) error {
				t.Error("an invalid frame reached the dispatcher")
				return nil
			}}
			require.ErrorContains(t, stream.consume(testutil.DeadlineContext(t)), tc.reason)
		})
	}
}

func TestOpenCodeStreamPreservesDispatchFailure(t *testing.T) {
	t.Parallel()
	failure := errors.New("the transcript write failed")
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\ndata: {\"type\":\"session.text.ended\"}\n\n")
	})
	stream := &openCodeStream{rpc: rpc, connected: make(chan struct{}), dispatch: func(context.Context, json.RawMessage) error { return failure }}
	assert.ErrorIs(t, stream.consume(testutil.DeadlineContext(t)), failure)
}

func TestOpenCodeStreamRequiresItsDependencies(t *testing.T) {
	t.Parallel()
	stream, err := startOpenCodeStream(t.Context(), openCodeStreamConfig{})
	require.Error(t, err)
	assert.Nil(t, stream)
}

func TestOpenCodeStreamWaitJoinsAnInFlightDispatch(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	rpc := openCodeTestRPC(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"server.connected\"}\n\ndata: {\"type\":\"session.text.ended\"}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	stream, err := startOpenCodeStream(ctx, openCodeStreamConfig{rpc: rpc, clock: testutil.NewQuartzMock(t), dispatch: func(context.Context, json.RawMessage) error {
		close(entered)
		<-release
		return nil
	}})
	require.NoError(t, err)
	t.Cleanup(func() { joinOpenCodeStream(t, stream) })
	t.Cleanup(func() { close(release) })
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the dispatch did not start")
	}
	stream.stop()
	select {
	case <-stream.done:
		t.Fatal("the stream ended while its dispatcher still ran")
	default:
	}
}
