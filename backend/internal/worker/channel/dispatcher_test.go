package channel

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
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
	d := NewDispatcher()

	var calledWith struct {
		userID string
		method string
	}

	d.Register("test.method", func(_ context.Context, userID string, req *leapmuxv1.InnerRpcRequest, sender *Sender) {
		calledWith.userID = userID
		calledWith.method = req.GetMethod()
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{
			Payload: []byte("ok"),
		})
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	d.Dispatch(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "test.method",
	}, 7, cs)

	assert.Equal(t, "user-1", calledWith.userID)
	assert.Equal(t, "test.method", calledWith.method)

	// Verify response was sent and can be decrypted.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	require.Equal(t, uint32(1), chMsg.GetProtocolVersion())

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.Equal(t, uint32(7), chMsg.GetCorrelationId())
	assert.Equal(t, []byte("ok"), resp.GetPayload())
}

func TestDispatcher_PanicRecovery(t *testing.T) {
	d := NewDispatcher()

	d.Register("panicking", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {
		panic("test panic in handler")
	})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	// Dispatch should not panic — the panic should be recovered and
	// an INTERNAL error response should be sent instead.
	require.NotPanics(t, func() {
		d.Dispatch(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
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
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(13), resp.GetErrorCode()) // INTERNAL
	assert.Contains(t, resp.GetErrorMessage(), "internal error")
}

func TestDispatcher_UnknownMethod(t *testing.T) {
	d := NewDispatcher()
	d.Register("known", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {})

	workerSession, initiatorSession := setupTestSessions(t)

	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	d.Dispatch(context.Background(), "user-1", &leapmuxv1.InnerRpcRequest{
		Method: "unknown",
	}, 1, cs)

	// Should get an UNIMPLEMENTED error.
	msgs := sender.messages()
	require.Len(t, msgs, 1)

	chMsg := msgs[0].GetChannelMessageResp()
	require.NotNil(t, chMsg)
	require.Equal(t, uint32(1), chMsg.GetProtocolVersion())

	respPt, err := initiatorSession.Decrypt(chMsg.GetCiphertext())
	require.NoError(t, err)

	var envelope leapmuxv1.InnerMessage
	require.NoError(t, proto.Unmarshal(respPt, &envelope))
	respKind, ok := envelope.GetKind().(*leapmuxv1.InnerMessage_Response)
	require.True(t, ok, "expected InnerMessage_Response, got %T", envelope.GetKind())
	resp := respKind.Response
	assert.True(t, resp.GetIsError())
	assert.Equal(t, int32(12), resp.GetErrorCode())
	assert.Contains(t, resp.GetErrorMessage(), "unknown")
}

// TestDispatcher_CtxPropagated pins the dispatcher → handler ctx hand-off
// that the per-session cancellation depends on: a cancelled inbound ctx
// must be observable inside the handler so the handler's
// `exec.CommandContext` (and other ctx-aware ops) cancel as soon as the
// channel session is torn down. Without this, the gitReadTimeout cap is
// the *only* defense against a wedged subprocess, and we'd revert to the
// pre-refactor "client disposal can't reach the worker" behavior.
func TestDispatcher_CtxPropagated(t *testing.T) {
	d := NewDispatcher()

	gotCtxC := make(chan context.Context, 1)
	d.Register("inspect-ctx", func(ctx context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, sender *Sender) {
		gotCtxC <- ctx
		_ = sender.SendResponse(&leapmuxv1.InnerRpcResponse{})
	})

	workerSession, _ := setupTestSessions(t)
	sender := newCollectSender()
	cs := &channelSender{
		channelID:      "test-ch",
		session:        workerSession,
		sendFn:         sender.send,
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	parent, cancel := context.WithCancel(context.Background())
	d.Dispatch(parent, "user-1", &leapmuxv1.InnerRpcRequest{Method: "inspect-ctx"}, 99, cs)

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
	d := NewDispatcher()

	seenC := make(chan bool, 1)
	d.Register("watch", func(ctx context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, sender *Sender) {
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
		maxMessageSize: channelwire.DefaultMaxMessageSize,
	}

	parent, cancel := context.WithCancel(context.Background())
	cancel()
	d.Dispatch(parent, "user-1", &leapmuxv1.InnerRpcRequest{Method: "watch"}, 1, cs)

	require.True(t, <-seenC, "handler must see ctx.Done() synchronously when parent is pre-cancelled")
}

// stubWriter is a minimal ResponseWriter for DispatchAsync tests that
// don't care about encrypted round-trips. Captures send calls so a
// future assertion can verify response shape if needed; today the
// tests below only need the writer to satisfy the interface.
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

func (*stubWriter) ChannelID() string { return "" }

// TestDispatcher_DispatchAsync_AddHappensBeforeGoroutine pins the
// happens-before invariant that motivated the RegisterTracked /
// BindCleanup / DispatchAsync trio. Add(1) MUST execute before
// DispatchAsync returns, so a caller that immediately calls Wait() on
// the bound WaitGroup is forced to wait for the handler to finish.
// The previous pattern (`go DispatchWith(...)` + per-handler Add(1))
// failed this invariant: Wait could observe counter=0 in the window
// between the goroutine launching and the handler reaching its
// Cleanup.Add(1).
func TestDispatcher_DispatchAsync_AddHappensBeforeGoroutine(t *testing.T) {
	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	// Block the handler on a channel so we can observe the
	// pre-completion WaitGroup state from outside.
	release := make(chan struct{})
	handlerEntered := make(chan struct{})
	d.RegisterTracked("slow-mutation", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {
		close(handlerEntered)
		<-release
	})

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), "u", &leapmuxv1.InnerRpcRequest{Method: "slow-mutation"}, w)

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
// methods don't touch the bound WaitGroup, so plain Register stays
// fire-and-forget on Shutdown for read-only probes.
func TestDispatcher_DispatchAsync_UntrackedNoOp(t *testing.T) {
	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	done := make(chan struct{})
	d.Register("readonly", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {
		close(done)
	})

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), "u", &leapmuxv1.InnerRpcRequest{Method: "readonly"}, w)

	// wg.Wait() must return immediately — Register (untracked) does
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
	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	d.RegisterTracked("ok", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {})
	d.RegisterTracked("boom", func(_ context.Context, _ string, _ *leapmuxv1.InnerRpcRequest, _ *Sender) {
		panic("handler panic")
	})

	w := &stubWriter{}
	d.DispatchWith(context.Background(), "u", &leapmuxv1.InnerRpcRequest{Method: "ok"}, w)
	d.DispatchWith(context.Background(), "u", &leapmuxv1.InnerRpcRequest{Method: "boom"}, w)

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
	d := NewDispatcher()
	var wg sync.WaitGroup
	d.BindCleanup(&wg)

	w := &stubWriter{}
	d.DispatchAsync(context.Background(), "u", &leapmuxv1.InnerRpcRequest{Method: "no-such-method"}, w)

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
