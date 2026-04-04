// Package tunnel provides a public E2EE channel client for the desktop app
// to communicate with Workers via the Hub relay.
package tunnel

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
)

// Channel manages a single E2EE channel from the desktop app to a Worker
// via the Hub's WebSocket relay.
type Channel struct {
	channelID string
	session   *noiseutil.Session
	ws        *websocket.Conn
	ctx       context.Context
	cancel    context.CancelFunc

	mu         sync.Mutex
	nextReqID  uint32
	pending    map[uint32]chan *leapmuxv1.InnerRpcResponse
	streamCbs  map[uint32]func(*leapmuxv1.InnerStreamMessage)
	reassembly map[uint32]*chunkBuffer
	closed     atomic.Bool
}

type chunkBuffer struct {
	parts [][]byte
	total int
}

// OpenChannelOptions configures how OpenChannel connects to the Hub.
type OpenChannelOptions struct {
	// HTTPClient is the HTTP client for ConnectRPC calls (GetWorkerPublicKey, etc.).
	// When nil, a default client with 30s timeout is used.
	HTTPClient *http.Client

	// WebSocketHTTPClient is the HTTP/1.1 client used for WebSocket upgrade.
	// When nil, websocket.Dial uses the default transport.
	WebSocketHTTPClient *http.Client
}

// OpenChannel opens a new E2EE channel to the specified worker via Hub.
func OpenChannel(ctx context.Context, hubURL, token, userID, workerID string, opts *OpenChannelOptions) (*Channel, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	var wsHTTPClient *http.Client
	if opts != nil {
		if opts.HTTPClient != nil {
			httpClient = opts.HTTPClient
		}
		wsHTTPClient = opts.WebSocketHTTPClient
	}
	channelClient := leapmuxv1connect.NewChannelServiceClient(httpClient, hubURL, withAuth(token))

	// 1. Get Worker's public key.
	pubKeyResp, err := channelClient.GetWorkerPublicKey(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerPublicKeyRequest{WorkerId: workerID},
	))
	if err != nil {
		return nil, fmt.Errorf("get worker public key: %w", err)
	}

	// 2. Get Worker's encryption mode.
	encModeResp, err := channelClient.GetWorkerEncryptionMode(ctx, connect.NewRequest(
		&leapmuxv1.GetWorkerEncryptionModeRequest{WorkerId: workerID},
	))
	if err != nil {
		return nil, fmt.Errorf("get worker encryption mode: %w", err)
	}
	encMode := encModeResp.Msg.GetEncryptionMode()

	// 3. Perform Noise_NK handshake (message 1).
	var hs *noiseutil.HandshakeState
	var msg1 []byte
	if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
		hs, msg1, err = noiseutil.ClassicalInitiatorHandshake1(pubKeyResp.Msg.GetPublicKey())
	} else {
		hs, msg1, err = noiseutil.InitiatorHandshake1(pubKeyResp.Msg.GetPublicKey(), pubKeyResp.Msg.GetMlkemPublicKey())
	}
	if err != nil {
		return nil, fmt.Errorf("handshake1: %w", err)
	}

	// 4. Open channel via Hub.
	openResp, err := channelClient.OpenChannel(ctx, connect.NewRequest(
		&leapmuxv1.OpenChannelRequest{
			WorkerId:         workerID,
			HandshakePayload: msg1,
		},
	))
	if err != nil {
		return nil, fmt.Errorf("open channel: %w", err)
	}
	channelID := openResp.Msg.GetChannelId()

	// 5. Complete handshake (message 2).
	var session *noiseutil.Session
	if encMode == leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC {
		session, err = noiseutil.ClassicalInitiatorHandshake2(hs, openResp.Msg.GetHandshakePayload())
	} else {
		session, err = noiseutil.InitiatorHandshake2(hs, openResp.Msg.GetHandshakePayload(), pubKeyResp.Msg.GetSlhdsaPublicKey())
	}
	if err != nil {
		return nil, fmt.Errorf("handshake2: %w", err)
	}

	// 6. Connect to Hub's WebSocket relay.
	wsURL := channelwire.HTTPToWS(hubURL) + "/ws/channel"

	subprotocols := []string{"channel-relay"}
	if token != "" {
		subprotocols = append(subprotocols, channelwire.AuthTokenSubprotocolPrefix+token)
	}

	wsDialOpts := &websocket.DialOptions{
		Subprotocols: subprotocols,
	}
	if wsHTTPClient != nil {
		wsDialOpts.HTTPClient = wsHTTPClient
	}
	wsConn, _, err := websocket.Dial(ctx, wsURL, wsDialOpts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}
	wsConn.SetReadLimit(channelwire.DefaultMaxMessageSize + 1024)

	chCtx, chCancel := context.WithCancel(ctx)
	ch := &Channel{
		channelID:  channelID,
		session:    session,
		ws:         wsConn,
		ctx:        chCtx,
		cancel:     chCancel,
		pending:    make(map[uint32]chan *leapmuxv1.InnerRpcResponse),
		streamCbs:  make(map[uint32]func(*leapmuxv1.InnerStreamMessage)),
		reassembly: make(map[uint32]*chunkBuffer),
	}

	go ch.recvLoop()

	// 7. Send UserIdClaim.
	claim := &leapmuxv1.UserIdClaim{
		UserId:      userID,
		TimestampMs: time.Now().UnixMilli(),
	}
	claimEnv := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_UserIdClaim{UserIdClaim: claim},
	}
	if err := ch.sendInner(0, claimEnv); err != nil {
		ch.Close()
		return nil, fmt.Errorf("send user id claim: %w", err)
	}

	// Wait for UserIdClaimResponse.
	claimRespCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.mu.Lock()
	ch.pending[0] = claimRespCh
	ch.mu.Unlock()

	select {
	case <-time.After(30 * time.Second):
		ch.Close()
		return nil, fmt.Errorf("user id claim timeout")
	case <-chCtx.Done():
		ch.Close()
		return nil, fmt.Errorf("channel closed during claim")
	case resp := <-claimRespCh:
		if resp != nil && resp.GetIsError() {
			ch.Close()
			return nil, fmt.Errorf("user id claim rejected: %s", resp.GetErrorMessage())
		}
	}

	slog.Info("tunnel E2EE channel opened", "channel_id", channelID, "worker_id", workerID)
	return ch, nil
}

