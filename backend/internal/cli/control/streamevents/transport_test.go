package streamevents

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/locallisten/locallistentest"
	"github.com/leapmux/leapmux/tunnel"
	"google.golang.org/protobuf/proto"
)

// capturingHandler is a slog.Handler that records emitted records so a test can
// assert a malformed frame was surfaced (and at what level).
type capturingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capturingHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capturingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}
func (h *capturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capturingHandler) WithGroup(string) slog.Handler      { return h }

// snapshot returns a copy of the records, so a test reads them without a race
// against a transport goroutine that still logs.
func (h *capturingHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]slog.Record(nil), h.records...)
}

// recordAttrs flattens the attributes of r into a map from key to value.
func recordAttrs(r slog.Record) map[string]any {
	out := make(map[string]any)
	r.Attrs(func(a slog.Attr) bool {
		out[a.Key] = a.Value.Resolve().Any()
		return true
	})
	return out
}

// fakeChannel is a minimal channelLike that captures the stream callback so a
// test can invoke it deterministically (standing in for the channel demux
// goroutine), and exposes a cancellable Context().
type fakeChannel struct {
	cb           func(*leapmuxv1.InnerStreamMessage)
	streamOnSend *leapmuxv1.InnerStreamMessage
	// method is the procedure that the opening SendRPCNoWait named.
	method string
	// respCh receives the response handler, so a test can deliver the terminal
	// error envelope a server sends instead of a stream frame.
	respCh           chan<- *leapmuxv1.InnerRpcResponse
	ctx              context.Context
	streamRequests   []streamRequestRecord
	cancelledStreams []uint64
}

type streamRequestRecord struct {
	reqID   uint64
	payload []byte
	cancel  bool
}

func (f *fakeChannel) SendRPCNoWait(_ context.Context, method string, _ []byte, handlers tunnel.RPCHandlers) (uint64, error) {
	f.method = method
	f.cb = handlers.Stream
	f.respCh = handlers.Response
	if f.streamOnSend != nil {
		f.cb(f.streamOnSend)
	}
	return 1, nil
}
func (f *fakeChannel) SendStreamRequest(_ context.Context, reqID uint64, payload []byte, cancel bool) error {
	f.streamRequests = append(f.streamRequests, streamRequestRecord{reqID: reqID, payload: payload, cancel: cancel})
	return nil
}
func (f *fakeChannel) CancelStream(_ context.Context, reqID uint64) error {
	f.cancelledStreams = append(f.cancelledStreams, reqID)
	return nil
}
func (f *fakeChannel) UnregisterStream(_ uint64)  {}
func (f *fakeChannel) UnregisterPending(_ uint64) {}
func (f *fakeChannel) Context() context.Context   { return f.ctx }

func TestChannelTransportRegistersStreamBeforeSendingRequest(t *testing.T) {
	fc := &fakeChannel{
		ctx:          context.Background(),
		streamOnSend: &leapmuxv1.InnerStreamMessage{},
	}
	transport := NewChannelTransport(fc, nil)
	frames := 0
	handle, err := transport.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	assert.Equal(t, 1, frames, "a stream frame sent with the request response must not race callback registration")
	handle.Cancel()
	<-handle.Done()
}

// TestChannelTransport_DropsFramesAfterTeardown asserts the cb guard: a frame the
// channel demux delivers AFTER teardown (Done() closed) must not reach onFrame,
// so a late frame can't run consumer logic (e.g. resetting reconnect backoff) for
// a session that already ended.
func TestChannelTransport_DropsFramesAfterTeardown(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)

	frames := 0
	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// A frame BEFORE teardown is delivered (empty payload unmarshals to a default
	// WatchEventsResponse).
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames)

	// Tear down and wait for Done() (the goroutine sets `closed` before closing done).
	handle.Cancel()
	<-handle.Done()

	// A late frame the demux still had in flight must be dropped, not delivered.
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames, "onFrame must not run after Done()")
}

