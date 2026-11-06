package channel

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// setupTestSessions creates a composite keypair and performs a full hybrid handshake,
// returning the worker and initiator sessions.
func setupTestSessions(t *testing.T) (*noiseutil.Session, *noiseutil.Session) {
	t.Helper()
	ck, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)

	slhdsaPub, err := ck.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	hs, msg1, err := noiseutil.InitiatorHandshake1(ck.X25519Public, ck.MlkemPublicKeyBytes())
	require.NoError(t, err)

	msg2, workerSession, err := noiseutil.ResponderHandshake(ck, msg1)
	require.NoError(t, err)

	initiatorSession, err := noiseutil.InitiatorHandshake2(hs, msg2, slhdsaPub)
	require.NoError(t, err)

	return workerSession, initiatorSession
}

func TestDispatcher_RegisterAndDispatch(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	var calledWith struct {
		userID userid.UserID
		method string
	}

	d.Handle("test.method", Registration{}, func(_ context.Context, caller Caller, req *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		calledWith.userID = caller.UserID
		calledWith.method = req.GetMethod()
		_ = sender.SendResponse(channelwire.NewPayloadResponse([]byte("ok")))
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	d.Dispatch(context.Background(), LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "test.method",
	}, 7, cs)

	assert.Equal(t, "user-1", calledWith.userID.String())
	assert.Equal(t, "test.method", calledWith.method)

	// Verify response was sent and can be decrypted.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.Equal(t, uint64(7), chMsg.GetCorrelationId())
	assert.Equal(t, []byte("ok"), resp.GetPayload())
}

func TestDispatcher_PanicRecovery(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	d.Handle("panicking", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {
		panic("test panic in handler")
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	// Dispatch should not panic — the panic should be recovered and
	// an INTERNAL error response should be sent instead.
	require.NotPanics(t, func() {
		d.Dispatch(context.Background(), LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
			Method: "panicking",
		}, 42, cs)
	})

	// Should get an INTERNAL error response.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.NotNil(t, resp.GetError())
	assert.Equal(t, int32(13), resp.GetError().GetCode()) // INTERNAL
	assert.Contains(t, resp.GetError().GetMessage(), "internal error")
}

func TestDispatcher_UnknownMethod(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	d.Handle("known", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	d.Dispatch(context.Background(), LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{
		Method: "unknown",
	}, 1, cs)

	// Should get an UNIMPLEMENTED error.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.NotNil(t, resp.GetError())
	assert.Equal(t, int32(12), resp.GetError().GetCode())
	assert.Contains(t, resp.GetError().GetMessage(), "unknown")
}

// TestDispatcher_CtxPropagated pins the dispatcher → handler ctx hand-off
// that the per-session cancellation depends on: a cancelled inbound ctx
// must be observable inside the handler so the handler's
// `exec.CommandContext` (and other ctx-aware ops) cancel as soon as the
// channel session is torn down. Without this, the gitReadTimeout cap is
// the *only* defense against a wedged subprocess, and we'd revert to the
// pre-refactor "client disposal can't reach the worker" behavior.
func TestDispatcher_CtxPropagated(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	gotCtxC := make(chan context.Context, 1)
	d.Handle("inspect-ctx", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		gotCtxC <- ctx
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})

	workerSession, _ := setupTestSessions(t)
	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	parent, cancel := context.WithCancel(context.Background())
	d.Dispatch(parent, LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{Method: "inspect-ctx"}, 99, cs)

	// Handler receives the exact ctx we passed in.
	select {
	case got := <-gotCtxC:
		require.Same(t, parent, got, "handler must receive the inbound ctx by identity")
		// Sanity: cancelling the parent ctx is observable from the
		// received ctx (defensive against a future refactor wrapping
		// without preserving cancellation).
		require.NoError(t, got.Err(), "ctx must not be cancelled before parent cancel")
		cancel()
		require.ErrorIs(t, got.Err(), context.Canceled, "handler ctx must propagate parent cancellation")
	default:
		cancel()
		t.Fatal("handler was not invoked")
	}
}

// TestDispatcher_CtxAlreadyCancelled exercises the late-cancel race: the
// session ctx may already be cancelled by the time DispatchWith reaches
// the handler (the channel was closed while the receive-loop goroutine
// was still running). The handler must observe Done() immediately so it
// can short-circuit instead of spinning up a subprocess.
func TestDispatcher_CtxAlreadyCancelled(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	seenC := make(chan bool, 1)
	d.Handle("watch", Registration{}, func(ctx context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		select {
		case <-ctx.Done():
			seenC <- true
		default:
			seenC <- false
		}
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})

	workerSession, _ := setupTestSessions(t)
	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxReassembled: contracts.DefaultMaxReassembledMessageSize,
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	d.Dispatch(parent, LocalAgentCaller(userid.MustNew("user-1")), &leapmuxv1.InnerRpcRequest{Method: "watch"}, 1, cs)

	require.True(t, <-seenC, "handler must see ctx.Done() synchronously when parent is pre-cancelled")
}

// streamRecordingWriter records the stream frames a handler sends.
type streamRecordingWriter struct {
	stubWriter
	mu     sync.Mutex
	frames []*leapmuxv1.InnerStreamMessage
}

func (w *streamRecordingWriter) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.frames = append(w.frames, msg)
	return nil
}

// A streaming method answers a handler panic in the stream's own shape: one
// frame that fails the stream with Internal and ends it.
func TestDispatcher_PanicInAStreamHandlerSendsAStreamErrorFrame(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	d.Handle("panicking-stream", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		panic("test panic in a stream handler")
	})

	w := &streamRecordingWriter{}
	require.NotPanics(t, func() {
		d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
			&leapmuxv1.InnerRpcRequest{Method: "panicking-stream"}, w)
	})

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.frames, 1)
	frame := w.frames[0]
	require.NotNil(t, frame.GetError())
	assert.Equal(t, int32(codes.Internal), frame.GetError().GetCode())
	assert.Equal(t, "internal error", frame.GetError().GetMessage())
	assert.True(t, frame.GetEnd())
	assert.Zero(t, w.sent.Load(), "a streaming method answers in-band, never with a unary error")
}

