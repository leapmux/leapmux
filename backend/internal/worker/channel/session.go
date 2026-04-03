// Package channel manages encrypted E2EE channels on the Worker side.
// It handles Noise_NK handshakes, session encryption/decryption,
// and inner RPC message dispatch.
package channel

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	noiseutil "github.com/leapmux/leapmux/internal/noise"
	"google.golang.org/protobuf/proto"
)

// SendFunc sends a ConnectRequest (containing ChannelMessage) to the Hub.
type SendFunc func(msg *leapmuxv1.ConnectRequest) error

// channelSession tracks an active encrypted channel.
type channelSession struct {
	ChannelID              string
	UserID                 string
	Session                *noiseutil.Session
	sender                 *channelSender          // shared sender for this channel (protects Encrypt+Send)
	verified               bool                    // true after a valid UserIdClaim has been received
	reassembly             map[uint32]*chunkBuffer // correlationID -> in-progress chunk reassembly
	accessibleWorkspaceIDs map[string]bool         // workspaces the user can access (set from ChannelOpenRequest)
}

// CloseCallback is called when a channel is closed, allowing cleanup
// of associated resources (e.g. removing watchers).
type CloseCallback func(channelID string)

// Manager manages encrypted channel sessions on the Worker side.
type Manager struct {
	mu                   sync.RWMutex
	sessions             map[string]*channelSession  // channelID -> session
	compositeKey         *noiseutil.CompositeKeypair // Worker's composite keypair (X25519 + ML-KEM + SLH-DSA)
	encryptionMode       leapmuxv1.EncryptionMode    // Encryption mode
	sendFn               SendFunc                    // Function to send messages to Hub
	dispatcher           *Dispatcher                 // Inner RPC dispatcher
	closeCallback        CloseCallback               // Called when a channel is closed
	maxMessageSize       int                         // maximum reassembled message size
	maxIncompleteChunked int                         // maximum in-flight chunked sequences per channel
}

// NewManager creates a new channel Manager.
// Pass 0 for maxMessageSize or maxIncompleteChunked to use defaults.
func NewManager(
	compositeKey *noiseutil.CompositeKeypair,
	encryptionMode leapmuxv1.EncryptionMode,
	sendFn SendFunc,
	maxMessageSize int,
	maxIncompleteChunked int,
	closeCallback CloseCallback,
) *Manager {
	if maxMessageSize <= 0 {
		maxMessageSize = channelwire.DefaultMaxMessageSize
	}
	if maxIncompleteChunked <= 0 {
		maxIncompleteChunked = channelwire.DefaultMaxIncompleteChunked
	}
	return &Manager{
		sessions:             make(map[string]*channelSession),
		compositeKey:         compositeKey,
		encryptionMode:       encryptionMode,
		sendFn:               sendFn,
		maxMessageSize:       maxMessageSize,
		maxIncompleteChunked: maxIncompleteChunked,
		closeCallback:        closeCallback,
	}
}

// SetDispatcher sets the inner RPC dispatcher for handling decrypted requests.
func (m *Manager) SetDispatcher(d *Dispatcher) {
	m.dispatcher = d
}

// Dispatcher returns the inner RPC dispatcher.
func (m *Manager) Dispatcher() *Dispatcher {
	return m.dispatcher
}

