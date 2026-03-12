package channel

import (
	"log/slog"
	"runtime/debug"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// ResponseWriter sends inner RPC responses back through the transport layer.
// Implementations include the encrypted channel sender and the cleartext
// RPC-forward writer.
type ResponseWriter interface {
	SendResponse(resp *leapmuxv1.InnerRpcResponse) error
	SendError(code int32, message string) error
	SendStream(msg *leapmuxv1.InnerStreamMessage) error
	// ChannelID returns the E2EE channel ID for this writer.
	// Returns "" for non-channel writers (e.g. cleartext RPC-forward).
	ChannelID() string
}

// Sender provides methods for sending responses back to the caller.
type Sender struct {
	inner ResponseWriter
}

// NewSender creates a Sender from a ResponseWriter.
func NewSender(w ResponseWriter) *Sender {
	return &Sender{inner: w}
}

// SendResponse sends an InnerRpcResponse.
func (s *Sender) SendResponse(resp *leapmuxv1.InnerRpcResponse) error {
	return s.inner.SendResponse(resp)
}

// SendError sends an error response.
func (s *Sender) SendError(code int32, message string) error {
	return s.inner.SendError(code, message)
}

// SendStream sends an InnerStreamMessage.
func (s *Sender) SendStream(msg *leapmuxv1.InnerStreamMessage) error {
	return s.inner.SendStream(msg)
}

// ChannelID returns the E2EE channel ID for this sender.
func (s *Sender) ChannelID() string {
	return s.inner.ChannelID()
}

// HandlerFunc is the signature for an inner RPC method handler.
// userID is the authenticated user, req is the deserialized request, sender
// is used to send one or more responses back through the encrypted channel.
type HandlerFunc func(userID string, req *leapmuxv1.InnerRpcRequest, sender *Sender)

// Dispatcher routes inner RPC method calls to registered handlers.
type Dispatcher struct {
	handlers map[string]HandlerFunc
}

// NewDispatcher creates a new inner RPC Dispatcher.
func NewDispatcher() *Dispatcher {
	return &Dispatcher{
		handlers: make(map[string]HandlerFunc),
	}
}

// Register adds a handler for the given method name.
func (d *Dispatcher) Register(method string, handler HandlerFunc) {
	d.handlers[method] = handler
}

// Dispatch routes an InnerRpcRequest to the appropriate handler using
// the encrypted channel sender. requestID is the correlation ID from
// the outer ChannelMessage, used to match responses on the frontend.
func (d *Dispatcher) Dispatch(userID string, req *leapmuxv1.InnerRpcRequest, requestID uint32, inner *channelSender) {
	d.DispatchWith(userID, req, &boundSender{sender: inner, requestID: requestID, method: req.GetMethod()})
}

// DispatchWith routes an InnerRpcRequest to the appropriate handler using
// a custom ResponseWriter. This is used for both encrypted channel sends
// and cleartext RPC-forward responses.
func (d *Dispatcher) DispatchWith(userID string, req *leapmuxv1.InnerRpcRequest, w ResponseWriter) {
	handler, ok := d.handlers[req.GetMethod()]
	if !ok {
		slog.Warn("unknown inner RPC method",
			"method", req.GetMethod(),
		)
		_ = w.SendError(12, "unknown method: "+req.GetMethod()) // UNIMPLEMENTED
		return
	}

	// Recover from handler panics so the goroutine doesn't die silently,
	// which would leave the frontend waiting until its 30s timeout fires.
	defer func() {
		if r := recover(); r != nil {
			slog.Error("handler panicked",
				"method", req.GetMethod(),
				"panic", r,
				"stack", string(debug.Stack()),
			)
			_ = w.SendError(13, "internal error") // INTERNAL
		}
	}()

	sender := &Sender{inner: w}
	handler(userID, req, sender)
}