// The registration decides the reply shape, so a streaming handler that answers
// with SendError cannot leave the client holding a stream with no End.
func TestDispatcher_StreamingHandlerSendErrorArrivesAsOneStreamFrame(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	d.Handle("stream", Registration{Streaming: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		_ = sender.SendError(int32(codes.NotFound), "no such stream")
	})

	w := &streamRecordingWriter{}
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "stream"}, w)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.frames, 1)
	frame := w.frames[0]
	require.NotNil(t, frame.GetError())
	assert.Equal(t, int32(codes.NotFound), frame.GetError().GetCode())
	assert.Equal(t, "no such stream", frame.GetError().GetMessage())
	assert.True(t, frame.GetEnd(), "the error frame must end the stream")
	assert.Zero(t, w.sent.Load(), "a streaming method never answers with a unary error")
}

// A handler may retain its writer past return, as WatchEvents does, so the
// retained writer must keep the declared shape after the dispatcher returned.
func TestDispatcher_RetainedStreamWriterKeepsItsShape(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var retained ResponseWriter
	d.Handle("stream", Registration{Streaming: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		retained = sender
	})

	w := &streamRecordingWriter{}
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "stream"}, w)
	require.NotNil(t, retained)
	require.NoError(t, retained.SendError(int32(codes.Internal), "late failure"))

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.frames, 1)
	assert.Equal(t, int32(codes.Internal), w.frames[0].GetError().GetCode())
	assert.True(t, w.frames[0].GetEnd())
	assert.Zero(t, w.sent.Load())
}