// HandleOpen processes a ChannelOpenRequest from the Hub.
// It performs the Noise_NK responder handshake and returns the response.
func (m *Manager) HandleOpen(req *leapmuxv1.ChannelOpenRequest) *leapmuxv1.ChannelOpenResponse {
	var handshakeResp []byte
	var session *noiseutil.Session
	var err error

	switch m.encryptionMode {
	case leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC:
		// Classical Noise_NK (X25519 only, no PQ).
		handshakeResp, session, err = noiseutil.ClassicalResponderHandshake(
			m.compositeKey.X25519Public,
			m.compositeKey.X25519Private,
			req.GetHandshakePayload(),
		)
	default:
		// Post-quantum hybrid Noise_NK (X25519 + ML-KEM + SLH-DSA).
		handshakeResp, session, err = noiseutil.ResponderHandshake(
			m.compositeKey,
			req.GetHandshakePayload(),
		)
	}

	if err != nil {
		slog.Error("channel handshake failed",
			"channel_id", req.GetChannelId(),
			"error", err,
		)
		return &leapmuxv1.ChannelOpenResponse{
			ChannelId: req.GetChannelId(),
			Error:     fmt.Sprintf("handshake failed: %v", err),
		}
	}

	// Build accessible workspace ID set from the Hub-provided list.
	awsIDs := make(map[string]bool, len(req.GetAccessibleWorkspaceIds()))
	for _, wsID := range req.GetAccessibleWorkspaceIds() {
		awsIDs[wsID] = true
	}

	m.mu.Lock()
	m.sessions[req.GetChannelId()] = &channelSession{
		ChannelID: req.GetChannelId(),
		UserID:    req.GetUserId(),
		Session:   session,
		sender: &channelSender{
			channelID:      req.GetChannelId(),
			session:        session,
			sendFn:         m.sendFn,
			maxMessageSize: m.maxMessageSize,
		},
		reassembly:             make(map[uint32]*chunkBuffer),
		accessibleWorkspaceIDs: awsIDs,
	}
	m.mu.Unlock()

	slog.Info("channel opened",
		"channel_id", req.GetChannelId(),
		"user_id", req.GetUserId(),
		"encryption_mode", m.encryptionMode,
	)

	return &leapmuxv1.ChannelOpenResponse{
		ChannelId:        req.GetChannelId(),
		HandshakePayload: handshakeResp,
	}
}

// AccessibleWorkspaceIDs returns the set of workspace IDs accessible to the
// user on the given channel. Returns nil if the channel is not found.
func (m *Manager) AccessibleWorkspaceIDs(channelID string) map[string]bool {
	m.mu.RLock()
	sess, ok := m.sessions[channelID]
	m.mu.RUnlock()
	if !ok {
		return nil
	}
	return sess.accessibleWorkspaceIDs
}

// AddAccessibleWorkspaceID adds a workspace ID to the channel's accessible
// set. This is needed when a workspace is created after the channel was
// opened, so that subsequent WatchEvents calls can see the new workspace.
func (m *Manager) AddAccessibleWorkspaceID(channelID, workspaceID string) {
	m.mu.RLock()
	sess, ok := m.sessions[channelID]
	m.mu.RUnlock()
	if !ok {
		return
	}
	sess.accessibleWorkspaceIDs[workspaceID] = true
}

