package streamevents

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/tunnel"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
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

// fakeChannel is a minimal channelLike that captures the stream callback so a
// test can invoke it deterministically (standing in for the channel demux
// goroutine), and exposes a cancellable Context().
type fakeChannel struct {
	cb           func(*leapmuxv1.InnerStreamMessage)
	streamOnSend *leapmuxv1.InnerStreamMessage
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

func (f *fakeChannel) SendRPCNoWait(_ context.Context, _ string, _ []byte, handlers tunnel.RPCHandlers) (uint64, error) {
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
		IsError:      true,
		ErrorCode:    7,
		ErrorMessage: "only the worker owner can watch events",
	}

	select {
	case <-handle.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a terminal error envelope must end the subscription")
	}

	require.Len(t, h.records, 1, "the terminal error must be logged, not discarded")
	require.Equal(t, slog.LevelError, h.records[0].Level)
	require.Contains(t, h.records[0].Message, "server error")
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

func (handler *replayIdentityIPCHandler) StreamInner(ctx context.Context, request *connect.Request[leapmuxv1.StreamInnerRequest], stream *connect.ServerStream[leapmuxv1.StreamInnerEnvelope]) error {
	handler.opening <- proto.Clone(request.Msg).(*leapmuxv1.StreamInnerRequest)
	payload := handler.responsePayload
	if payload == nil {
		var err error
		payload, err = proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_UpdateAck{UpdateAck: &leapmuxv1.WatchUpdateAck{UpdateId: 1}}})
		if err != nil {
			return err
		}
	}
	if err := stream.Send(&leapmuxv1.StreamInnerEnvelope{Payload: payload}); err != nil {
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
	eventBytes, err := proto.Marshal(&leapmuxv1.AgentEvent{AgentId: rootID, Replay: true, ReplayId: replayID, Event: &leapmuxv1.AgentEvent_GoalChanged{GoalChanged: &leapmuxv1.AgentGoalChanged{AgentId: rootID, Goal: &leapmuxv1.AgentGoal{Objective: "The root goal"}}}})
	require.NoError(t, err)
	eventBytes = protowire.AppendTag(eventBytes, 17, protowire.BytesType)
	eventBytes = protowire.AppendString(eventBytes, childID)
	var wireEvent leapmuxv1.AgentEvent
	require.NoError(t, proto.Unmarshal(eventBytes, &wireEvent))
	payload, err := proto.Marshal(&leapmuxv1.WatchEventsResponse{Event: &leapmuxv1.WatchEventsResponse_AgentEvent{AgentEvent: &wireEvent}})
	require.NoError(t, err)
	assertFrame := func(t *testing.T, frame *leapmuxv1.WatchEventsResponse) {
		t.Helper()
		event := frame.GetAgentEvent()
		require.NotNil(t, event)
		assert.Equal(t, rootID, event.GetAgentId())
		assert.Equal(t, rootID, event.GetGoalChanged().GetAgentId())
		assert.Equal(t, "The root goal", event.GetGoalChanged().GetGoal().GetObjective())
		assert.True(t, event.GetReplay())
		assert.Equal(t, replayID, event.GetReplayId())
		message := event.ProtoReflect()
		field := message.Descriptor().Fields().ByName("replay_agent_id")
		var origin string
		if field != nil && field.Kind() == protoreflect.StringKind {
			origin = message.Get(field).String()
		}
		assert.Equal(t, childID, origin, "the actual generated received field must preserve the explicit origin")
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
		_, serve := leapmuxv1connect.NewControlIPCServiceHandler(handler)
		server := httptest.NewServer(serve)
		t.Cleanup(server.Close)
		client := leapmuxv1connect.NewControlIPCServiceClient(server.Client(), server.URL)
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

func (handler *replayIdentityIPCHandler) UpdateStream(_ context.Context, request *connect.Request[leapmuxv1.UpdateStreamRequest]) (*connect.Response[leapmuxv1.UpdateStreamResponse], error) {
	handler.update <- proto.Clone(request.Msg).(*leapmuxv1.UpdateStreamRequest)
	return connect.NewResponse(&leapmuxv1.UpdateStreamResponse{}), nil
}

func (*replayIdentityIPCHandler) Cancel(context.Context, *connect.Request[leapmuxv1.CancelRequest]) (*connect.Response[leapmuxv1.CancelResponse], error) {
	return connect.NewResponse(&leapmuxv1.CancelResponse{}), nil
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
		_, serve := leapmuxv1connect.NewControlIPCServiceHandler(handler)
		server := httptest.NewServer(serve)
		t.Cleanup(server.Close)
		client := leapmuxv1connect.NewControlIPCServiceClient(server.Client(), server.URL)
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
		assert.Equal(t, "worker.WatchEvents", opening.Method)
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