// TestChannelTransport_TeardownNotBlockedByInFlightFrame pins the deadlock fix:
// the transport must NOT hold its frame mutex across onFrame. onFrame chains into
// the consumer's synchronous stdout encode, so a back-pressured `--follow` reader
// blocks it; holding the mutex across that call would wedge the teardown goroutine
// (which needs the mutex to set `closed`/cancel), and Done() would never close.
// With the mutex held only across the `closed` check, teardown completes promptly
// even while a frame is stuck in onFrame.
func TestChannelTransport_TeardownNotBlockedByInFlightFrame(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)

	entered := make(chan struct{})
	release := make(chan struct{})
	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		close(entered)
		<-release // simulate a back-pressured stdout encode that blocks
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// Deliver a frame on a separate goroutine; its onFrame blocks inside the cb,
	// standing in for the channel demux goroutine stuck writing to a paused pipe.
	go fc.cb(&leapmuxv1.InnerStreamMessage{})
	<-entered // the cb is now inside onFrame

	// Teardown must finish even though a frame is wedged in onFrame.
	handle.Cancel()
	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Done() did not close while a frame was blocked in onFrame (teardown deadlock)")
	}
	close(release) // let the blocked frame drain so the goroutine exits
}

// TestChannelTransport_LogsMalformedFrame asserts a frame that fails to decode as
// a WatchEventsResponse is surfaced at warn (not dropped silently) AND that the
// stream stays alive -- a single corrupt frame must not reach onFrame nor end the
// subscription, so a later valid frame still delivers.
// A subscription that the server terminates with an error envelope must report
// WHY. The envelope was received and discarded, so a `--follow` consumer went
// quiet -- or resubscribed into the same rejection forever -- with nothing to
// diagnose from. The sibling path (crossworker.Client.StreamInner) surfaces the
// identical envelope.
func TestChannelTransport_LogsEndingErrorEnvelope(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	h := &capturingHandler{}
	tr := NewChannelTransport(fc, slog.New(h))

	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)
	require.NotNil(t, fc.respCh)

	// The worker rejects the subscription instead of streaming.
	fc.respCh <- &leapmuxv1.InnerRpcResponse{
		Error: &leapmuxv1.RpcError{Code: 7, Message: "only the worker owner can watch events"},
	}

	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a terminal error envelope must end the subscription")
	}

	records := h.snapshot()
	require.Len(t, records, 1, "the terminal error must be logged, not discarded")
	require.Equal(t, slog.LevelError, records[0].Level)
	require.Contains(t, records[0].Message, "server error")
	// The reason is the part an operator acts on, so the record must carry the
	// code and the message of the envelope, not only the fact that it ended.
	attrs := recordAttrs(records[0])
	assert.Equal(t, "channel", attrs["transport"])
	assert.EqualValues(t, 7, attrs["code"])
	assert.Equal(t, "only the worker owner can watch events", attrs["error"])
}

// The channel transport opens the subscription under the worker's WatchEvents
// procedure. The worker dispatches by the full procedure name, so any other
// name finds no handler.
func TestChannelTransport_OpensTheWatchEventsProcedure(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	handle, err := NewChannelTransport(fc, nil).OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)
	t.Cleanup(func() { handle.Cancel(); <-handle.Done() })

	assert.Equal(t, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, fc.method)
}

// A stream frame that carries an error ends the subscription, as the final
// error response does. The frame holds no event, so it must not reach
// onFrame, and its reason must reach the log.
func TestChannelTransport_AStreamFrameWithAnErrorEndsTheSubscription(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	h := &capturingHandler{}
	frames := 0
	handle, err := NewChannelTransport(fc, slog.New(h)).OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	fc.cb(&leapmuxv1.InnerStreamMessage{Error: &leapmuxv1.RpcError{Code: 5, Message: "the agent is gone"}})

	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("an error frame must end the subscription")
	}
	assert.Zero(t, frames, "an error frame carries no event")
	records := h.snapshot()
	require.Len(t, records, 1)
	assert.Equal(t, slog.LevelError, records[0].Level)
	attrs := recordAttrs(records[0])
	assert.Equal(t, "channel", attrs["transport"])
	assert.EqualValues(t, 5, attrs["code"])
	assert.Equal(t, "the agent is gone", attrs["error"])
}