// HandleMessage processes an encrypted ChannelMessage from the Hub.
// It decrypts the message, dispatches the inner RPC, and sends encrypted responses.
func (m *Manager) HandleMessage(msg *leapmuxv1.ChannelMessage) {
	m.mu.RLock()
	sess, ok := m.sessions[msg.GetChannelId()]
	m.mu.RUnlock()

	if !ok {
		slog.Warn("received message for unknown channel", "channel_id", msg.GetChannelId())
		return
	}

	// Decrypt. This must remain sequential in the receive loop because
	// the receive cipher state tracks a nonce counter.
	decrypted, err := sess.Session.Decrypt(msg.GetCiphertext())
	if err != nil {
		slog.Error("failed to decrypt channel message",
			"channel_id", msg.GetChannelId(),
			"ciphertext_len", len(msg.GetCiphertext()),
			"error", err,
		)
		return
	}

	requestID := msg.GetCorrelationId()

	// Handle chunked reassembly.
	var plaintext []byte
	if msg.GetFlags() == leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE {
		// More chunks to come — buffer this one.
		buf, exists := sess.reassembly[requestID]
		if !exists {
			// New chunked sequence — check max incomplete limit.
			if len(sess.reassembly) >= m.maxIncompleteChunked {
				slog.Warn("too many incomplete chunked messages",
					"channel_id", msg.GetChannelId(),
					"correlation_id", requestID,
					"count", len(sess.reassembly),
				)
				go func() { _ = sess.sender.sendError(requestID, 8, "too many incomplete chunked messages") }() // RESOURCE_EXHAUSTED
				return
			}
			buf = &chunkBuffer{}
			sess.reassembly[requestID] = buf
		}
		buf.parts = append(buf.parts, decrypted)
		buf.total += len(decrypted)
		if buf.total > m.maxMessageSize {
			slog.Warn("chunked message exceeds max size",
				"channel_id", msg.GetChannelId(),
				"correlation_id", requestID,
				"size", buf.total,
				"max", m.maxMessageSize,
			)
			delete(sess.reassembly, requestID)
			go func(total, max int) {
				_ = sess.sender.sendError(requestID, 8, // RESOURCE_EXHAUSTED
					fmt.Sprintf("chunked message too large: %d bytes exceeds %d byte limit", total, max))
			}(buf.total, m.maxMessageSize)
			return
		}
		slog.Debug("buffered chunk",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"chunk_size", len(decrypted),
			"total", buf.total,
		)
		return
	}

	// Final chunk (or single non-chunked message).
	if buf, exists := sess.reassembly[requestID]; exists {
		// Concatenate buffered parts + this final chunk.
		buf.parts = append(buf.parts, decrypted)
		buf.total += len(decrypted)
		if buf.total > m.maxMessageSize {
			slog.Warn("chunked message exceeds max size",
				"channel_id", msg.GetChannelId(),
				"correlation_id", requestID,
				"size", buf.total,
				"max", m.maxMessageSize,
			)
			delete(sess.reassembly, requestID)
			go func(total, max int) {
				_ = sess.sender.sendError(requestID, 8, // RESOURCE_EXHAUSTED
					fmt.Sprintf("chunked message too large: %d bytes exceeds %d byte limit", total, max))
			}(buf.total, m.maxMessageSize)
			return
		}
		plaintext = make([]byte, 0, buf.total)
		for _, part := range buf.parts {
			plaintext = append(plaintext, part...)
		}
		delete(sess.reassembly, requestID)
		slog.Debug("reassembled chunked message",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"total_size", len(plaintext),
		)
	} else {
		plaintext = decrypted
	}

	slog.Debug("received channel message",
		"channel_id", msg.GetChannelId(),
		"correlation_id", requestID,
	)

	// Use the per-session sender so all messages on this channel share
	// a single mutex protecting Encrypt+Send (prevents nonce reuse).
	// Wrap it with boundSender to bind the request ID for responses.
	bs := &boundSender{sender: sess.sender, requestID: requestID}

	// Parse InnerMessage envelope.
	var envelope leapmuxv1.InnerMessage
	if err := proto.Unmarshal(plaintext, &envelope); err != nil {
		slog.Error("failed to unmarshal inner message",
			"channel_id", msg.GetChannelId(),
			"error", err,
		)
		return
	}

	switch kind := envelope.GetKind().(type) {
	case *leapmuxv1.InnerMessage_UserIdClaim:
		slog.Debug("received user_id_claim",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
		)
		// The verified flag must be set synchronously so that
		// subsequent Requests on this channel see it immediately.
		// However, the response send is dispatched in a goroutine
		// to avoid blocking the receive loop on the send mutex,
		// which can deadlock when handlers are concurrently sending
		// responses on the same bidi stream.
		if sess.verified {
			go sess.sender.sendClaimResponse(requestID, false, "user ID already verified")
			return
		}
		if kind.UserIdClaim.GetUserId() != sess.UserID {
			go func() {
				sess.sender.sendClaimResponse(requestID, false, "user ID mismatch")
				m.HandleClose(msg.GetChannelId())
			}()
			return
		}
		sess.verified = true
		go sess.sender.sendClaimResponse(requestID, true, "")

	case *leapmuxv1.InnerMessage_Request:
		bs.method = kind.Request.GetMethod()
		slog.Debug("received inner RPC request",
			"channel_id", msg.GetChannelId(),
			"correlation_id", requestID,
			"method", bs.method,
		)
		if !sess.verified {
			go func() { _ = bs.SendError(9, "user ID not verified") }() // FAILED_PRECONDITION
			return
		}
		if m.dispatcher != nil {
			// Dispatch in a goroutine so the receive loop is not
			// blocked by slow handlers (e.g. WatchEvents with git ops).
			go m.dispatcher.DispatchWith(sess.UserID, kind.Request, bs)
		} else {
			go func() { _ = bs.SendError(12, "no dispatcher configured") }() // UNIMPLEMENTED
		}

	default:
		slog.Warn("unexpected inner message kind",
			"channel_id", msg.GetChannelId(),
			"kind", fmt.Sprintf("%T", envelope.GetKind()),
		)
	}
}