// The shape changes SendError alone: a stream's data frames, its writer's
// metadata and its stream binding still reach the transport's writer.
func TestDispatcher_StreamWriterForwardsEverythingButSendError(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	inner := &streamMetadataWriter{}
	d.Handle("stream", Registration{Streaming: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		assert.Equal(t, "channel-1", sender.ChannelID())
		assert.Equal(t, 4096, sender.MaxPayloadBudget())
		_, ok := sender.BindStream(nil)
		assert.True(t, ok)
		assert.NoError(t, sender.SendStream(&leapmuxv1.InnerStreamMessage{Payload: []byte("data")}))
	})

	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "stream"}, inner)

	inner.mu.Lock()
	defer inner.mu.Unlock()
	require.Len(t, inner.frames, 1)
	assert.Equal(t, []byte("data"), inner.frames[0].GetPayload())
	assert.Nil(t, inner.frames[0].GetError())
	assert.False(t, inner.frames[0].GetEnd())
	assert.Equal(t, 1, inner.binds)
}

// streamMetadataWriter is a streamRecordingWriter that also states a channel and
// counts its stream bindings.
type streamMetadataWriter struct {
	streamRecordingWriter
	binds int
}

func (*streamMetadataWriter) ChannelID() string     { return "channel-1" }
func (*streamMetadataWriter) MaxPayloadBudget() int { return 4096 }
func (w *streamMetadataWriter) BindStream(StreamController) (func(), bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.binds++
	return func() {}, true
}

// unpreparableInlineWriter records stream frames, can never prepare a stream,
// and offers no error queue, so the dispatcher must send its refusal inline.
type unpreparableInlineWriter struct{ streamRecordingWriter }

func (*unpreparableInlineWriter) prepareStream() (func(), bool) { return nil, false }

// A writer with no error queue still receives the refusal of a stream that
// cannot be prepared, inline and in the stream's shape, instead of no reply at
// all.
func TestDispatcher_RefusesAStreamItCannotPrepareInlineWithoutAQueue(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var invoked atomic.Bool
	d.Handle("stream", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		invoked.Store(true)
	})
	w := &unpreparableInlineWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "stream"}, w)

	w.mu.Lock()
	defer w.mu.Unlock()
	require.Len(t, w.frames, 1)
	frame := w.frames[0]
	require.NotNil(t, frame.GetError())
	assert.Equal(t, int32(codes.FailedPrecondition), frame.GetError().GetCode())
	assert.True(t, frame.GetEnd())
	assert.Zero(t, w.sent.Load(), "a streaming method is never refused with a unary error")
	assert.False(t, invoked.Load(), "a stream that cannot be prepared never reaches its handler")
}

// A second registration for a method must fail loudly. A silent overwrite would
// replace the first handler, with its owner gate and its reply shape, and
// nothing would show it. The check ignores the Registration, so a method
// registered with one of them is refused with each of the others.
func TestDispatcher_HandlePanicsOnADuplicateMethod(t *testing.T) {
	t.Parallel()

	registrations := map[string]Registration{
		"plain":              {},
		"tracked":            {Tracked: true},
		"streaming":          {Streaming: true},
		"tracked, streaming": {Tracked: true, Streaming: true},
	}
	const method = "/leapmux.v1.WorkerSystemService/Ping"
	for firstName, first := range registrations {
		for secondName, second := range registrations {
			t.Run(firstName+" then "+secondName, func(t *testing.T) {
				d := NewDispatcher()
				var firstCalls atomic.Int32
				d.Handle(method, first, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
					firstCalls.Add(1)
				})

				assert.PanicsWithValue(t, "duplicate inner RPC handler: "+method, func() {
					d.Handle(method, second, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
						t.Error("the refused handler must never run")
					})
				})

				// The refusal left the first handler in place.
				d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
					&leapmuxv1.InnerRpcRequest{Method: method}, &stubWriter{})
				assert.Equal(t, int32(1), firstCalls.Load(), "the first handler still serves the method")
				assert.Equal(t, []string{method}, d.Methods())
			})
		}
	}
}

// stubWriter is a minimal ResponseWriter for the tests that do not need an
// encrypted round trip. It counts the calls that send a reply.
type stubWriter struct{ sent atomic.Int32 }

func (s *stubWriter) SendResponse(*leapmuxv1.InnerRpcResponse) error {
	s.sent.Add(1)
	return nil
}

