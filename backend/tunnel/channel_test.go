package tunnel

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"github.com/leapmux/leapmux/internal/tunnelflow"
)

type rollbackChannelService struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler

	publicKey []byte

	mu            sync.Mutex
	closedChannel string
	closeAuth     string
	paramsAuth    string
	openAuth      string
}

func (s *rollbackChannelService) GetWorkerHandshakeParams(
	_ context.Context,
	req *connect.Request[leapmuxv1.GetWorkerHandshakeParamsRequest],
) (*connect.Response[leapmuxv1.GetWorkerHandshakeParamsResponse], error) {
	s.mu.Lock()
	s.paramsAuth = req.Header().Get("Authorization")
	s.mu.Unlock()
	return connect.NewResponse(&leapmuxv1.GetWorkerHandshakeParamsResponse{
		PublicKey:      s.publicKey,
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}), nil
}

func (s *rollbackChannelService) OpenChannel(
	_ context.Context,
	req *connect.Request[leapmuxv1.OpenChannelRequest],
) (*connect.Response[leapmuxv1.OpenChannelResponse], error) {
	s.mu.Lock()
	s.openAuth = req.Header().Get("Authorization")
	s.mu.Unlock()
	return connect.NewResponse(&leapmuxv1.OpenChannelResponse{
		ChannelId:        "registered-channel",
		HandshakePayload: []byte("invalid-handshake-response"),
		UserId:           "authenticated-user",
	}), nil
}