// A subscription the server ends CLEANLY (a non-error response) must not be
// reported as a failure.
func TestChannelTransport_CleanEndingResponseIsNotLoggedAsError(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	h := &capturingHandler{}
	tr := NewChannelTransport(fc, slog.New(h))

	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)
	require.NotNil(t, fc.respCh)

	fc.respCh <- &leapmuxv1.InnerRpcResponse{}

	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a terminal response must end the subscription")
	}
	require.Empty(t, h.records, "a clean termination is not an error")
}

func TestChannelTransport_UpdateSendsStreamRequest(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)
	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)

	req := &leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{AgentWatchEntry("a-1", 0)},
	}
	require.NoError(t, handle.Update(req))
	require.Len(t, fc.streamRequests, 1)
	assert.False(t, fc.streamRequests[0].cancel)
	assert.NotEmpty(t, fc.streamRequests[0].payload)
}

func TestChannelTransport_CancelSendsCancelStream(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)
	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)
	handle.Cancel()
	require.Equal(t, []uint64{1}, fc.cancelledStreams)
	<-handle.Done()
}

// TestChannelTransport_UpdateAfterCancelReturnsError pins the guard against a
// trailing Update that races Cancel/Done: without it, SendStreamRequest for an
// already-retired correlation id is silently dropped while Update returns nil,
// so the caller would advance its local interest believing the revision landed.
func TestChannelTransport_UpdateAfterCancelReturnsError(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)
	handle, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)
	handle.Cancel()
	<-handle.Done()

	err = handle.Update(&leapmuxv1.WatchEventsRequest{
		Agents: []*leapmuxv1.WatchAgentEntry{AgentWatchEntry("a-1", 0)},
	})
	require.Error(t, err)
	// Only the cancel-stream from Cancel landed — the trailing Update sent nothing.
	streamSends := 0
	for _, r := range fc.streamRequests {
		if !r.cancel {
			streamSends++
		}
	}
	assert.Equal(t, 0, streamSends, "Update after Cancel must not send on the wire")
}

// TestChannelTransport_CancelAfterParentCancelled pins WithoutCancel: tearing
// down the parent ctx must not prevent CancelStream from reaching the worker.
func TestChannelTransport_CancelAfterParentCancelled(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	tr := NewChannelTransport(fc, nil)
	parent, cancelParent := context.WithCancel(context.Background())
	handle, err := tr.OpenWatchEvents(parent, &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)

	cancelParent()
	handle.Cancel()
	require.Equal(t, []uint64{1}, fc.cancelledStreams,
		"CancelStream must still fire after the parent context is cancelled")
	<-handle.Done()
}