func (s *stubWriter) SendError(int32, string) error {
	s.sent.Add(1)
	return nil
}

func (s *stubWriter) SendStream(*leapmuxv1.InnerStreamMessage) error {
	s.sent.Add(1)
	return nil
}

func (*stubWriter) ChannelID() string     { return "" }
func (*stubWriter) MaxPayloadBudget() int { return 0 }
func (*stubWriter) BindStream(StreamController) (func(), bool) {
	return func() {}, false
}

// TestDispatcher_DispatchAsync_AddHappensBeforeGoroutine pins the
// happens-before invariant that motivated the Registration.Tracked /
// BindCleanup / DispatchAsync trio. Add(1) MUST execute before
// DispatchAsync returns, so a caller that immediately calls Wait() on
// the bound WaitGroup is forced to wait for the handler to finish.
// The previous pattern (`go DispatchWith(...)` + per-handler Add(1))
// failed this invariant: Wait could observe counter=0 in the window
// between the goroutine launching and the handler reaching its
// Cleanup.Add(1).
func TestDispatcher_DispatchAsync_AddHappensBeforeGoroutine(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	// Block the handler on a channel so we can observe the
	// pre-completion WaitGroup state from outside.
	release := make(chan struct{})
	handlerEntered := make(chan struct{})
	d.Handle("slow-mutation", Registration{Tracked: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {
		close(handlerEntered)
		<-release
	})

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "slow-mutation"}, w)

	// At this point DispatchAsync has returned. The Add(1) must have
	// fired BEFORE the goroutine launched, so Wait() in a sibling
	// goroutine should still be blocked.
	waitReturned := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitReturned)
	}()

	// Give the goroutine a moment to fall into Wait(). It must NOT
	// return until we release the handler — that's the invariant.
	select {
	case <-waitReturned:
		t.Fatal("wg.Wait() returned before the handler finished — Add(1) must happen-before DispatchAsync returns")
	case <-time.After(50 * time.Millisecond):
		// Expected: still blocked.
	}

	// Confirm the handler is actually running (no scheduling fluke).
	select {
	case <-handlerEntered:
	case <-time.After(time.Second):
		t.Fatal("tracked handler never entered")
	}

	close(release)

	select {
	case <-waitReturned:
		// Expected: Done() fired after the handler returned.
	case <-time.After(time.Second):
		t.Fatal("wg.Wait() did not return after the handler finished")
	}
}

// TestDispatcher_DispatchAsync_UntrackedNoOp pins that untracked
// methods don't touch the bound WaitGroup, so a handler registered without
// Tracked stays fire-and-forget on Shutdown for read-only probes.
func TestDispatcher_DispatchAsync_UntrackedNoOp(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	done := make(chan struct{})
	d.Handle("readonly", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {
		close(done)
	})

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "readonly"}, w)

	// wg.Wait() must return immediately — an untracked registration does
	// NOT Add(1).
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("wg.Wait() did not return promptly for an untracked handler")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("untracked handler never ran")
	}
}

// TestDispatcher_DispatchWith_TrackedAddsAndDones pins the
// synchronous-path equivalent: the local-IPC router calls DispatchWith
// directly (not via a goroutine), so the Add(1) must happen at the
// top of the function and Done() must fire on return — even when the
// handler panics.
func TestDispatcher_DispatchWith_TrackedAddsAndDones(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	d.Handle("ok", Registration{Tracked: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {})
	d.Handle("boom", Registration{Tracked: true}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, _ ResponseWriter) {
		panic("handler panic")
	})

	w := &stubWriter{}
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "ok"}, w)
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "boom"}, w)

	// Both should have Add+Done'd by now. Wait must return promptly;
	// a leak would hang.
	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("wg.Wait() leaked — Done() must fire even when the handler panics")
	}
}