func (s *rollbackChannelService) CloseChannel(
	_ context.Context,
	req *connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	s.mu.Lock()
	s.closedChannel = req.Msg.GetChannelId()
	s.closeAuth = req.Header().Get("Authorization")
	s.mu.Unlock()
	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

// Both halves of an initiator handshake must always come from the SAME encryption
// mode.
//
// noiseutil hands both modes one *HandshakeState type, so a message 1 started
// classically and finished with the hybrid reader (or the reverse) compiles cleanly
// and fails only as a decrypt error at the far end -- which is why the pair is
// selected once, together, rather than re-branched at each message. Round-tripping
// each mode against its own responder is what pins that: it fails if the two halves
// ever come from different modes.
func TestNewInitiatorHandshakerPairsBothHalvesToOneMode(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	slhdsaPub, err := key.SlhdsaPublicKeyBytes()
	require.NoError(t, err)

	classicResponder := func(msg1 []byte) ([]byte, *noiseutil.Session, error) {
		return noiseutil.ClassicalResponderHandshake(key.X25519Public, key.X25519Private, msg1)
	}
	pqResponder := func(msg1 []byte) ([]byte, *noiseutil.Session, error) {
		return noiseutil.ResponderHandshake(key, msg1)
	}

	for name, tc := range map[string]struct {
		mode    leapmuxv1.EncryptionMode
		respond func([]byte) ([]byte, *noiseutil.Session, error)
	}{
		"classic": {
			mode:    leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
			respond: classicResponder,
		},
		"post-quantum": {
			mode:    leapmuxv1.EncryptionMode_ENCRYPTION_MODE_POST_QUANTUM,
			respond: pqResponder,
		},
		// An unknown mode must take the STRONGER handshake, never downgrade to
		// X25519-only. UNSPECIFIED is the one that reaches here in practice --
		// ChannelService.GetWorkerHandshakeParams normalises it to POST_QUANTUM, and
		// this end must agree with that reading rather than pick the classic pair.
		"unspecified is post-quantum, not a downgrade": {
			mode:    leapmuxv1.EncryptionMode_ENCRYPTION_MODE_UNSPECIFIED,
			respond: pqResponder,
		},
	} {
		t.Run(name, func(t *testing.T) {
			handshaker := newInitiatorHandshaker(&leapmuxv1.GetWorkerHandshakeParamsResponse{
				PublicKey:       key.X25519Public,
				MlkemPublicKey:  key.MlkemPublicKeyBytes(),
				SlhdsaPublicKey: slhdsaPub,
				EncryptionMode:  tc.mode,
			})

			hs, msg1, err := handshaker.start()
			require.NoError(t, err)
			msg2, workerSession, err := tc.respond(msg1)
			require.NoError(t, err, "the responder for this mode must accept the message 1 start produced")

			session, err := handshaker.finish(hs, msg2)
			require.NoError(t, err, "finish must be the half matching the message 1 start sent")

			// Decrypt, not just a nil error: a mismatched pair is only observable by
			// running the session, which is exactly why the mode is read once.
			ciphertext, err := session.Encrypt([]byte("ping"))
			require.NoError(t, err)
			plaintext, err := workerSession.Decrypt(ciphertext)
			require.NoError(t, err, "the paired halves must agree on the session keys")
			assert.Equal(t, "ping", string(plaintext))
		})
	}
}

func TestOpenChannelRollsBackRegisteredChannelAfterHandshakeFailure(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	service := &rollbackChannelService{publicKey: key.X25519Public}
	path, handler := leapmuxv1connect.NewChannelServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	require.NotEmpty(t, path)

	_, err = OpenChannel(context.Background(), server.URL, "worker-1", &OpenChannelOptions{
		LifetimeContext: context.Background(),
		BearerToken:     "delegation-token",
	})
	require.ErrorContains(t, err, "handshake2")

	service.mu.Lock()
	defer service.mu.Unlock()
	assert.Equal(t, "registered-channel", service.closedChannel)
	assert.Equal(t, "Bearer delegation-token", service.closeAuth)
}

// EVERY Hub call an open makes must carry the caller's credential.
//
// The open touches the Hub four times -- handshake params, open, the /ws/channel
// upgrade, and the rollback close -- and each used to spell the header by hand. An
// omission does not fail at the call site: the request authenticates as nobody and
// comes back as a permission error from the Hub, which reads like an authorization
// bug rather than a missing header. This pins the three connect calls (the WS
// upgrade is covered by the integration test, which dials a real relay).
func TestOpenChannelSendsBearerOnEveryHubCall(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	service := &rollbackChannelService{publicKey: key.X25519Public}
	_, handler := leapmuxv1connect.NewChannelServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	// Fails at handshake2 (the fake returns a bogus payload), which is AFTER all
	// three connect calls have been made -- including the rollback close.
	_, err = OpenChannel(context.Background(), server.URL, "worker-1", &OpenChannelOptions{
		LifetimeContext: context.Background(),
		BearerToken:     "delegation-token",
	})
	require.ErrorContains(t, err, "handshake2")

	service.mu.Lock()
	defer service.mu.Unlock()
	assert.Equal(t, "Bearer delegation-token", service.paramsAuth,
		"GetWorkerHandshakeParams must carry the credential")
	assert.Equal(t, "Bearer delegation-token", service.openAuth,
		"OpenChannel must carry the credential")
	assert.Equal(t, "Bearer delegation-token", service.closeAuth,
		"the rollback CloseChannel must carry the credential")
}

// A caller that declares who it expects to be must not get a channel the Hub
// authenticated as someone else.
//
// The Hub's answer is authoritative and this never overrides it -- the open FAILS
// instead. What it catches is a silent disagreement: a caller that pools channels
// by identity (crossworker.Client keys on {worker, user, workspace}) would otherwise
// cache a channel authenticated as X under the key for Y, and every later call on it
// would run as X with nothing in the stack able to notice. The fake below
// authenticates every open as "authenticated-user".
func TestOpenChannelRejectsIdentityMismatch(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	service := &rollbackChannelService{publicKey: key.X25519Public}
	_, handler := leapmuxv1connect.NewChannelServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, err = OpenChannel(context.Background(), server.URL, "worker-1", &OpenChannelOptions{
		LifetimeContext: context.Background(),
		ExpectedUserID:  "someone-else",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authenticated this channel as",
		"a channel the Hub authenticated as another user must not be handed out")

	// The registered channel is rolled back rather than stranded on the Worker.
	service.mu.Lock()
	defer service.mu.Unlock()
	assert.Equal(t, "registered-channel", service.closedChannel,
		"an open refused on identity must still roll back the Hub-registered channel")
}

// The matching case must proceed -- and the channel must report the Hub's identity,
// so a caller can reconcile against what the channel IS rather than what it asked
// for. (The open still fails at handshake2 against this fake; reaching that proves
// the identity check passed.)
func TestOpenChannelAcceptsMatchingExpectedUserID(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	service := &rollbackChannelService{publicKey: key.X25519Public}
	_, handler := leapmuxv1connect.NewChannelServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, err = OpenChannel(context.Background(), server.URL, "worker-1", &OpenChannelOptions{
		LifetimeContext: context.Background(),
		ExpectedUserID:  "authenticated-user",
	})
	require.ErrorContains(t, err, "handshake2",
		"a matching identity must pass the check and proceed to the handshake")
}

// A client with no credential must send no Authorization header at all, rather than
// an empty or malformed one -- solo/local callers are unauthenticated by design.
func TestOpenChannelOmitsAuthHeaderWithoutBearer(t *testing.T) {
	key, err := noiseutil.GenerateCompositeKeypair()
	require.NoError(t, err)
	service := &rollbackChannelService{publicKey: key.X25519Public}
	_, handler := leapmuxv1connect.NewChannelServiceHandler(service)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	_, err = OpenChannel(context.Background(), server.URL, "worker-1", &OpenChannelOptions{
		LifetimeContext: context.Background(),
	})
	require.ErrorContains(t, err, "handshake2")

	service.mu.Lock()
	defer service.mu.Unlock()
	assert.Empty(t, service.paramsAuth, "no bearer must mean no Authorization header")
	assert.Empty(t, service.openAuth, "no bearer must mean no Authorization header")
	assert.Empty(t, service.closeAuth, "no bearer must mean no Authorization header")
}

func TestOpenChannelRequiresExplicitLifetime(t *testing.T) {
	_, err := OpenChannel(context.Background(), "http://hub.invalid", "worker-1", nil)
	assert.ErrorContains(t, err, "lifetime context is required")
}

func TestDialTunnelRejectsNilChannel(t *testing.T) {
	require.NotPanics(t, func() {
		_, err := DialTunnel(nil, "example.test", 443)
		assert.ErrorContains(t, err, "channel is required")
	})
}

type canceledOpenChannel struct {
	ctx           context.Context
	cancel        context.CancelFunc
	closeRequests chan string
}

func (c *canceledOpenChannel) Context() context.Context { return c.ctx }
func (*canceledOpenChannel) UnregisterPending(uint64)   {}
func (*canceledOpenChannel) UnregisterStream(uint64)    {}
func (c *canceledOpenChannel) SendRPCNoWait(
	_ context.Context,
	method string,
	payload []byte,
	handlers RPCHandlers,
) (uint64, error) {
	if method != "OpenTunnelConn" {
		var request leapmuxv1.CloseTunnelConnRequest
		if err := proto.Unmarshal(payload, &request); err != nil {
			return 0, err
		}
		c.closeRequests <- request.GetConnId()
		return 2, nil
	}
	var request leapmuxv1.OpenTunnelConnRequest
	if err := proto.Unmarshal(payload, &request); err != nil {
		return 0, err
	}
	responsePayload, err := proto.Marshal(&leapmuxv1.OpenTunnelConnResponse{ConnId: request.GetConnId()})
	if err != nil {
		return 0, err
	}
	handlers.Response <- &leapmuxv1.InnerRpcResponse{Payload: responsePayload}
	c.cancel()
	return 1, nil
}

func TestDialTunnelDoesNotReturnConnectionAfterOperationCancellation(t *testing.T) {
	for range 32 {
		operationCtx, cancelOperation := context.WithCancel(context.Background())
		channelCtx, cancelChannel := context.WithCancel(context.Background())
		channel := &canceledOpenChannel{
			ctx:           channelCtx,
			cancel:        cancelOperation,
			closeRequests: make(chan string, 1),
		}

		conn, err := dialTunnelContext(operationCtx, channel, "example.test", 443)
		if conn != nil {
			_ = conn.Close()
		}
		cancelChannel()
		assert.Nil(t, conn)
		assert.ErrorIs(t, err, context.Canceled)
		select {
		case connID := <-channel.closeRequests:
			assert.NotEmpty(t, connID)
		case <-time.After(time.Second):
			t.Fatal("canceled open did not issue a remote close")
		}
	}
}

type recordingChannel struct {
	ctx  context.Context
	mu   sync.Mutex
	sent []*leapmuxv1.SendTunnelDataRequest
}

func (c *recordingChannel) Context() context.Context { return c.ctx }
func (*recordingChannel) UnregisterPending(uint64)   {}
func (*recordingChannel) UnregisterStream(uint64)    {}
func (c *recordingChannel) SendRPCNoWait(_ context.Context, method string, payload []byte, _ RPCHandlers) (uint64, error) {
	if method == "SendTunnelData" {
		var r leapmuxv1.SendTunnelDataRequest
		if err := proto.Unmarshal(payload, &r); err != nil {
			return 0, err
		}
		c.mu.Lock()
		c.sent = append(c.sent, &r)
		c.mu.Unlock()
	}
	return 1, nil
}

// CloseWrite must forward a single half-close signal (SendTunnelData with
// CloseWrite=true and no data) so the desktop port-forward/SOCKS5 copy can
// propagate a client's write-half-close to the worker's target; repeated calls
// are no-ops, matching *net.TCPConn.CloseWrite.
func TestConnCloseWriteSendsHalfCloseFlag(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	require.NoError(t, tc.CloseWrite())
	require.NoError(t, tc.CloseWrite(), "CloseWrite must be idempotent")

	ch.mu.Lock()
	defer ch.mu.Unlock()
	require.Len(t, ch.sent, 1, "CloseWrite must send exactly one half-close frame")
	assert.True(t, ch.sent[0].GetCloseWrite(), "the frame must carry the close_write flag")
	assert.Equal(t, "conn-1", ch.sent[0].GetConnId())
	assert.Empty(t, ch.sent[0].GetData(), "a half-close frame carries no data")
}

// net.Conn.Write accepts a buffer of ANY size, so Write must split a large one
// across frames rather than hand the whole buffer to a single SendTunnelData.
// Framing the caller's buffer verbatim made the in-flight byte ceiling a property
// of the caller (fine for today's 32 KiB io.Copy, but a bufio.Writer or
// bytes.Buffer.WriteTo passes megabytes in one call), pinning up to
// tunnelflow.WriteWindowFrames * DefaultMaxMessageSize on the worker -- and a buffer
// past the channel's inner-message limit failed outright instead of chunking.
func TestConnWriteSplitsLargeBufferIntoWindowedChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	// Larger than one chunk and not a multiple of it, so the ragged tail is covered.
	payload := make([]byte, tunnelflow.MaxChunkBytes*2+7)
	for i := range payload {
		payload[i] = byte(i)
	}

	n, err := tc.Write(payload)
	require.NoError(t, err, "a large Write must chunk, not fail")
	require.Equal(t, len(payload), n, "Write reports every byte it accepted")

	ch.mu.Lock()
	defer ch.mu.Unlock()
	require.Len(t, ch.sent, 3, "the buffer splits into ceil(len/tunnelflow.MaxChunkBytes) frames")

	var reassembled []byte
	for i, frame := range ch.sent {
		require.LessOrEqual(t, len(frame.GetData()), tunnelflow.MaxChunkBytes,
			"no frame exceeds the chunk bound the send window is denominated in")
		assert.Equal(t, uint64(i), frame.GetSeq(), "chunks carry contiguous sequences")
		assert.False(t, frame.GetCloseWrite(), "a data chunk is not a half-close")
		reassembled = append(reassembled, frame.GetData()...)
	}
	assert.Equal(t, payload, reassembled, "the chunks reassemble to the original buffer in order")
}

// failThenRecordChannel fails the first n SendTunnelData sends, then records the
// rest -- standing in for a transient send failure (an expired write deadline, a
// momentarily-full send window) that a caller retries.
type failThenRecordChannel struct {
	ctx        context.Context
	mu         sync.Mutex
	failsLeft  int
	sent       []*leapmuxv1.SendTunnelDataRequest
	sendErrMsg string
}

func (c *failThenRecordChannel) Context() context.Context { return c.ctx }
func (*failThenRecordChannel) UnregisterPending(uint64)   {}
func (*failThenRecordChannel) UnregisterStream(uint64)    {}
func (c *failThenRecordChannel) SendRPCNoWait(_ context.Context, method string, payload []byte, _ RPCHandlers) (uint64, error) {
	if method != "SendTunnelData" {
		return 1, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failsLeft > 0 {
		c.failsLeft--
		return 0, errors.New(c.sendErrMsg)
	}
	var r leapmuxv1.SendTunnelDataRequest
	if err := proto.Unmarshal(payload, &r); err != nil {
		return 0, err
	}
	c.sent = append(c.sent, &r)
	return 1, nil
}

func (c *failThenRecordChannel) frames() []*leapmuxv1.SendTunnelDataRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*leapmuxv1.SendTunnelDataRequest(nil), c.sent...)
}