// HandleClose removes a channel session and invokes the close callback.
func (m *Manager) HandleClose(channelID string) {
	m.mu.Lock()
	if sess, ok := m.sessions[channelID]; ok {
		// Clear reassembly buffers to free memory.
		sess.reassembly = nil
	}
	delete(m.sessions, channelID)
	m.mu.Unlock()

	if m.closeCallback != nil {
		m.closeCallback(channelID)
	}

	slog.Info("channel closed", "channel_id", channelID)
}

// CloseAll removes all channel sessions and invokes the close callback
// for each one, allowing associated resources (e.g. watchers) to be
// cleaned up.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	channels := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		channels = append(channels, id)
	}
	m.sessions = make(map[string]*channelSession)
	m.mu.Unlock()

	if m.closeCallback != nil {
		for _, id := range channels {
			m.closeCallback(id)
		}
	}
}

// channelSender sends encrypted responses back through a channel.
// A mutex protects Encrypt+Send to prevent nonce reuse from concurrent access.
type channelSender struct {
	mu             sync.Mutex
	channelID      string
	session        *noiseutil.Session
	sendFn         SendFunc
	maxMessageSize int
}

// sendEncrypted marshals an InnerMessage envelope, encrypts, and sends.
// If the marshaled data exceeds channelwire.MaxPlaintextPerChunk, it is split into chunks,
// each encrypted separately and sent with flags=MORE except the last.
func (s *channelSender) sendEncrypted(requestID uint32, envelope *leapmuxv1.InnerMessage) error {
	data, err := proto.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal inner message: %w", err)
	}

	if len(data) > s.maxMessageSize {
		return fmt.Errorf("message too large: %d > %d", len(data), s.maxMessageSize)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Fast path: single frame.
	if len(data) <= channelwire.MaxPlaintextPerChunk {
		ciphertext, encErr := s.session.Encrypt(data)
		if encErr != nil {
			return fmt.Errorf("encrypt inner message: %w", encErr)
		}

		slog.Debug("sending channel message",
			"channel_id", s.channelID,
			"correlation_id", requestID,
		)

		return s.sendFn(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
				ChannelMessageResp: &leapmuxv1.ChannelMessage{
					ProtocolVersion: 1,
					ChannelId:       s.channelID,
					Ciphertext:      ciphertext,
					CorrelationId:   requestID,
				},
			},
		})
	}

	// Chunked path: split data into channelwire.MaxPlaintextPerChunk-sized chunks.
	for offset := 0; offset < len(data); {
		end := offset + channelwire.MaxPlaintextPerChunk
		if end > len(data) {
			end = len(data)
		}
		chunk := data[offset:end]
		offset = end

		ciphertext, encErr := s.session.Encrypt(chunk)
		if encErr != nil {
			return fmt.Errorf("encrypt chunk: %w", encErr)
		}

		flags := leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_UNSPECIFIED
		if offset < len(data) {
			flags = leapmuxv1.ChannelMessageFlags_CHANNEL_MESSAGE_FLAGS_MORE
		}

		slog.Debug("sending channel message chunk",
			"channel_id", s.channelID,
			"correlation_id", requestID,
			"chunk_size", len(chunk),
			"flags", flags,
		)

		if sendErr := s.sendFn(&leapmuxv1.ConnectRequest{
			Payload: &leapmuxv1.ConnectRequest_ChannelMessageResp{
				ChannelMessageResp: &leapmuxv1.ChannelMessage{
					ProtocolVersion: 1,
					ChannelId:       s.channelID,
					Ciphertext:      ciphertext,
					CorrelationId:   requestID,
					Flags:           flags,
				},
			},
		}); sendErr != nil {
			return fmt.Errorf("send chunk: %w", sendErr)
		}
	}

	return nil
}