func TestChannelTransport_LogsMalformedFrame(t *testing.T) {
	fc := &fakeChannel{ctx: context.Background()}
	h := &capturingHandler{}
	tr := NewChannelTransport(fc, slog.New(h))

	frames := 0
	_, err := tr.OpenWatchEvents(context.Background(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {
		frames++
	})
	require.NoError(t, err)
	require.NotNil(t, fc.cb)

	// A payload that isn't a valid WatchEventsResponse (wire type 7 is invalid).
	fc.cb(&leapmuxv1.InnerStreamMessage{Payload: []byte{0xff, 0xff, 0xff}})
	require.Equal(t, 0, frames, "a malformed frame must not reach onFrame")
	require.Len(t, h.records, 1, "the malformed frame must be logged")
	require.Equal(t, slog.LevelWarn, h.records[0].Level)

	// The stream survived the bad frame: a subsequent valid frame still delivers.
	fc.cb(&leapmuxv1.InnerStreamMessage{})
	require.Equal(t, 1, frames, "a valid frame after a malformed one must still deliver")
}

type replayIdentityIPCHandler struct {
	leapmuxv1connect.UnimplementedControlIPCServiceHandler
	opening         chan *leapmuxv1.StreamInnerRequest
	update          chan *leapmuxv1.UpdateStreamRequest
	responsePayload []byte
}

func (handler *replayIdentityIPCHandler) StreamInner(ctx context.Context, request *leapmuxv1.StreamInnerRequest, stream leapmuxv1connect.ControlIPCServiceStreamInnerServerStream) error {
	handler.opening <- proto.Clone(request).(*leapmuxv1.StreamInnerRequest)
	payload := handler.responsePayload
	if payload == nil {
		var err error
		payload, err = proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: 1}}})
		if err != nil {
			return err
		}
	}
	if err := stream.Send(&leapmuxv1.InnerStreamMessage{Payload: payload}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestReplayOriginBothTransportsPreserveReceivedBytes(t *testing.T) {
	const rootID = "projected-root"
	const childID = "originating-child"
	const replayID = ^uint64(0)
	request := &leapmuxv1.WatchEventsRequest{UpdateId: 31, Agents: []*leapmuxv1.WatchAgentEntry{{AgentId: childID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: replayID}}}
	wireEvent := &leapmuxv1.AgentEvent{
		AgentId: rootID,
		Replay:  &leapmuxv1.ReplayOrigin{ReplayId: replayID, AgentId: childID},
		Event:   &leapmuxv1.AgentEvent_GoalChanged{GoalChanged: &leapmuxv1.AgentGoalSnapshot{Goal: &leapmuxv1.AgentGoal{Objective: "The root goal"}}},
	}
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: wireEvent}})
	require.NoError(t, err)
	assertFrame := func(t *testing.T, frame *leapmuxv1.WatchEventsResponse) {
		t.Helper()
		event := frame.GetAgentEvent()
		require.NotNil(t, event)
		assert.Equal(t, rootID, event.GetAgentId())
		assert.Equal(t, "The root goal", event.GetGoalChanged().GetGoal().GetObjective())
		require.NotNil(t, event.GetReplay())
		assert.Equal(t, replayID, event.GetReplay().GetReplayId())
		assert.Equal(t, childID, event.GetReplay().GetAgentId(), "the received frame must preserve the explicit origin")
		received, err := proto.Marshal(frame)
		require.NoError(t, err)
		assert.Equal(t, payload, received, "both actual decoders must preserve the complete protobuf response")
	}
	t.Run("channel", func(t *testing.T) {
		channel := &fakeChannel{ctx: context.Background(), streamOnSend: &leapmuxv1.InnerStreamMessage{Payload: payload}}
		var received *leapmuxv1.WatchEventsResponse
		handle, err := NewChannelTransport(channel, nil).OpenWatchEvents(t.Context(), request, func(frame *leapmuxv1.WatchEventsResponse) { received = frame })
		require.NoError(t, err)
		t.Cleanup(func() { handle.Cancel(); <-handle.Done() })
		require.NotNil(t, received)
		assertFrame(t, received)
	})
	t.Run("local IPC", func(t *testing.T) {
		handler := &replayIdentityIPCHandler{opening: make(chan *leapmuxv1.StreamInnerRequest, 1), update: make(chan *leapmuxv1.UpdateStreamRequest, 1), responsePayload: payload}
		connectServer := connect.NewServer()
		leapmuxv1connect.RegisterControlIPCServiceHandler(connectServer, handler)
		mux := http.NewServeMux()
		connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
		frames := make(chan *leapmuxv1.WatchEventsResponse, 1)
		handle, err := NewLocalIPCTransport(client, "worker", nil).OpenWatchEvents(t.Context(), request, func(frame *leapmuxv1.WatchEventsResponse) { frames <- frame })
		require.NoError(t, err)
		t.Cleanup(func() { handle.Cancel(); <-handle.Done() })
		ctx := testutil.DeadlineContext(t)
		select {
		case received := <-frames:
			assertFrame(t, received)
		case <-ctx.Done():
			t.Fatal("the actual IPC decoder received no replay frame")
		}
	})
}