// A CloseWrite whose send FAILS must report that failure and leave the half-close
// unsent-but-retryable: it must not consume its once-only latch. Latching on
// entry (as a sync.Once does, which marks the once consumed even when its body
// fails) made a retry after a transient failure return nil while no close_write
// frame was ever emitted -- so the target never saw the read-EOF and a
// request/response peer (HTTP/1.0, SSH, `nc -N`) hung forever waiting for a
// response it would never send.
func TestConnCloseWriteRetriesAfterFailedSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &failThenRecordChannel{ctx: ctx, failsLeft: 1, sendErrMsg: "transient send failure"}
	tc := newConn(ch, "conn-1", "example.test", 443)

	require.Error(t, tc.CloseWrite(), "a failed send must surface to the caller")
	require.Empty(t, ch.frames(), "the failed CloseWrite put no frame on the wire")

	require.NoError(t, tc.CloseWrite(), "a retry after a transient failure must send the half-close")
	frames := ch.frames()
	require.Len(t, frames, 1, "the retry emits exactly one half-close frame")
	assert.True(t, frames[0].GetCloseWrite(), "the retried frame carries the close_write flag")

	require.NoError(t, tc.CloseWrite(), "CloseWrite is idempotent once the frame is on the wire")
	assert.Len(t, ch.frames(), 1, "a successful half-close is never re-sent")
}

// CloseWrite on an already-closed conn returns net.ErrClosed without putting a
// frame on the wire -- the closed-check gates entry before sendFrameLocked would
// acquire a window slot, matching *net.TCPConn.CloseWrite on a closed conn.
func TestConnCloseWriteAfterCloseReturnsErrClosed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)
	close(tc.closed) // simulate Close() without driving a real remote-close RPC

	require.ErrorIs(t, tc.CloseWrite(), net.ErrClosed)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.Empty(t, ch.sent, "a closed conn must not send a half-close frame")
}

// seqRecordingChannel is a tunnelRPCChannel fake that fails the first failSends
// SendTunnelData sends (simulating a per-request send failure on a still-alive
// channel) and records every later one, so tests can assert the per-conn write
// sequence the worker observes stays contiguous across a failed send.
type seqRecordingChannel struct {
	ctx       context.Context
	mu        sync.Mutex
	failSends int
	sent      []*leapmuxv1.SendTunnelDataRequest
}

func (c *seqRecordingChannel) Context() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}
func (*seqRecordingChannel) UnregisterPending(uint64) {}
func (*seqRecordingChannel) UnregisterStream(uint64)  {}
func (c *seqRecordingChannel) SendRPCNoWait(_ context.Context, method string, payload []byte, handlers RPCHandlers) (uint64, error) {
	if method != "SendTunnelData" {
		return 1, nil
	}
	c.mu.Lock()
	if c.failSends > 0 {
		c.failSends--
		c.mu.Unlock()
		return 0, errors.New("simulated send failure")
	}
	var r leapmuxv1.SendTunnelDataRequest
	if err := proto.Unmarshal(payload, &r); err != nil {
		c.mu.Unlock()
		return 0, err
	}
	c.sent = append(c.sent, &r)
	c.mu.Unlock()
	// Simulate the worker's per-write ACK so the client's send-window releases
	// (respCh is buffered, so this never blocks the caller).
	if handlers.Response != nil {
		handlers.Response <- &leapmuxv1.InnerRpcResponse{}
	}
	return 1, nil
}

// windowTestChannel captures each SendTunnelData's ACK channel so a test can
// release send-window slots on demand, exercising the write flow-control gate.
type windowTestChannel struct {
	ctx  context.Context
	mu   sync.Mutex
	acks []chan<- *leapmuxv1.InnerRpcResponse
}

func (c *windowTestChannel) Context() context.Context { return c.ctx }
func (*windowTestChannel) UnregisterPending(uint64)   {}
func (*windowTestChannel) UnregisterStream(uint64)    {}
func (c *windowTestChannel) SendRPCNoWait(_ context.Context, method string, _ []byte, handlers RPCHandlers) (uint64, error) {
	if method == "SendTunnelData" && handlers.Response != nil {
		c.mu.Lock()
		c.acks = append(c.acks, handlers.Response)
		c.mu.Unlock()
	}
	return 1, nil
}

// ackOne delivers the ACK for the oldest outstanding send, freeing one window
// slot. Returns false when nothing is outstanding.
func (c *windowTestChannel) ackOne() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.acks) == 0 {
		return false
	}
	ack := c.acks[0]
	c.acks = c.acks[1:]
	ack <- &leapmuxv1.InnerRpcResponse{}
	return true
}

func (c *windowTestChannel) pendingAcks() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.acks)
}

// nakOne delivers an error ACK for the oldest outstanding send -- the worker's
// NAK when its write to the target failed -- freeing one window slot. Returns
// false when nothing is outstanding.
func (c *windowTestChannel) nakOne(code int32, message string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.acks) == 0 {
		return false
	}
	ack := c.acks[0]
	c.acks = c.acks[1:]
	ack <- &leapmuxv1.InnerRpcResponse{IsError: true, ErrorCode: code, ErrorMessage: message}
	return true
}

// Write must block once tunnelflow.WriteWindowFrames frames are unacknowledged (end-to-end
// write flow control), and resume as soon as the worker acks one -- so a slow
// target backpressures the client instead of letting the worker buffer without
// bound.
func TestConnWriteBlocksWhenSendWindowFull(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	for range tunnelflow.WriteWindowFrames {
		n, err := tc.Write([]byte("x"))
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	require.Equal(t, tunnelflow.WriteWindowFrames, ch.pendingAcks(), "the full window is outstanding")

	writeDone := make(chan error, 1)
	go func() {
		_, err := tc.Write([]byte("blocked"))
		writeDone <- err
	}()
	select {
	case <-writeDone:
		t.Fatal("Write returned even though the send window was full")
	case <-time.After(50 * time.Millisecond):
	}

	require.True(t, ch.ackOne(), "one outstanding frame is acked to free a slot")
	select {
	case err := <-writeDone:
		require.NoError(t, err, "Write proceeds once a window slot is freed")
	case <-time.After(time.Second):
		t.Fatal("Write did not resume after a window slot was freed")
	}
}

// A worker NAK for a SendTunnelData frame (its write to the target failed) must
// surface on a subsequent Write and CloseWrite -- the way TCP surfaces EPIPE on
// a later write -- instead of Write reporting success for bytes the worker
// silently drops. The first frame still reaches the wire (its NAK arrives
// asynchronously), so it is the LATER write that fails, matching TCP.
func TestConnWriteSurfacesTargetWriteError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	n, err := tc.Write([]byte("x"))
	require.NoError(t, err, "the frame reaches the wire; the worker NAK arrives asynchronously")
	require.Equal(t, 1, n)
	require.True(t, ch.nakOne(13, "write: broken pipe"), "the worker NAKs the outstanding frame")

	// awaitWriteAck latches the error on its own goroutine; wait for it, then the
	// next Write / CloseWrite must observe it and fail.
	require.Eventually(t, func() bool { return tc.writeErr.Err() != nil }, time.Second, time.Millisecond,
		"the NAK must latch a terminal write error")

	_, err = tc.Write([]byte("y"))
	require.Error(t, err, "a Write after a NAK must fail instead of silently dropping")
	assert.Contains(t, err.Error(), "broken pipe")

	assert.ErrorContains(t, tc.CloseWrite(), "broken pipe",
		"CloseWrite must surface the broken target rather than send a doomed frame")
}

// A worker NAK for the close_write frame ITSELF (the target's write-half-close
// failed) must latch a terminal write error, exactly as a data-frame NAK does.
// The half-close rides the same send-window + ack path as data (it occupies a
// window slot and registers an ack handler), so the worker's error is surfaced
// on a subsequent write the way TCP surfaces EPIPE rather than being dropped and
// the FIN reported to the caller as delivered.
func TestConnCloseWriteSurfacesTargetCloseError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	require.NoError(t, tc.CloseWrite(), "the half-close frame reaches the wire; the NAK arrives asynchronously")
	require.Equal(t, 1, ch.pendingAcks(),
		"CloseWrite must register an ack handler, so it occupies a send-window slot like data")
	require.True(t, ch.nakOne(13, "close_write: broken pipe"), "the worker NAKs the half-close")

	require.Eventually(t, func() bool { return tc.writeErr.Err() != nil }, time.Second, time.Millisecond,
		"a NAK on the half-close must latch a terminal write error")
	assert.ErrorContains(t, tc.writeErr.Err(), "broken pipe",
		"the latched error must carry the worker's reason, not a generic one")

	// The write half is closed either way, so a subsequent Write fails with
	// net.ErrClosed (what *net.TCPConn returns for a write after CloseWrite)
	// rather than the NAK -- what matters is that it does not silently drop.
	_, err := tc.Write([]byte("y"))
	assert.ErrorIs(t, err, net.ErrClosed,
		"a write after the half-close must fail instead of silently dropping")
}