// TestDispatcher_DispatchAsync_UnknownMethod pins that an unknown
// method on DispatchAsync doesn't accidentally Add(1) (no handler
// means no tracking decision can be made), and still sends the
// Unimplemented error to the writer.
func TestDispatcher_DispatchAsync_UnknownMethod(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "no-such-method"}, w)

	waitDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(time.Second):
		t.Fatal("wg.Wait() did not return for an unknown method — DispatchAsync must not Add(1) when no handler matched")
	}

	// The Unimplemented response is sent on a goroutine spawned by
	// DispatchAsync; give it a brief moment to land. We don't care
	// about the exact code/message here — that's pinned by
	// TestDispatcher_UnknownMethodReturnsUnimplemented elsewhere — we
	// just need to know the goroutine eventually fires the send so a
	// hung DispatchAsync doesn't silently swallow the request.
	deadline := time.Now().Add(time.Second)
	for w.sent.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	require.Equal(t, int32(1), w.sent.Load(), "DispatchAsync must still send a response for an unknown method")
}

// TestDispatcher_InvokePassesWriterThrough pins that invoke hands a unary
// method's ResponseWriter to the handler by identity — no wrapping pass-through
// layer between DispatchWith and the handler. Only a streaming method needs the
// reply shape of shapedWriter. Reintroducing a wrapper for a unary method (e.g.
// the deleted Sender) would allocate per dispatch and break Same.
func TestDispatcher_InvokePassesWriterThrough(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	var got ResponseWriter
	d.Handle("identity", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		got = sender
	})

	w := &stubWriter{}
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "identity"}, w)
	assert.Same(t, w, got, "handler must receive the DispatchWith writer by identity")
}

// TestDispatcher_AsyncPassesWriterThrough is the DispatchAsync twin of
// TestDispatcher_InvokePassesWriterThrough — both entry points share
// invoke, so a future wrap of a unary writer must fail both paths.
func TestDispatcher_AsyncPassesWriterThrough(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	gotC := make(chan ResponseWriter, 1)
	d.Handle("identity", Registration{}, func(_ context.Context, _ Caller, _ *leapmuxv1.InnerRpcRequest, sender ResponseWriter) {
		gotC <- sender
	})

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "identity"}, w)

	select {
	case got := <-gotC:
		assert.Same(t, w, got, "handler must receive the DispatchAsync writer by identity")
	case <-time.After(time.Second):
		t.Fatal("handler was not invoked")
	}
}

// --- shutdown admission and the handler join ---

// recordingWriter captures every send by shape, so a refusal's reply shape is
// observable: a unary method must not be answered with a stream frame.
type recordingWriter struct {
	responses atomic.Int32
	errors    atomic.Int32
	streams   atomic.Int32
}

func (w *recordingWriter) SendResponse(*leapmuxv1.InnerRpcResponse) error {
	w.responses.Add(1)
	return nil
}

func (w *recordingWriter) SendError(int32, string) error {
	w.errors.Add(1)
	return nil
}

func (w *recordingWriter) SendStream(*leapmuxv1.InnerStreamMessage) error {
	w.streams.Add(1)
	return nil
}

func (*recordingWriter) ChannelID() string     { return "" }
func (*recordingWriter) MaxPayloadBudget() int { return 0 }
func (*recordingWriter) BindStream(StreamController) (func(), bool) {
	return func() {}, false
}

// A closed gate must refuse both dispatch paths, in each method's declared
// reply shape, with the shutdown as the answer.
func TestDispatcherRefusesAfterCloseAdmission(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()
	ran := atomic.Bool{}
	d.Handle("leapmux.v1.WorkerAgentService/Mutate", Registration{Tracked: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		ran.Store(true)
	})
	d.Handle("leapmux.v1.WorkerWatchService/Watch", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		ran.Store(true)
	})
	d.CloseAdmission()

	wUnary := &recordingWriter{}
	d.DispatchWith(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "leapmux.v1.WorkerAgentService/Mutate"}, wUnary)
	assert.Equal(t, int32(1), wUnary.errors.Load(), "a unary refusal is one unary error")
	assert.Zero(t, wUnary.streams.Load())
	assert.False(t, ran.Load(), "a refused handler must not run")

	wStream := &recordingWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "leapmux.v1.WorkerWatchService/Watch"}, wStream)
	assert.Eventually(t, func() bool { return wStream.streams.Load() == 1 },
		time.Second, 5*time.Millisecond, "a streaming refusal is one stream error frame")
	assert.Zero(t, wStream.errors.Load())
	assert.False(t, ran.Load(), "a refused handler must not run")
}