// Closed returns true if the channel has been closed.
func (ch *Channel) Closed() bool {
	return ch.closed.Load()
}

// Close closes the channel.
func (ch *Channel) Close() {
	if ch.closed.CompareAndSwap(false, true) {
		ch.cancel()
		_ = ch.ws.Close(websocket.StatusNormalClosure, "")

		ch.mu.Lock()
		for _, c := range ch.pending {
			close(c)
		}
		ch.pending = make(map[uint32]chan *leapmuxv1.InnerRpcResponse)
		ch.streamCbs = make(map[uint32]func(*leapmuxv1.InnerStreamMessage))
		ch.reassembly = make(map[uint32]*chunkBuffer)
		ch.mu.Unlock()
	}
}

// CallRPC sends a unary inner RPC and waits for the response.
func (ch *Channel) CallRPC(method string, payload []byte) (*leapmuxv1.InnerRpcResponse, error) {
	reqID := atomic.AddUint32(&ch.nextReqID, 1)

	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	ch.mu.Lock()
	ch.pending[reqID] = respCh
	ch.mu.Unlock()

	defer func() {
		ch.mu.Lock()
		delete(ch.pending, reqID)
		ch.mu.Unlock()
	}()

	innerReq := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{
			Request: &leapmuxv1.InnerRpcRequest{
				Method:  method,
				Payload: payload,
			},
		},
	}

	if err := ch.sendInner(reqID, innerReq); err != nil {
		return nil, err
	}

	select {
	case resp := <-respCh:
		if resp == nil {
			return nil, fmt.Errorf("channel closed")
		}
		if resp.GetIsError() {
			return nil, fmt.Errorf("rpc error (code %d): %s", resp.GetErrorCode(), resp.GetErrorMessage())
		}
		return resp, nil
	case <-time.After(30 * time.Second):
		return nil, fmt.Errorf("rpc timeout")
	case <-ch.ctx.Done():
		return nil, ch.ctx.Err()
	}
}

// SendRPCNoWait sends an inner RPC without waiting for a response.
// Returns the correlation ID for registering stream callbacks.
func (ch *Channel) SendRPCNoWait(method string, payload []byte) (uint32, error) {
	reqID := atomic.AddUint32(&ch.nextReqID, 1)

	innerReq := &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Request{
			Request: &leapmuxv1.InnerRpcRequest{
				Method:  method,
				Payload: payload,
			},
		},
	}

	if err := ch.sendInner(reqID, innerReq); err != nil {
		return 0, err
	}
	return reqID, nil
}

// RegisterPending registers a response channel for a specific request ID.
func (ch *Channel) RegisterPending(reqID uint32, respCh chan *leapmuxv1.InnerRpcResponse) {
	ch.mu.Lock()
	ch.pending[reqID] = respCh
	ch.mu.Unlock()
}

// UnregisterPending removes a pending response channel.
func (ch *Channel) UnregisterPending(reqID uint32) {
	ch.mu.Lock()
	delete(ch.pending, reqID)
	ch.mu.Unlock()
}

// RegisterStream registers a callback for stream messages.
func (ch *Channel) RegisterStream(reqID uint32, cb func(*leapmuxv1.InnerStreamMessage)) {
	ch.mu.Lock()
	ch.streamCbs[reqID] = cb
	ch.mu.Unlock()
}

// UnregisterStream removes a stream callback.
func (ch *Channel) UnregisterStream(reqID uint32) {
	ch.mu.Lock()
	delete(ch.streamCbs, reqID)
	ch.mu.Unlock()
}