// Close must unblock a Write parked on a full send window rather than leaking
// the goroutine until the channel dies.
func TestConnWriteWindowUnblocksOnClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)
	for range tunnelflow.WriteWindowFrames {
		_, err := tc.Write([]byte("x"))
		require.NoError(t, err)
	}

	writeDone := make(chan error, 1)
	go func() {
		_, err := tc.Write([]byte("blocked"))
		writeDone <- err
	}()
	select {
	case <-writeDone:
		t.Fatal("Write returned even though the send window was full")
	case <-time.After(50 * time.Millisecond):
	}

	// Prevent the fixture from attempting a real encrypted close RPC.
	close(tc.closed)
	select {
	case err := <-writeDone:
		require.ErrorIs(t, err, net.ErrClosed)
	case <-time.After(time.Second):
		t.Fatal("Close did not unblock the window-parked Write")
	}
}

// A Write whose send fails while the channel stays alive must NOT advance the
// per-conn write sequence: the worker's exact-turn gate (waitWriteTurn) would
// otherwise wait forever for the never-delivered seq, stranding every later
// write/close for the conn. The next Write reuses the seq, keeping the sequence
// the worker sees contiguous.
func TestConnWriteDoesNotConsumeSeqOnSendFailure(t *testing.T) {
	ch := &seqRecordingChannel{failSends: 1}
	tc := newConn(ch, "conn-1", "example.test", 443)

	_, err := tc.Write([]byte("dropped"))
	require.Error(t, err, "the simulated send failure must surface to the caller")

	n, err := tc.Write([]byte("delivered"))
	require.NoError(t, err)
	assert.Equal(t, len("delivered"), n)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	require.Len(t, ch.sent, 1, "only the successful write reaches the wire")
	assert.Equal(t, uint64(0), ch.sent[0].GetSeq(),
		"the dropped write must not consume seq 0, so the delivered write reuses it")
	assert.Equal(t, "delivered", string(ch.sent[0].GetData()))
}

// CloseWrite shares Write's per-conn sequence, so after a failed data Write the
// half-close reuses the seq the failed write left unconsumed -- a hole here
// would strand the worker's graceful-close flush too.
func TestConnCloseWriteReusesSeqAfterFailedWrite(t *testing.T) {
	ch := &seqRecordingChannel{failSends: 1}
	tc := newConn(ch, "conn-1", "example.test", 443)

	_, err := tc.Write([]byte("dropped"))
	require.Error(t, err)

	require.NoError(t, tc.CloseWrite())

	ch.mu.Lock()
	defer ch.mu.Unlock()
	require.Len(t, ch.sent, 1)
	assert.True(t, ch.sent[0].GetCloseWrite(), "the recorded frame is the half-close")
	assert.Equal(t, uint64(0), ch.sent[0].GetSeq(),
		"the half-close reuses the seq the failed write did not consume")
}

// A shared-transport write failure cancels ch.ctx (ch.cancel) before recvLoop's
// deferred Close sets `closed`. Closed() must report the channel dead in that
// window so a pooled/cached caller re-resolves a fresh channel instead of
// dialing through one whose next SendRPCNoWait fails "channel closed".
func TestChannelClosedReflectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := &Channel{ctx: ctx, cancel: cancel}
	require.False(t, ch.Closed(), "a live channel is not closed")
	cancel()
	assert.True(t, ch.Closed(),
		"a cancelled lifetime context makes the channel unusable, so Closed() must be true")
}

// creditRecordingChannel records the credit carried by each GrantTunnelReadCredit
// the client sends, so a test can assert Read replenishes the worker's read
// window as it drains readBuf.
type creditRecordingChannel struct {
	ctx context.Context
	// blockSend, when non-nil, wedges every send until it is closed -- standing in
	// for a stalled shared send permit or WebSocket write.
	blockSend chan struct{}
	mu        sync.Mutex
	grants    []uint64
}

func (c *creditRecordingChannel) Context() context.Context { return c.ctx }
func (*creditRecordingChannel) UnregisterPending(uint64)   {}
func (*creditRecordingChannel) UnregisterStream(uint64)    {}
func (c *creditRecordingChannel) SendRPCNoWait(ctx context.Context, method string, payload []byte, _ RPCHandlers) (uint64, error) {
	if c.blockSend != nil {
		select {
		case <-c.blockSend:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	}
	if method == "GrantTunnelReadCredit" {
		var r leapmuxv1.GrantTunnelReadCreditRequest
		if err := proto.Unmarshal(payload, &r); err == nil {
			c.mu.Lock()
			c.grants = append(c.grants, r.GetCredit())
			c.mu.Unlock()
		}
	}
	return 1, nil
}

func (c *creditRecordingChannel) totalGranted() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total uint64
	for _, g := range c.grants {
		total += g
	}
	return total
}

// The client must enforce the per-frame chunk bound on INBOUND data itself, not
// trust the worker to have chunked -- the mirror of the worker's own
// TestSendTunnelData_RejectsOversizeFrame.
//
// The worker frames at its 32 KiB read buffer, which is what makes InitialReadWindow
// a BYTE bound (InitialReadWindow * MaxChunkBytes). But that is the SENDER's
// convention. A worker that does not chunk is otherwise capped only by the channel's
// inner-message limit, and readBuf holds ReadBufFrames (256) of them: an
// unchecked client admits over 4 GiB pinned per conn, against a 4 MiB
// design target. The bound has to hold at BOTH receivers or it is not a property of
// the protocol, only of the peer's good manners.
func TestConnRejectsOversizeInboundFrame(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &creditRecordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
		Data: make([]byte, tunnelflow.MaxChunkBytes+1),
	})})

	buf := make([]byte, 8)
	_, err := tc.Read(buf)
	require.Error(t, err, "an oversize inbound frame must fail the conn, not be buffered")
	assert.Contains(t, err.Error(), "chunk bound")
}

// A frame exactly AT the bound is legitimate and must still be delivered: the check
// is > not >=, so the boundary the worker is allowed to send is not rejected.
func TestConnAcceptsInboundFrameAtChunkBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &creditRecordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
		Data: make([]byte, tunnelflow.MaxChunkBytes),
	})})

	buf := make([]byte, tunnelflow.MaxChunkBytes)
	n, err := tc.Read(buf)
	require.NoError(t, err, "a frame exactly at the chunk bound is legitimate")
	assert.Equal(t, tunnelflow.MaxChunkBytes, n)
}

// Read must replenish the worker's read-send window as it drains readBuf: after
// consuming a full tunnelflow.ReadCreditBatch of frames the client sends one batched
// GrantTunnelReadCredit covering them, so the worker can send more inbound data.
func TestConnReadGrantsReadCreditAsItConsumes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &creditRecordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	for range tunnelflow.ReadCreditBatch {
		tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
			Data: []byte("x"),
		})})
	}
	assert.Zero(t, ch.totalGranted(), "no credit is granted before frames are consumed")

	buf := make([]byte, 8)
	for range tunnelflow.ReadCreditBatch {
		n, err := tc.Read(buf)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	}
	// The grant is sent by creditLoop, not inline by Read: Read must never park on
	// the shared send path (see grantReadCredit), so the batch replenishment is
	// observable only asynchronously.
	require.Eventually(t, func() bool { return ch.totalGranted() == uint64(tunnelflow.ReadCreditBatch) },
		2*time.Second, 5*time.Millisecond,
		"consuming a full batch replenishes the worker's read window")
	// Hold past the first grant to prove the batch is sent exactly once rather
	// than once per consumed frame.
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, uint64(tunnelflow.ReadCreditBatch), ch.totalGranted(),
		"consuming a full batch replenishes the worker's read window exactly once")
}