func (handler *replayIdentityIPCHandler) UpdateStream(_ context.Context, request *leapmuxv1.UpdateStreamRequest) (*leapmuxv1.UpdateStreamResponse, error) {
	handler.update <- proto.Clone(request).(*leapmuxv1.UpdateStreamRequest)
	return &leapmuxv1.UpdateStreamResponse{}, nil
}

type endingIPCHandler struct {
	leapmuxv1connect.UnimplementedControlIPCServiceHandler
	finish        chan struct{}
	result        error
	cancelStarted chan struct{}
	cancelReply   chan struct{}
}

func (h *endingIPCHandler) StreamInner(ctx context.Context, _ *leapmuxv1.StreamInnerRequest, stream leapmuxv1connect.ControlIPCServiceStreamInnerServerStream) error {
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: 1}}})
	if err != nil {
		return err
	}
	if err := stream.Send(&leapmuxv1.InnerStreamMessage{Payload: payload}); err != nil {
		return err
	}
	select {
	case <-h.finish:
		return h.result
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *endingIPCHandler) UpdateStream(ctx context.Context, request *leapmuxv1.UpdateStreamRequest) (*leapmuxv1.UpdateStreamResponse, error) {
	if !request.GetCancel() {
		return &leapmuxv1.UpdateStreamResponse{}, nil
	}
	close(h.finish)
	close(h.cancelStarted)
	select {
	case <-h.cancelReply:
		return &leapmuxv1.UpdateStreamResponse{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// inBandErrorIPCHandler ends every stream with one in-band error frame, the
// way the router answers a worker handler that refuses a subscription.
type inBandErrorIPCHandler struct {
	leapmuxv1connect.UnimplementedControlIPCServiceHandler
	rpcErr *leapmuxv1.RpcError
}

func (h *inBandErrorIPCHandler) StreamInner(ctx context.Context, _ *leapmuxv1.StreamInnerRequest, stream leapmuxv1connect.ControlIPCServiceStreamInnerServerStream) error {
	if err := stream.Send(&leapmuxv1.InnerStreamMessage{Error: h.rpcErr}); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

func (*inBandErrorIPCHandler) UpdateStream(context.Context, *leapmuxv1.UpdateStreamRequest) (*leapmuxv1.UpdateStreamResponse, error) {
	return &leapmuxv1.UpdateStreamResponse{}, nil
}

// startIPCHandler serves handler as the ControlIPCService and returns a
// client for it.
func startIPCHandler(t *testing.T, handler leapmuxv1connect.ControlIPCServiceHandler) leapmuxv1connect.ControlIPCServiceClient {
	t.Helper()
	connectServer := connect.NewServer()
	leapmuxv1connect.RegisterControlIPCServiceHandler(connectServer, handler)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
}

// An in-band error frame on the local-IPC stream ends the subscription, and
// its code and message reach the log. Without the log, a `--follow` consumer
// goes quiet with nothing to diagnose from.
func TestLocalIPCTransport_AnInBandErrorEndsTheSubscription(t *testing.T) {
	client := startIPCHandler(t, &inBandErrorIPCHandler{rpcErr: &leapmuxv1.RpcError{Code: 7, Message: "only the worker owner can watch events"}})
	h := &capturingHandler{}
	frames := make(chan struct{}, 1)
	handle, err := NewLocalIPCTransport(client, "worker", slog.New(h)).OpenWatchEvents(t.Context(), &leapmuxv1.WatchEventsRequest{},
		func(*leapmuxv1.WatchEventsResponse) { frames <- struct{}{} })
	require.NoError(t, err)
	t.Cleanup(handle.Cancel)

	ctx := testutil.DeadlineContext(t)
	select {
	case <-handle.Done():
	case <-ctx.Done():
		t.Fatal("an in-band error must end the subscription")
	}
	select {
	case <-frames:
		t.Fatal("an error frame carries no event")
	default:
	}
	records := h.snapshot()
	require.Len(t, records, 1)
	assert.Equal(t, slog.LevelError, records[0].Level)
	assert.Equal(t, "streamevents: subscription ended with a server error", records[0].Message)
	attrs := recordAttrs(records[0])
	assert.Equal(t, "local-ipc", attrs["transport"])
	assert.EqualValues(t, 7, attrs["code"])
	assert.Equal(t, "only the worker owner can watch events", attrs["error"])
}

// Cancel retires the stream through UpdateStream with the cancel flag and
// the stream's OWN client request id. The router keys every stream by that
// id, so another id would leave this stream open and could retire a stream
// of another process.
func TestLocalIPCTransport_CancelRetiresItsOwnStream(t *testing.T) {
	handler := &replayIdentityIPCHandler{opening: make(chan *leapmuxv1.StreamInnerRequest, 1), update: make(chan *leapmuxv1.UpdateStreamRequest, 1)}
	client := startIPCHandler(t, handler)
	handle, err := NewLocalIPCTransport(client, "worker", nil).OpenWatchEvents(t.Context(), &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) {})
	require.NoError(t, err)

	ctx := testutil.DeadlineContext(t)
	var opening *leapmuxv1.StreamInnerRequest
	select {
	case opening = <-handler.opening:
	case <-ctx.Done():
		t.Fatal("the IPC handler received no opening request")
	}
	require.NotEmpty(t, opening.GetClientRequestId(), "the router needs an id to key the stream by")

	handle.Cancel()
	select {
	case update := <-handler.update:
		assert.True(t, update.GetCancel(), "a retirement sets the cancel flag")
		assert.Equal(t, opening.GetClientRequestId(), update.GetClientRequestId())
		assert.Empty(t, update.GetPayload(), "a retirement carries no revision")
	case <-ctx.Done():
		t.Fatal("Cancel sent no retirement")
	}
	select {
	case <-handle.Done():
	case <-ctx.Done():
		t.Fatal("Cancel must end the subscription")
	}
	assert.ErrorIs(t, handle.Update(&leapmuxv1.WatchEventsRequest{}), errHandleClosed,
		"a revision after Cancel must report that it did not land")
}

func TestLocalIPCTransport_DistinguishesCancellationFromTransportFailure(t *testing.T) {
	for _, mode := range []string{"parent cancellation", "explicit cancellation", "remote failure"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			parent, cancelParent := context.WithCancel(ctx)
			defer cancelParent()
			handler := &endingIPCHandler{
				finish: make(chan struct{}), cancelStarted: make(chan struct{}), cancelReply: make(chan struct{}),
				result: connect.NewError(connect.CodeCanceled, "remote subscription canceled"),
			}
			if mode == "remote failure" {
				handler.result = connect.NewError(connect.CodeUnavailable, "worker disconnected")
			}
			server := connect.NewServer()
			leapmuxv1connect.RegisterControlIPCServiceHandler(server, handler)
			mux := http.NewServeMux()
			connecthttp.Mount(mux, server)
			httpServer := httptest.NewServer(mux)
			t.Cleanup(httpServer.Close)
			var replyOnce sync.Once
			releaseReply := func() { replyOnce.Do(func() { close(handler.cancelReply) }) }
			t.Cleanup(releaseReply)
			client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(httpServer.Client(), httpServer.URL)))
			logs := &capturingHandler{}
			frame := make(chan struct{}, 1)
			handle, err := NewLocalIPCTransport(client, "worker", slog.New(logs)).OpenWatchEvents(parent, &leapmuxv1.WatchEventsRequest{}, func(*leapmuxv1.WatchEventsResponse) { frame <- struct{}{} })
			require.NoError(t, err)
			select {
			case <-frame:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			cancelDone := make(chan struct{})
			switch mode {
			case "parent cancellation":
				cancelParent()
			case "explicit cancellation":
				go func() { handle.Cancel(); close(cancelDone) }()
				select {
				case <-handler.cancelStarted:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			case "remote failure":
				close(handler.finish)
			}
			select {
			case <-handle.Done():
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			logs.mu.Lock()
			records := append([]slog.Record(nil), logs.records...)
			logs.mu.Unlock()
			if mode == "remote failure" {
				require.Len(t, records, 1)
				assert.Equal(t, slog.LevelError, records[0].Level)
				assert.Equal(t, "streamevents: subscription ended with a transport error", records[0].Message)
			} else {
				assert.Empty(t, records, "intentional cancellation must not report a transport failure")
			}
			releaseReply()
			if mode == "explicit cancellation" {
				select {
				case <-cancelDone:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
		})
	}
}

func TestSubscriptionReplayIdentityBothTransportsPreserveActualRequestBytes(t *testing.T) {
	const identity = uint64(9007199254740995)
	const cursor = int64(9007199254740993)
	tail := int64(0)
	request := &leapmuxv1.WatchEventsRequest{UpdateId: identity + 1, Agents: []*leapmuxv1.WatchAgentEntry{{
		AgentId: "agent", Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL, ReplayId: identity,
		Replay: leapmuxv1.WatchReplayMode_WATCH_REPLAY_MODE_AFTER_CURSOR, CursorSeq: cursor, WindowTailSeq: &tail,
	}}}
	assertPayload := func(t *testing.T, payload []byte) {
		t.Helper()
		var decoded leapmuxv1.WatchEventsRequest
		require.NoError(t, proto.Unmarshal(payload, &decoded))
		assert.True(t, proto.Equal(request, &decoded))
		require.Len(t, decoded.Agents, 1)
		assert.Equal(t, identity, decoded.Agents[0].ReplayId)
		assert.Equal(t, cursor, decoded.Agents[0].CursorSeq)
		require.NotNil(t, decoded.Agents[0].WindowTailSeq)
		assert.Zero(t, *decoded.Agents[0].WindowTailSeq)
	}
	t.Run("channel", func(t *testing.T) {
		channel := &replayIdentityChannel{fakeChannel: &fakeChannel{ctx: context.Background()}}
		handle, err := NewChannelTransport(channel, nil).OpenWatchEvents(t.Context(), request, func(*leapmuxv1.WatchEventsResponse) {})
		require.NoError(t, err)
		t.Cleanup(func() { handle.Cancel(); <-handle.Done() })
		assertPayload(t, channel.openings[0])
		require.NoError(t, handle.Update(request))
		assertPayload(t, channel.revisions[0])
	})
	t.Run("local IPC", func(t *testing.T) {
		handler := &replayIdentityIPCHandler{opening: make(chan *leapmuxv1.StreamInnerRequest, 1), update: make(chan *leapmuxv1.UpdateStreamRequest, 1)}
		connectServer := connect.NewServer()
		leapmuxv1connect.RegisterControlIPCServiceHandler(connectServer, handler)
		mux := http.NewServeMux()
		connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		client := leapmuxv1connect.NewControlIPCServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))
		handle, err := NewLocalIPCTransport(client, "worker", nil).OpenWatchEvents(t.Context(), request, func(*leapmuxv1.WatchEventsResponse) {})
		require.NoError(t, err)
		t.Cleanup(func() { handle.Cancel(); <-handle.Done() })
		ctx := testutil.DeadlineContext(t)
		var opening *leapmuxv1.StreamInnerRequest
		select {
		case opening = <-handler.opening:
		case <-ctx.Done():
			t.Fatal("the IPC handler received no opening request")
		}
		assert.Equal(t, "worker."+leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, opening.Method)
		assert.Equal(t, "worker", opening.TargetWorkerId)
		assertPayload(t, opening.Payload)
		require.NoError(t, handle.Update(request))
		select {
		case update := <-handler.update:
			assert.Equal(t, opening.ClientRequestId, update.ClientRequestId)
			assertPayload(t, update.Payload)
		case <-ctx.Done():
			t.Fatal("the IPC handler received no update request")
		}
	})
}

// echoStreamDispatcher stands in for the worker's WatchEvents handler behind a
// real control-IPC router. Each stream sends UpdateAck 1 when it opens, and
// UpdateAck 2 on its OWN stream for each revision the router delivers to it, so
// a test can tell which stream a revision reached.
type echoStreamDispatcher struct{}

type echoController struct{ frames chan struct{} }

func (c echoController) OnClientFrame([]byte) {
	select {
	case c.frames <- struct{}{}:
	default:
	}
}

func (echoController) OnCancel() {}

func (echoStreamDispatcher) DispatchWith(ctx context.Context, _ channel.Caller, _ *leapmuxv1.InnerRpcRequest, w channel.ResponseWriter) {
	ctrl := echoController{frames: make(chan struct{}, 1)}
	release, ok := w.BindStream(ctrl)
	if !ok {
		return
	}
	defer release()
	sendAck := func(updateID uint64) bool {
		payload, err := proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: updateID}}})
		return err == nil && w.SendStream(&leapmuxv1.InnerStreamMessage{Payload: payload}) == nil
	}
	if !sendAck(1) {
		return
	}
	for {
		select {
		case <-ctrl.frames:
			if !sendAck(2) {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// Two CLI processes that share one worker each open a subscription through
// their own transport. The router keys a stream by its client request id, so
// the two ids must differ, or the second stream replaces the first.
func TestTwoTransportsOnOneRouterKeepTheirStreams(t *testing.T) {
	socketURL := locallistentest.UniqueListenURL(t, "twotransports")
	token := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: socketURL,
		Token:     token,
		TokenInfo: controlipc.TokenInfo{UserID: userid.MustNew("u-1"), WorkerID: "worker-A"},
		Router: &controlipc.Router{
			WorkerID:        "worker-A",
			UserID:          userid.MustNew("u-1"),
			LocalDispatcher: echoStreamDispatcher{},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	ctx := testutil.DeadlineContext(t)
	open := func() (Handle, <-chan uint64) {
		// A separate client and transport for each subscription, as two CLI
		// processes hold.
		c, err := control.NewLocalClient(socketURL, token)
		require.NoError(t, err)
		ipc, err := c.ControlIPCStreamService()
		require.NoError(t, err)
		acks := make(chan uint64, 4)
		handle, err := NewLocalIPCTransport(ipc, "", nil).OpenWatchEvents(ctx, &leapmuxv1.WatchEventsRequest{},
			func(frame *leapmuxv1.WatchEventsResponse) { acks <- frame.GetUpdateAck().GetUpdateId() })
		require.NoError(t, err)
		t.Cleanup(handle.Cancel)
		select {
		case id := <-acks:
			require.Equal(t, uint64(1), id, "the stream opens with UpdateAck 1")
		case <-ctx.Done():
			t.Fatal("the subscription never opened")
		}
		return handle, acks
	}
	first, firstAcks := open()
	second, secondAcks := open()

	require.NoError(t, first.Update(&leapmuxv1.WatchEventsRequest{}), "the first subscription accepts a revision")
	select {
	case id := <-firstAcks:
		assert.Equal(t, uint64(2), id, "the first stream answers its own revision")
	case <-secondAcks:
		t.Fatal("the first subscription's revision reached the second subscription's stream")
	case <-first.Done():
		t.Fatal("opening the second subscription ended the first")
	case <-ctx.Done():
		t.Fatal("the first stream never answered its revision")
	}
	select {
	case <-second.Done():
		t.Fatal("the second subscription ended")
	default:
	}
}