// sendClaimResponse sends a UserIdClaimResponse.
func (s *channelSender) sendClaimResponse(requestID uint32, success bool, errorMessage string) {
	slog.Debug("sending user_id_claim_response",
		"channel_id", s.channelID,
		"correlation_id", requestID,
		"success", success,
	)
	_ = s.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_UserIdClaimResponse{
			UserIdClaimResponse: &leapmuxv1.UserIdClaimResponse{
				Success:      success,
				ErrorMessage: errorMessage,
			},
		},
	})
}

// sendResponse sends an InnerRpcResponse (encrypted) back to the frontend.
func (s *channelSender) sendResponse(requestID uint32, resp *leapmuxv1.InnerRpcResponse) error {
	return s.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Response{Response: resp},
	})
}

// sendError sends an error InnerRpcResponse.
func (s *channelSender) sendError(requestID uint32, code int32, message string) error {
	return s.sendResponse(requestID, &leapmuxv1.InnerRpcResponse{
		IsError:      true,
		ErrorMessage: message,
		ErrorCode:    code,
	})
}

// ChannelID returns the E2EE channel ID for this sender.
func (s *channelSender) ChannelID() string {
	return s.channelID
}

// sendStream sends an InnerStreamMessage (encrypted) back to the frontend.
func (s *channelSender) sendStream(requestID uint32, msg *leapmuxv1.InnerStreamMessage) error {
	return s.sendEncrypted(requestID, &leapmuxv1.InnerMessage{
		Kind: &leapmuxv1.InnerMessage_Stream{Stream: msg},
	})
}

// boundSender wraps a channelSender with a fixed requestID and method name.
// This is needed because the channelSender is shared per channel but each
// incoming message has its own ID, and dispatch runs in goroutines concurrently.
type boundSender struct {
	sender    *channelSender
	requestID uint32
	method    string
}

func (b *boundSender) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	slog.Debug("sending inner RPC response",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"is_error", resp.GetIsError(),
		"error_code", resp.GetErrorCode(),
		"error_message", resp.GetErrorMessage(),
		"payload_len", len(resp.GetPayload()),
	)
	if err := b.sender.sendResponse(b.requestID, resp); err != nil {
		slog.Warn("failed to send inner RPC response",
			"channel_id", b.sender.channelID,
			"correlation_id", b.requestID,
			"method", b.method,
			"error", err,
		)
		return err
	}
	return nil
}

func (b *boundSender) SendError(code int32, message string) error {
	slog.Debug("sending inner RPC error",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"code", code,
		"message", message,
	)
	if err := b.sender.sendError(b.requestID, code, message); err != nil {
		slog.Warn("failed to send inner RPC error",
			"channel_id", b.sender.channelID,
			"correlation_id", b.requestID,
			"method", b.method,
			"error", err,
		)
		return err
	}
	return nil
}

func (b *boundSender) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	slog.Debug("sending inner stream message",
		"channel_id", b.sender.channelID,
		"correlation_id", b.requestID,
		"method", b.method,
		"end", msg.GetEnd(),
		"is_error", msg.GetIsError(),
		"error_code", msg.GetErrorCode(),
		"error_message", msg.GetErrorMessage(),
		"payload_len", len(msg.GetPayload()),
	)
	if err := b.sender.sendStream(b.requestID, msg); err != nil {
		slog.Warn("failed to send inner stream message",
			"channel_id", b.sender.channelID,
			"correlation_id", b.requestID,
			"method", b.method,
			"error", err,
		)
		return err
	}
	return nil
}

func (b *boundSender) ChannelID() string {
	return b.sender.ChannelID()
}