// An untracked handler is exactly the dispatch the old Cleanup.Wait missed,
// so WaitHandlers must join it like any other.
func TestDispatcherWaitHandlersJoinsAnUntrackedHandler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := NewDispatcher()
		release := make(chan struct{})
		entered := make(chan struct{})
		d.Handle("plain", Registration{}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
			close(entered)
			<-release
		})

		d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
			&leapmuxv1.InnerRpcRequest{Method: "plain"}, &recordingWriter{})

		waited := make(chan bool, 1)
		go func() { waited <- d.WaitHandlers(time.Second) }()
		<-entered
		synctest.Wait()
		select {
		case joined := <-waited:
			t.Fatalf("WaitHandlers returned while the handler was parked: %v", joined)
		default:
		}

		close(release)
		assert.True(t, <-waited, "the released handler joins inside the budget")
	})
}

// A handler that never returns is abandoned at the deadline, with false.
func TestDispatcherWaitHandlersReturnsFalseAtTheDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := NewDispatcher()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		entered := make(chan struct{})
		d.Handle("stuck", Registration{}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
			close(entered)
			<-release
		})

		d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
			&leapmuxv1.InnerRpcRequest{Method: "stuck"}, &recordingWriter{})
		<-entered

		waited := make(chan bool, 1)
		go func() { waited <- d.WaitHandlers(time.Second) }()

		// The budget is fake time in the bubble, so the deadline arrives
		// without spending a real second.
		assert.False(t, <-waited, "a handler past the deadline is abandoned")
	})
}

func TestDispatcherWaitHandlersWithNoHandlers(t *testing.T) {
	t.Parallel()

	d := NewDispatcher()

	assert.True(t, d.WaitHandlers(time.Second), "no handler runs, so WaitHandlers returns at once")
}

// A dispatch admitted before the close must be counted by the time
// CloseAdmission plus a completed WaitHandlers return, and nothing may start
// after them. This is the Add-after-Wait misuse the gate closes.
func TestDispatcherCloseAdmissionRacesDispatch(t *testing.T) {
	d := NewDispatcher()
	var admitted atomic.Int32
	started := make(chan struct{})
	d.Handle("racy", Registration{Tracked: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
		// Counting at ENTRY is the admitted count: the dispatcher runs a
		// handler only after its own gate admitted the dispatch.
		admitted.Add(1)
		<-started
	})

	cleanup := &sync.WaitGroup{}
	d.BindCleanup(cleanup)
	const n = 32
	writers := make([]*recordingWriter, n)
	var racers sync.WaitGroup
	for i := range n {
		racers.Add(1)
		writers[i] = &recordingWriter{}
		go func(w *recordingWriter) {
			defer racers.Done()
			d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
				&leapmuxv1.InnerRpcRequest{Method: "racy"}, w)
		}(writers[i])
	}
	// racers.Wait makes the race one of CONCURRENT Begin+Add under -race, not
	// of admission-vs-close: DispatchAsync runs its Begin synchronously, so
	// all n dispatches are admitted by the time it returns. The racers' own
	// WaitGroup is separate from the bound cleanup counter on purpose: the
	// counter mixes the handlers' Adds and Dones, and a Wait on it here would
	// wait for handlers that this test releases only below.
	racers.Wait()
	// racers.Wait guarantees every dispatch was ADMITTED (Begin ran
	// synchronously inside DispatchAsync), but the handler goroutines only
	// get scheduled after it: their entry count catches up here, before the
	// close, so the close is what the assertions below observe.
	require.Eventually(t, func() bool { return admitted.Load() == n },
		5*time.Second, 5*time.Millisecond,
		"every racing dispatch is admitted and counted")

	d.CloseAdmission()

	// None may start after CloseAdmission plus a completed WaitHandlers.
	close(started)
	assert.True(t, d.WaitHandlers(5*time.Second), "every admitted handler finishes")
	assert.True(t, d.WaitHandlers(0), "the counter is at zero, so an idle drain returns at once")
	assert.Equal(t, int32(n), admitted.Load(),
		"no handler started after the close")

	late := &recordingWriter{}
	d.DispatchAsync(context.Background(), LocalAgentCaller(userid.MustNew("u")),
		&leapmuxv1.InnerRpcRequest{Method: "racy"}, late)
	assert.Equal(t, int32(1), late.errors.Load(),
		"a dispatch after the close is refused in the writer's shape")
	assert.Equal(t, int32(n), admitted.Load(), "the late dispatch ran no handler")
}