// Read must not block on the shared channel send path: it runs under readMu and,
// by the time credit is accrued, has already copied bytes into the caller's
// buffer. A grant sent inline would park the consumer's read goroutine on the
// channel-wide send permit -- which honours neither the read deadline nor
// Conn.Close -- withholding bytes Read already holds until the whole channel
// tore down. This pins that Read returns promptly even when every credit send is
// wedged.
func TestConnReadDoesNotBlockOnWedgedCreditSend(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ch := &creditRecordingChannel{ctx: ctx, blockSend: release}
	tc := newConn(ch, "conn-1", "example.test", 443)

	for range tunnelflow.ReadCreditBatch {
		tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
			Data: []byte("x"),
		})})
	}

	buf := make([]byte, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range tunnelflow.ReadCreditBatch {
			if _, err := tc.Read(buf); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked on the wedged credit send instead of returning buffered bytes")
	}
}

// A conn abandoned by a FAILED dial must release its credit goroutine. Such a
// conn is never returned to a caller and so is never Closed -- only unregistered
// -- so tying the release to Close alone leaked one goroutine per failed dial for
// the shared channel's entire lifetime (a channel outlives many dials).
func TestConnFailedDialReleasesCreditLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &recordingChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)

	// The dial failure paths abandon the conn via unregister, without Close.
	tc.unregister()

	select {
	case <-tc.credit.done():
	case <-time.After(2 * time.Second):
		t.Fatal("an abandoned conn leaked its credit-loop goroutine until the channel died")
	}
}

// alwaysFailSendChannel fails every SendRPCNoWait, so dialTunnelContext exits on
// its open-send failure path -- before tc.reqID is assigned.
type alwaysFailSendChannel struct{ ctx context.Context }

func (c *alwaysFailSendChannel) Context() context.Context { return c.ctx }
func (*alwaysFailSendChannel) UnregisterPending(uint64)   {}
func (*alwaysFailSendChannel) UnregisterStream(uint64)    {}
func (*alwaysFailSendChannel) SendRPCNoWait(context.Context, string, []byte, RPCHandlers) (uint64, error) {
	return 0, errors.New("send failed")
}

// A dial whose OPEN SEND fails must also release the credit goroutine newConn
// spawned. This path cannot route through unregister() -- tc.reqID is not
// assigned yet, so it would target reqID 0, an id allocateReqIDLocked never hands
// out -- which is why dialTunnelContext guards every pre-open exit with its own
// deferred release. Each failed dial otherwise parked a goroutine for the shared channel's
// whole lifetime, and a retrying port-forward dials repeatedly.
func TestDialTunnelSendFailureReleasesCreditLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &alwaysFailSendChannel{ctx: ctx}

	settled := func() int {
		runtime.GC()
		return runtime.NumGoroutine()
	}
	// Let any goroutines from earlier tests retire before sampling the baseline.
	time.Sleep(50 * time.Millisecond)
	baseline := settled()

	const dials = 20
	for range dials {
		conn, err := dialTunnelContext(ctx, ch, "example.test", 443)
		require.Error(t, err, "the dial fails when its open frame cannot be sent")
		require.Nil(t, conn)
	}

	// Each abandoned dial must retire its creditLoop; a leak parks one goroutine
	// per dial for the shared channel's whole lifetime.
	require.Eventually(t, func() bool { return settled() < baseline+dials/2 },
		2*time.Second, 10*time.Millisecond,
		"dials abandoned before their open was sent leaked creditLoop goroutines")
}

func newReassemblyTestChannel(t *testing.T) *Channel {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return &Channel{
		ctx:        ctx,
		cancel:     cancel,
		sendPermit: make(chan struct{}, 1),
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
}

// A partial reassembly buffer must not outlive the request that owns it.
// Reassembly state exists only to feed a registered handler, so once the request
// is unregistered (its RPC timed out, its caller gave up, its conn closed) the
// partial can never be completed: leaving it behind pinned up to
// DefaultMaxMessageSize AND consumed a slot of the incomplete-chunked cap for the
// channel's whole life, so four abandoned partials permanently rejected every
// subsequent chunked message on an otherwise healthy channel.
func TestChannelUnregisterDropsPartialReassembly(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[7] = respCh
	ch.reassembly[7] = &channelwire.ChunkBuffer{Parts: [][]byte{[]byte("partial")}, Total: 7}

	ch.UnregisterPending(7)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.NotContains(t, ch.reassembly, uint64(7),
		"unregistering the last handler drops the partial it could have fed")
}

// A partial must survive while ANY handler for its id is still live: a tunnel
// conn registers both a response and a stream handler under one id, so dropping
// the response handler alone must not discard a stream's in-flight reassembly.
func TestChannelUnregisterKeepsPartialWhileStreamHandlerLives(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[7] = respCh
	ch.streamCbs[7] = newStreamCallback(func(*leapmuxv1.InnerStreamMessage) {}, ch.ctx.Done())
	t.Cleanup(func() { ch.streamCbs[7].stop() })
	ch.reassembly[7] = &channelwire.ChunkBuffer{Parts: [][]byte{[]byte("partial")}, Total: 7}

	ch.UnregisterPending(7)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.Contains(t, ch.reassembly, uint64(7),
		"the stream handler can still consume the partial, so it must be kept")
}

// The correlation-id allocator must never hand out 0 (which keeps a zero-valued
// correlation id distinguishable from an allocated one) nor an id whose handler is
// still registered. A bare incrementing counter wraps at 2^32 and then silently
// collides -- clobbering a live tunnel Conn's stream registration and misrouting
// its responses, which the duplicate-response guard cannot even detect. A tunnel
// burns an id per Write, so wrap is reachable on a long-lived transfer.
func TestAllocateReqIDSkipsReservedAndLiveIDs(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	ch.pending[1] = make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.streamCbs[2] = newStreamCallback(func(*leapmuxv1.InnerStreamMessage) {}, ch.ctx.Done())
	t.Cleanup(func() { ch.streamCbs[2].stop() })

	// Poised at the counter's ceiling: the next bare increment would yield 0, then
	// 1, then 2 -- the skipped zero and both live registrations. uint64 makes this
	// unreachable in production (see channel.proto's correlation_id), but the skip
	// must still hold at the boundary rather than depending on never reaching it.
	ch.nextReqID = ^uint64(0)

	ch.mu.Lock()
	defer ch.mu.Unlock()
	first := ch.allocateReqIDLocked()
	assert.NotZero(t, first, "id 0 is never allocated, so a zero correlation id stays distinguishable")
	assert.NotContains(t, []uint64{1, 2}, first, "a live registration is never reused")
	assert.Equal(t, uint64(3), first)

	second := ch.allocateReqIDLocked()
	assert.Equal(t, uint64(4), second, "allocation continues past the skipped ids")

	assert.NotNil(t, ch.pending[1], "the live response handler is untouched")
	assert.NotNil(t, ch.streamCbs[2], "the live stream handler is untouched")
}

// deliverResponse delivers with a non-blocking send, so an unbuffered response
// channel would have its ONLY response silently dropped -- and logged as a
// spurious "duplicate". The convention was documented but unenforced; reject it
// at the call so a future caller fails loudly instead of losing a response.
func TestSendRPCNoWaitRejectsUnbufferedResponseChannel(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	unbuffered := make(chan *leapmuxv1.InnerRpcResponse)

	_, err := ch.SendRPCNoWait(context.Background(), "Method", nil, RPCHandlers{Response: unbuffered})
	require.ErrorContains(t, err, "must be buffered")

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.Empty(t, ch.pending, "a rejected call registers no handler")
}

// A latchedErr's ZERO VALUE must be a usable, unlatched latch.
//
// Its sibling primitive in this same change (ctxutil.Mutex) documents
// the same property, and for the same reason: a constructor returning the latch by
// value copies a sync.Once into its destination, and a zero value that merely LOOKS
// usable is a landmine -- with an eagerly-created done channel, a Conn field or
// struct literal that forgot the constructor would panic on the first stream error
// (close of a nil channel) and block forever in Read (a nil Done never fires).
func TestLatchedErrZeroValueIsUsable(t *testing.T) {
	var l latchedErr
	require.NoError(t, l.Err(), "an unlatched latch reports no error")
	select {
	case <-l.Done():
		t.Fatal("an unlatched latch must not be done")
	default:
	}

	l.Set(io.EOF)
	select {
	case <-l.Done():
	case <-time.After(time.Second):
		t.Fatal("Set must close Done")
	}
	assert.Equal(t, io.EOF, l.Err())

	// Done must hand back the SAME channel every call, not a fresh one per read.
	assert.Equal(t, l.Done(), l.Done(), "Done must be stable across calls")
}

// First-wins: later Sets are no-ops, so the surfaced cause stays stable when several
// concurrent failures report the same underlying broken target.
func TestLatchedErrKeepsFirstError(t *testing.T) {
	var l latchedErr
	l.Set(io.EOF)
	l.Set(errors.New("a later, different failure"))
	assert.Equal(t, io.EOF, l.Err(), "the first error sticks")
}

func newReadTestConn(t *testing.T) *Conn {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		sendPermit: make(chan struct{}, 1),
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	t.Cleanup(cancel)
	return newConn(ch, "conn-1", "example.test", 1234)
}

func readResultWithin(t *testing.T, tc *Conn, timeout time.Duration) (int, error) {
	t.Helper()
	type result struct {
		n   int
		err error
	}
	resultCh := make(chan result, 1)
	go func() {
		buf := make([]byte, 32)
		n, err := tc.Read(buf)
		resultCh <- result{n: n, err: err}
	}()
	select {
	case result := <-resultCh:
		return result.n, result.err
	case <-time.After(timeout):
		return 0, errors.New("read did not complete")
	}
}

func TestConnReadDeadlineInterruptsBlockedRead(t *testing.T) {
	tc := newReadTestConn(t)
	require.NoError(t, tc.SetReadDeadline(time.Now().Add(20*time.Millisecond)))

	n, err := readResultWithin(t, tc, 250*time.Millisecond)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
}

func TestConnReadDeadlineUpdateInterruptsExistingRead(t *testing.T) {
	tc := newReadTestConn(t)
	require.NoError(t, tc.SetReadDeadline(time.Now().Add(time.Hour)))

	resultCh := make(chan error, 1)
	go func() {
		_, err := tc.Read(make([]byte, 1))
		resultCh <- err
	}()
	time.Sleep(10 * time.Millisecond)
	require.NoError(t, tc.SetReadDeadline(time.Now().Add(20*time.Millisecond)))
	select {
	case err := <-resultCh:
		assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	case <-time.After(250 * time.Millisecond):
		t.Fatal("updated read deadline did not interrupt blocked read")
	}
}

func TestConnWriteDeadlineInterruptsSendPermitWait(t *testing.T) {
	tc := newReadTestConn(t)
	channel := tc.ch.(*Channel)
	channel.sendPermit <- struct{}{}
	t.Cleanup(func() { <-channel.sendPermit })
	require.NoError(t, tc.SetWriteDeadline(time.Now().Add(20*time.Millisecond)))

	started := time.Now()
	n, err := tc.Write([]byte("blocked"))
	assert.Zero(t, n)
	assert.ErrorIs(t, err, os.ErrDeadlineExceeded)
	assert.Less(t, time.Since(started), 250*time.Millisecond)
}

// A write deadline SET AFTER a Write parks on a full send window must wake the
// parked writer -- net.Conn requires a deadline to apply to already-pending I/O.
// conn.go's own comment (~line 133) records that a Write parked on a full send
// window once came to miss a later SetWriteDeadline; TestConnWriteDeadlineInterrupts
// SendPermitWait covers the permit-acquire wait with a PRE-SET deadline, but nothing
// pinned the window-park case, which is the one the deadline watcher exists for.
func TestConnWriteDeadlineSetAfterParkWakesWindowParkedWrite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &windowTestChannel{ctx: ctx}
	tc := newConn(ch, "conn-1", "example.test", 443)
	fillSendWindow(t, tc, ch)

	done := make(chan error, 1)
	go func() {
		_, err := tc.Write([]byte("blocked"))
		done <- err
	}()
	select {
	case <-done:
		t.Fatal("Write returned even though the send window was full")
	case <-time.After(50 * time.Millisecond):
	}

	require.NoError(t, tc.SetWriteDeadline(time.Now().Add(20*time.Millisecond)))
	select {
	case err := <-done:
		assert.ErrorIs(t, err, os.ErrDeadlineExceeded,
			"a deadline set after a Write parked on a full window must wake it")
	case <-time.After(2 * time.Second):
		t.Fatal("a write deadline set after the Write parked never woke it")
	}
}