// Context returns the channel's context.
func (ch *Channel) Context() context.Context {
	return ch.ctx
}

// sendInner encrypts and sends an InnerMessage.
func (ch *Channel) sendInner(correlationID uint32, msg *leapmuxv1.InnerMessage) error {
	plaintext, err := proto.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal inner message: %w", err)
	}

	for offset := 0; offset < len(plaintext) || offset == 0; {
		end := offset + channelwire.MaxPlaintextPerChunk
		if end > len(plaintext) {
			end = len(plaintext)
		}
		chunk := plaintext[offset:end]
		offset = end

		ciphertext, encErr := ch.session.Encrypt(chunk)
		if encErr != nil {
			return fmt.Errorf("encrypt: %w", encErr)
		}

		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if end < len(plaintext) {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}

		chMsg := &leapmuxv1.ChannelMessage{
			ProtocolVersion: 1,
			ChannelId:       ch.channelID,
			CorrelationId:   correlationID,
			Flags:           flags,
			Ciphertext:      ciphertext,
		}

		if err := channelwire.WriteChannelMessage(ch.ctx, ch.ws, chMsg); err != nil {
			return fmt.Errorf("write ws: %w", err)
		}
	}
	return nil
}

// recvLoop reads messages from the WebSocket and dispatches them.
func (ch *Channel) recvLoop() {
	defer ch.Close()

	for {
		chMsg, err := channelwire.ReadChannelMessage(ch.ctx, ch.ws)
		if err != nil {
			if ch.closed.Load() || ch.ctx.Err() != nil {
				return
			}
			slog.Error("tunnel channel recv error", "channel_id", ch.channelID, "error", err)
			return
		}

		correlationID := chMsg.GetCorrelationId()
		plaintext, decErr := ch.session.Decrypt(chMsg.GetCiphertext())
		if decErr != nil {
			slog.Error("tunnel channel decrypt error", "channel_id", ch.channelID, "error", decErr)
			return
		}

		// Handle chunking.
		if chMsg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
			ch.mu.Lock()
			buf, ok := ch.reassembly[correlationID]
			if !ok {
				buf = &chunkBuffer{}
				ch.reassembly[correlationID] = buf
			}
			buf.parts = append(buf.parts, plaintext)
			buf.total += len(plaintext)
			if buf.total > channelwire.DefaultMaxMessageSize {
				delete(ch.reassembly, correlationID)
				ch.mu.Unlock()
				slog.Error("tunnel channel message too large", "channel_id", ch.channelID)
				return
			}
			ch.mu.Unlock()
			continue
		}

		// Final chunk or non-chunked.
		ch.mu.Lock()
		buf, wasChunked := ch.reassembly[correlationID]
		if wasChunked {
			delete(ch.reassembly, correlationID)
		}
		ch.mu.Unlock()

		if wasChunked {
			assembled := make([]byte, 0, buf.total+len(plaintext))
			for _, p := range buf.parts {
				assembled = append(assembled, p...)
			}
			assembled = append(assembled, plaintext...)
			plaintext = assembled
		}

		var inner leapmuxv1.InnerMessage
		if err := proto.Unmarshal(plaintext, &inner); err != nil {
			slog.Error("tunnel channel unmarshal error", "channel_id", ch.channelID, "error", err)
			continue
		}

		switch kind := inner.GetKind().(type) {
		case *leapmuxv1.InnerMessage_Response:
			ch.mu.Lock()
			respCh, ok := ch.pending[correlationID]
			ch.mu.Unlock()
			if ok {
				respCh <- kind.Response
			}

		case *leapmuxv1.InnerMessage_Stream:
			ch.mu.Lock()
			cb, ok := ch.streamCbs[correlationID]
			ch.mu.Unlock()
			if ok {
				cb(kind.Stream)
			}

		case *leapmuxv1.InnerMessage_UserIdClaimResponse:
			ch.mu.Lock()
			respCh, ok := ch.pending[0]
			ch.mu.Unlock()
			if ok {
				if kind.UserIdClaimResponse.GetSuccess() {
					respCh <- &leapmuxv1.InnerRpcResponse{}
				} else {
					respCh <- &leapmuxv1.InnerRpcResponse{
						IsError:      true,
						ErrorMessage: kind.UserIdClaimResponse.GetErrorMessage(),
					}
				}
			}
		}
	}
}

func withAuth(token string) connect.ClientOption {
	return connect.WithInterceptors(&authInterceptor{token: token})
}

type authInterceptor struct {
	token string
}

func (a *authInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		if a.token != "" {
			req.Header().Set("Authorization", "Bearer "+a.token)
		}
		return next(ctx, req)
	}
}

func (a *authInterceptor) WrapStreamingClient(next connect.StreamingClientFunc) connect.StreamingClientFunc {
	return next
}

func (a *authInterceptor) WrapStreamingHandler(next connect.StreamingHandlerFunc) connect.StreamingHandlerFunc {
	return next
}