func TestDispatcherInvocationLifecycle(t *testing.T) {
	for _, async := range []bool{false, true} {
		for _, tracked := range []bool{false, true} {
			for _, bound := range []bool{false, true} {
				for _, panics := range []bool{false, true} {
					t.Run(fmt.Sprintf("async=%t/tracked=%t/cleanup=%t/panic=%t", async, tracked, bound, panics), func(t *testing.T) {
						synctest.Test(t, func(t *testing.T) {
							d := NewDispatcher()
							var cleanup sync.WaitGroup
							if bound {
								d.BindCleanup(&cleanup)
							}
							entered := make(chan struct{})
							release := make(chan struct{})
							finish := sync.OnceFunc(func() { close(release) })
							defer finish()
							d.Handle("lifecycle", Registration{Tracked: tracked}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) {
								close(entered)
								<-release
								if panics {
									panic("lifecycle test")
								}
							})
							dispatched := make(chan struct{})
							w := &recordingWriter{}
							go func() {
								defer close(dispatched)
								req := &leapmuxv1.InnerRpcRequest{Method: "lifecycle"}
								caller := LocalAgentCaller(userid.MustNew("u"))
								if async {
									d.DispatchAsync(t.Context(), caller, req, w)
								} else {
									d.DispatchWith(t.Context(), caller, req, w)
								}
							}()
							<-entered
							d.CloseAdmission()
							cleaned := make(chan struct{})
							go func() { cleanup.Wait(); close(cleaned) }()
							joined := make(chan bool, 1)
							go func() { joined <- d.WaitHandlers(time.Second) }()
							synctest.Wait()
							select {
							case <-joined:
								t.Fatal("the handler joined before release")
							default:
							}
							select {
							case <-cleaned:
								assert.False(t, tracked && bound, "tracked work must hold cleanup")
							default:
								assert.True(t, tracked && bound, "untracked work must not hold cleanup")
							}
							finish()
							require.True(t, <-joined)
							<-cleaned
							<-dispatched
							if panics {
								assert.Equal(t, int32(1), w.errors.Load())
							} else {
								assert.Zero(t, w.errors.Load())
							}
							assert.True(t, d.WaitHandlers(0))
						})
					})
				}
			}
		}
	}
}

type preparableRecordingWriter struct {
	recordingWriter
	prepared atomic.Int32
	released atomic.Int32
}

func (w *preparableRecordingWriter) prepareStream() (func(), bool) {
	w.prepared.Add(1)
	return func() { w.released.Add(1) }, true
}

func TestDispatcherClosedAdmissionReleasesPreparedStream(t *testing.T) {
	d := NewDispatcher()
	invoked := false
	d.Handle("stream", Registration{Streaming: true}, func(context.Context, Caller, *leapmuxv1.InnerRpcRequest, ResponseWriter) { invoked = true })
	d.CloseAdmission()
	w := &preparableRecordingWriter{}
	d.DispatchAsync(t.Context(), LocalAgentCaller(userid.MustNew("u")), &leapmuxv1.InnerRpcRequest{Method: "stream"}, w)
	assert.False(t, invoked)
	assert.Equal(t, int32(1), w.prepared.Load())
	assert.Equal(t, int32(1), w.released.Load())
	assert.Equal(t, int32(1), w.streams.Load())
	assert.Zero(t, w.errors.Load())
}