func TestConnTerminalErrorIsOrderedAndPersistent(t *testing.T) {
	tc := newReadTestConn(t)
	tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
		Data: []byte("before EOF"),
	})})
	tc.onStreamMessage(&leapmuxv1.InnerStreamMessage{Payload: mustMarshalTunnelEvent(t, &leapmuxv1.TunnelConnEvent{
		Eof: true,
	})})

	buf := make([]byte, 32)
	n, err := tc.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, "before EOF", string(buf[:n]))

	n, err = readResultWithin(t, tc, 250*time.Millisecond)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)

	n, err = readResultWithin(t, tc, 250*time.Millisecond)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)
}

func TestConnCloseWakesBlockedReadPersistently(t *testing.T) {
	tc := newReadTestConn(t)
	resultCh := make(chan error, 1)
	go func() {
		_, err := tc.Read(make([]byte, 1))
		resultCh <- err
	}()

	// Prevent the unit fixture from attempting a real encrypted close RPC.
	tc.ch.(*Channel).closed.Store(true)
	require.NoError(t, tc.Close())
	assert.ErrorIs(t, <-resultCh, net.ErrClosed)
	_, err := tc.Read(make([]byte, 1))
	assert.ErrorIs(t, err, net.ErrClosed)
}

func mustMarshalTunnelEvent(t *testing.T, event *leapmuxv1.TunnelConnEvent) []byte {
	t.Helper()
	payload, err := proto.Marshal(event)
	require.NoError(t, err)
	return payload
}

func TestStreamCallbackPreservesOrderWithoutBlockingDelivery(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	delivered := make(chan string, 2)
	stream := newStreamCallback(func(message *leapmuxv1.InnerStreamMessage) {
		value := string(message.GetPayload())
		if value == "first" {
			close(firstStarted)
			<-releaseFirst
		}
		delivered <- value
	}, make(chan struct{}))
	t.Cleanup(stream.stop)
	stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("first")})
	<-firstStarted

	started := time.Now()
	stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("second")})
	assert.Less(t, time.Since(started), 50*time.Millisecond)
	close(releaseFirst)
	assert.Equal(t, "first", <-delivered)
	assert.Equal(t, "second", <-delivered)
}

func TestStreamCallbackAppliesBackpressureWhenQueueFull(t *testing.T) {
	// A full stream queue must block (backpressure) instead of dropping the
	// message or tearing down the channel. The blocked deliver resumes once the
	// consumer drains or the callback is stopped.
	firstStarted := make(chan struct{})
	releaseConsumer := make(chan struct{})
	var once sync.Once
	stream := newStreamCallback(func(message *leapmuxv1.InnerStreamMessage) {
		once.Do(func() { close(firstStarted) })
		<-releaseConsumer
	}, make(chan struct{}))
	t.Cleanup(stream.stop)

	// First message is picked up by the dispatcher and stalls the callback.
	stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("first")})
	<-firstStarted

	// Fill the buffered queue (capacity streamCallbackQueueSize) while the
	// callback is stalled on the first message.
	for i := 0; i < streamCallbackQueueSize; i++ {
		stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("queued")})
	}

	// The next deliver must block: the queue is full and the consumer is stalled.
	overflowDelivered := make(chan struct{})
	go func() {
		stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("overflow")})
		close(overflowDelivered)
	}()
	select {
	case <-overflowDelivered:
		t.Fatal("deliver did not apply backpressure when the queue was full")
	case <-time.After(50 * time.Millisecond):
	}

	// Releasing the consumer drains the queue, which unblocks the overflow deliver.
	close(releaseConsumer)
	select {
	case <-overflowDelivered:
	case <-time.After(time.Second):
		t.Fatal("overflow deliver did not complete after the consumer drained")
	}
}

// A deliver blocked on a saturated consumer must unblock when the owning
// channel's lifetime ends, so a channel-wide cancel (a peer write failure or
// Close) can still wind recvLoop down instead of pinning a "healthy" channel
// forever with leaked goroutines.
func TestStreamCallbackDeliverUnblocksOnChannelDone(t *testing.T) {
	chDone := make(chan struct{})
	releaseConsumer := make(chan struct{})
	stream := newStreamCallback(func(*leapmuxv1.InnerStreamMessage) {
		<-releaseConsumer
	}, chDone)
	t.Cleanup(stream.stop)

	// Stall the dispatcher on the first message, then fill the queue.
	stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("first")})
	for i := 0; i < streamCallbackQueueSize; i++ {
		stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("queued")})
	}

	// The next deliver blocks: the queue is full and the consumer is stalled.
	blockedDeliver := make(chan struct{})
	go func() {
		stream.deliver(&leapmuxv1.InnerStreamMessage{Payload: []byte("overflow")})
		close(blockedDeliver)
	}()
	select {
	case <-blockedDeliver:
		t.Fatal("deliver did not block on the saturated queue")
	case <-time.After(50 * time.Millisecond):
	}

	// Ending the channel's lifetime must unblock the wedged deliver.
	close(chDone)
	select {
	case <-blockedDeliver:
	case <-time.After(time.Second):
		t.Fatal("deliver stayed blocked after the channel's lifetime ended")
	}
	close(releaseConsumer)
}

// deliverRPCError is the per-request error path recvLoop uses for chunked
// cap/oversize violations: it must notify only the registered pending handler
// (so a single oversized message fails its own RPC instead of tearing down
// the shared channel for every in-flight operation) and be a non-blocking
// no-op for a correlation ID with no handler.
func TestDeliverRPCError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}

	t.Run("registered handler receives error", func(t *testing.T) {
		respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
		const reqID = 42
		ch.pending[reqID] = respCh
		t.Cleanup(func() { delete(ch.pending, reqID) })

		ch.deliverRPCError(reqID, errCodeResourceExhausted, "too many incomplete chunked messages")

		select {
		case resp := <-respCh:
			require.True(t, resp.GetIsError())
			assert.Equal(t, errCodeResourceExhausted, resp.GetErrorCode())
			assert.Contains(t, resp.GetErrorMessage(), "too many incomplete")
		case <-time.After(time.Second):
			t.Fatal("registered handler did not receive the error")
		}
	})

	t.Run("unregistered id is a non-blocking no-op", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			ch.deliverRPCError(9999, errCodeResourceExhausted, "no handler")
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("deliverRPCError blocked for an unregistered correlation ID")
		}
	})
}

// deliverResponse must ALWAYS deliver the first (and only legitimate) response
// for a correlation id -- the buffered send never drops it, even for a caller
// that has not drained the channel yet -- while a second response for the same
// id (a peer protocol violation) is dropped rather than wedging recvLoop, and
// the real first response is preserved intact.
func TestDeliverResponseNeverDropsTheFirstResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 3
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[reqID] = respCh

	require.True(t, ch.deliverResponse(reqID, &leapmuxv1.InnerRpcResponse{Payload: []byte("first")}),
		"a registered pending handler must be reported as delivered")

	// A duplicate response for the same id, arriving while the first is still
	// buffered, must not block (the wedge this guards against) and must not evict
	// the real first response the caller is about to read.
	done := make(chan struct{})
	go func() {
		ch.deliverResponse(reqID, &leapmuxv1.InnerRpcResponse{Payload: []byte("duplicate")})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("deliverResponse wedged on a full buffer instead of dropping the duplicate")
	}

	got := <-respCh
	assert.Equal(t, []byte("first"), got.GetPayload(),
		"the first response must be preserved -- never overwritten or dropped by a duplicate")
	select {
	case extra := <-respCh:
		t.Fatalf("a duplicate response was delivered instead of dropped: %v", extra)
	default:
	}
}

// deliverRPCError must reach a request that has dropped its pending response
// handler and kept only a stream handler -- a tunnel Conn after OpenTunnelConn
// succeeds -- so its Conn.Read gets a terminal error instead of blocking until
// the whole channel dies.
func TestDeliverRPCErrorFallsBackToStreamHandler(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 7
	got := make(chan *leapmuxv1.InnerStreamMessage, 1)
	stream := newStreamCallback(func(msg *leapmuxv1.InnerStreamMessage) { got <- msg }, ch.ctx.Done())
	ch.streamCbs[reqID] = stream
	t.Cleanup(stream.stop)

	ch.deliverRPCError(reqID, errCodeResourceExhausted, "message too large")

	select {
	case msg := <-got:
		require.True(t, msg.GetIsError(), "stream handler must receive a terminal error frame")
		assert.Equal(t, errCodeResourceExhausted, msg.GetErrorCode())
		assert.Contains(t, msg.GetErrorMessage(), "too large")
	case <-time.After(time.Second):
		t.Fatal("stream-only request never received the terminal error")
	}
}

// An oversize chunked message must be errored ONCE and its remaining chunks
// dropped, not re-buffered from scratch.
//
// This is the stream-handler case: deliverTooLarge routes to the stream handler when
// no pending handler is left (a tunnel Conn after OpenTunnelConn succeeds), and
// Conn.onStreamMessage only latches its terminal error -- it never unregisters. So
// the id keeps a live handler, and simply DELETING the buffer on the violation let
// the very next MORE chunk allocate a fresh one and re-accumulate to the
// ceiling, over and over, for as long as the peer kept sending: allocate-and-discard
// cycles on the channel's sole receive goroutine, stalling every other RPC, stream
// and tunnel multiplexed on it.
func TestReassembleDropsChunksAfterOversizeInsteadOfReBuffering(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		channelID:  "ch-1",
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 7
	// A stream-only handler: the shape that never unregisters on error.
	errors := make(chan *leapmuxv1.InnerStreamMessage, 8)
	stream := newStreamCallback(func(msg *leapmuxv1.InnerStreamMessage) {
		if msg.GetIsError() {
			errors <- msg
		}
	}, ch.ctx.Done())
	ch.streamCbs[reqID] = stream
	t.Cleanup(stream.stop)

	// Drive the message past the ceiling.
	chunk := make([]byte, 1<<20) // 1 MiB
	more := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
	for range (channelwire.DefaultMaxMessageSize / len(chunk)) + 1 {
		payload, complete := ch.reassemble(reqID, more, chunk)
		require.False(t, complete)
		require.Nil(t, payload)
	}

	select {
	case msg := <-errors:
		assert.Contains(t, msg.GetErrorMessage(), "too large")
	case <-time.After(2 * time.Second):
		t.Fatal("an oversize chunked message must error its request")
	}

	ch.mu.Lock()
	buf, present := ch.reassembly[reqID]
	ch.mu.Unlock()
	require.True(t, present, "the id must stay marked so later chunks are recognised")
	require.True(t, buf.Poisoned, "the buffer must be poisoned, not deleted")
	assert.Zero(t, buf.Total, "poisoning must release the accumulated parts")
	assert.Empty(t, buf.Parts)

	// The peer keeps sending. Every further chunk must be dropped without
	// re-accumulating, and must NOT error the request again.
	for range 32 {
		payload, complete := ch.reassemble(reqID, more, chunk)
		require.False(t, complete)
		require.Nil(t, payload)
	}

	ch.mu.Lock()
	buf = ch.reassembly[reqID]
	ch.mu.Unlock()
	assert.Zero(t, buf.Total,
		"chunks after the violation must be dropped, not re-buffered toward the ceiling again")
	assert.Empty(t, errors, "the request must be errored once, not once per re-accumulation cycle")

	// The message's final chunk clears the marker, so the id is not held forever.
	payload, complete := ch.reassemble(reqID, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, chunk)
	assert.False(t, complete, "a poisoned message never completes")
	assert.Nil(t, payload)
	ch.mu.Lock()
	_, stillPresent := ch.reassembly[reqID]
	ch.mu.Unlock()
	assert.False(t, stillPresent, "the final chunk must clear the poisoned marker")
}

// An out-of-spec flags value (a combined MORE|CLOSE, an unknown integer) is a
// protocol violation dropped whole -- NOT read as a final chunk, which would
// deliver a truncated assembly to proto.Unmarshal, and NOT buffered, which
// would leave phantom reassembly state for an id the peer never terminates.
func TestReassembleDropsOutOfSpecFlags(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		channelID:  "ch-flags",
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 9

	payload, complete := ch.reassemble(reqID, leapmuxv1.ChannelMessageFlags(3), []byte("garbage"))
	assert.False(t, complete, "an out-of-spec frame must not complete a message")
	assert.Nil(t, payload)
	ch.mu.Lock()
	assert.Empty(t, ch.reassembly, "a dropped out-of-spec frame must leave no reassembly state")
	ch.mu.Unlock()
}

// The ordinary path must still work: chunks reassemble into the original message.
func TestReassembleJoinsChunks(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	const reqID = 3
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[reqID] = respCh

	more := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
	payload, complete := ch.reassemble(reqID, more, []byte("hello "))
	require.False(t, complete)
	require.Nil(t, payload)

	payload, complete = ch.reassemble(reqID, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("world"))
	require.True(t, complete, "the final chunk completes the message")
	assert.Equal(t, "hello world", string(payload))

	ch.mu.Lock()
	_, present := ch.reassembly[reqID]
	ch.mu.Unlock()
	assert.False(t, present, "a completed message must release its buffer")
}

// A non-chunked message passes straight through.
func TestReassemblePassesThroughUnchunked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	ch := &Channel{
		ctx:        ctx,
		cancel:     cancel,
		pending:    make(map[uint64]chan<- *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint64]*streamCallback),
		reassembly: make(map[uint64]*channelwire.ChunkBuffer),
	}
	payload, complete := ch.reassemble(1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("solo"))
	require.True(t, complete)
	assert.Equal(t, "solo", string(payload))
}

// streamErrorChannel builds a reassembly-only Channel plus a stream-only handler
// for correlationID -- the shape that latches its error WITHOUT unregistering,
// so every further chunk for the id keeps reaching reassemble. It returns the
// channel and the queue the handler's error deliveries land in.
func newStreamErrorChannel(t *testing.T, correlationID uint64) (*Channel, chan *leapmuxv1.InnerStreamMessage) {
	t.Helper()
	ch := newReassemblyTestChannel(t)
	ch.channelID = "ch-1"
	errs := make(chan *leapmuxv1.InnerStreamMessage, 512)
	stream := newStreamCallback(func(msg *leapmuxv1.InnerStreamMessage) {
		if msg.GetIsError() {
			errs <- msg
		}
	}, ch.ctx.Done())
	ch.streamCbs[correlationID] = stream
	t.Cleanup(stream.stop)
	return ch, errs
}

// An id rejected for breaching the incomplete-chunked CAP must be tombstoned,
// exactly as an oversize one is: without an entry to recognise them, chunks
// 2..N of the rejected message each re-entered the cap branch and errored the
// request AGAIN (a stream handler latches its error without unregistering, so
// the chunks never stop arriving), storming the channel's sole receive goroutine
// and stalling every unrelated RPC ack on it -- including the tunnel
// send-window releases unrelated writes are parked on. The commit's own
// invariant is that an id whose message breached a limit is errored ONCE and its
// remaining chunks dropped.
func TestReassembleErrorsOverCapIDOnceAndDropsItsRemainingChunks(t *testing.T) {
	const overCapID = 99
	ch, errs := newStreamErrorChannel(t, overCapID)
	more := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE

	// Fill the cap with live, in-flight chunked messages on other ids.
	for i := range uint64(channelwire.DefaultMaxIncompleteChunked) {
		reqID := 1 + i
		ch.pending[reqID] = make(chan *leapmuxv1.InnerRpcResponse, 1)
		payload, complete := ch.reassemble(reqID, more, []byte("live"))
		require.False(t, complete)
		require.Nil(t, payload)
	}
	ch.mu.Lock()
	require.Equal(t, channelwire.DefaultMaxIncompleteChunked, ch.liveReassemblyLocked(), "the cap is full of live buffers")
	ch.mu.Unlock()

	// A further id's first chunk breaches the cap and is errored.
	payload, complete := ch.reassemble(overCapID, more, []byte("first"))
	require.False(t, complete)
	require.Nil(t, payload)
	select {
	case msg := <-errs:
		assert.Contains(t, msg.GetErrorMessage(), "too many incomplete chunked messages")
	case <-time.After(2 * time.Second):
		t.Fatal("an over-cap chunked message must error its request")
	}

	ch.mu.Lock()
	buf, present := ch.reassembly[overCapID]
	ch.mu.Unlock()
	require.True(t, present, "the rejected id must be tombstoned so its later chunks are recognised")
	assert.True(t, buf.Poisoned)

	// The peer keeps sending. Every further chunk must be dropped silently.
	for range 200 {
		payload, complete := ch.reassemble(overCapID, more, []byte("more"))
		require.False(t, complete)
		require.Nil(t, payload)
	}
	ch.mu.Lock()
	buf = ch.reassembly[overCapID]
	ch.mu.Unlock()
	assert.Zero(t, buf.Total, "chunks after the violation must be dropped, not buffered")
	assert.Empty(t, buf.Parts)
	assert.Empty(t, errs, "the request must be errored ONCE, not once per chunk")

	// The message's final chunk reaps the tombstone, so the id is not held forever.
	payload, complete = ch.reassemble(overCapID, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("last"))
	assert.False(t, complete, "a rejected message never completes")
	assert.Nil(t, payload)
	ch.mu.Lock()
	_, stillPresent := ch.reassembly[overCapID]
	ch.mu.Unlock()
	assert.False(t, stillPresent, "the final chunk must reap the tombstone")
}

// A tombstone holds no bytes and must therefore hold no cap slot. Charging it
// one would let four protocol violations permanently reject every subsequent
// chunked message on an otherwise healthy channel -- the exact denial of service
// the cap exists to prevent. Assert a legitimate new chunked message can start
// as soon as a LIVE one completes, even while tombstones linger.
func TestReassembleTombstoneDoesNotConsumeCapSlot(t *testing.T) {
	const overCapID = 99
	ch, errs := newStreamErrorChannel(t, overCapID)
	more := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE

	for i := range uint64(channelwire.DefaultMaxIncompleteChunked) {
		reqID := 1 + i
		ch.pending[reqID] = make(chan *leapmuxv1.InnerRpcResponse, 1)
		_, complete := ch.reassemble(reqID, more, []byte("live"))
		require.False(t, complete)
	}
	_, complete := ch.reassemble(overCapID, more, []byte("first"))
	require.False(t, complete)
	select {
	case <-errs:
	case <-time.After(2 * time.Second):
		t.Fatal("the over-cap id must be errored")
	}
	ch.mu.Lock()
	require.Contains(t, ch.reassembly, uint64(overCapID), "the tombstone is present")
	require.Equal(t, channelwire.DefaultMaxIncompleteChunked, ch.liveReassemblyLocked(),
		"the tombstone must not be counted against the cap")
	ch.mu.Unlock()

	// One live message completes, freeing exactly one slot.
	payload, complete := ch.reassemble(1, leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED, []byte("done"))
	require.True(t, complete)
	assert.Equal(t, "livedone", string(payload))

	// A legitimate new chunked message must now be able to start, despite the
	// lingering tombstone.
	const freshID = 500
	ch.pending[freshID] = make(chan *leapmuxv1.InnerRpcResponse, 1)
	_, complete = ch.reassemble(freshID, more, []byte("fresh"))
	require.False(t, complete)
	ch.mu.Lock()
	fresh, started := ch.reassembly[freshID]
	ch.mu.Unlock()
	require.True(t, started, "a tombstone must not block a legitimate chunked message from starting")
	assert.False(t, fresh.Poisoned)
	assert.Equal(t, len("fresh"), fresh.Total, "the fresh message accumulates normally")
}

// unregisterRequest reaps a tombstone as it does a live buffer, and the derived
// live count must stay right either way: because liveReassemblyLocked counts
// non-tombstone entries rather than maintaining a running total, reaping a
// tombstone cannot mis-adjust a slot -- the whole class of counter drift the
// derive forecloses.
func TestUnregisterDroppingTombstoneKeepsCapAccountingStraight(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	ch.pending[7] = make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.pending[8] = make(chan *leapmuxv1.InnerRpcResponse, 1)

	ch.mu.Lock()
	ch.startReassemblyLocked(7)
	ch.startReassemblyLocked(8)
	ch.poisonReassemblyLocked(7) // 7 breached a limit: live -> tombstone
	require.Equal(t, 1, ch.liveReassemblyLocked(), "only 8 is still live")
	ch.mu.Unlock()

	ch.UnregisterPending(7) // reaps the tombstone

	ch.mu.Lock()
	defer ch.mu.Unlock()
	assert.NotContains(t, ch.reassembly, uint64(7))
	assert.Equal(t, 1, ch.liveReassemblyLocked(),
		"reaping a tombstone must not decrement the live count it never incremented")
}

// poisonReassemblyLocked must be idempotent: a second poison of an already-
// poisoned entry leaves it a tombstone, and the derived live count excludes it
// regardless of how many times it was poisoned.
func TestPoisonReassemblyIsIdempotent(t *testing.T) {
	ch := newReassemblyTestChannel(t)
	ch.mu.Lock()
	defer ch.mu.Unlock()
	ch.startReassemblyLocked(7)
	require.Equal(t, 1, ch.liveReassemblyLocked())
	ch.poisonReassemblyLocked(7)
	ch.poisonReassemblyLocked(7)
	assert.Zero(t, ch.liveReassemblyLocked(), "a tombstone is not counted, no matter how many times it was poisoned")
	assert.True(t, ch.reassembly[7].Poisoned)
}
