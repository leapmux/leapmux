package streamevents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/tunnel"
)

// errHandleClosed is returned by a Handle method invoked after the stream has
// been cancelled or the transport ended. It tells the caller the revision did
// not land (so it must re-state it later) rather than letting a silently-dropped
// UpdateStream advance local state.
var errHandleClosed = errors.New("streamevents: handle is closed")

// transportLogger returns logger, or slog.Default() when nil, so a transport never
// nil-derefs when surfacing a malformed frame. Shared by both transport constructors.
func transportLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.Default()
	}
	return logger
}

// decodeWatchFrame unmarshals a stream payload into a WatchEventsResponse, returning
// (resp, true) on success. On failure it logs a warning tagged with transportName and
// returns (nil, false): a frame that doesn't decode is a protocol violation (the worker
// always marshals a valid response), so it is surfaced rather than dropped silently,
// while the caller keeps the stream alive -- one corrupt frame shouldn't tear down an
// otherwise-healthy subscription. The single decode+warn both transports share, so
// their malformed-frame handling can't drift.
func decodeWatchFrame(logger *slog.Logger, transportName string, payload []byte) (*leapmuxv1.WatchEventsResponse, bool) {
	var resp leapmuxv1.WatchEventsResponse
	if err := proto.Unmarshal(payload, &resp); err != nil {
		logger.Warn("streamevents: dropping malformed WatchEvents frame", "transport", transportName, "error", err)
		return nil, false
	}
	return &resp, true
}

// logTerminalStreamError reports the error envelope that ended a WatchEvents
// subscription.
//
// A subscription can die on a server-side rejection ("only the worker owner...",
// a permission denial, an oversize/cap violation) rather than on a clean end. The
// envelope carrying that reason used to be received and discarded, so a
// `--follow` consumer just went quiet -- or resubscribed into the same rejection
// forever -- with nothing to diagnose from. The Transport contract signals
// termination through a reason-less `done` channel, so the reason cannot be
// returned to the caller without reshaping that interface; logging it is what
// keeps it recoverable today, and mirrors both the malformed-frame log above and
// crossworker.Client.StreamInner, which surfaces the identical envelope.
func logTerminalStreamError(logger *slog.Logger, transportName string, code int32, message string) {
	logger.Error("streamevents: subscription ended with a server error",
		"transport", transportName, "code", code, "error", message)
}

// Handle is a live WatchEvents subscription on one transport stream.
// Revisions ride Update; retirement is Cancel.
type Handle interface {
	Update(*leapmuxv1.WatchEventsRequest) error
	Cancel()
	Done() <-chan struct{}
}

// Transport abstracts "stream a WatchEvents subscription against
// some backend." Two production wirings exist:
//
//   - hub-bound: open an E2EE channel via `*remote.Client.OpenE2EEChannel`,
//     then `SendRPCNoWait` with atomically registered response and stream handlers.
//   - local-IPC: call `RemoteIPCService.StreamInner` with method
//     `worker.WatchEvents` over the per-agent socket.
//
// Both flows decode the same `WatchEventsResponse` payload off the
// wire. By writing the cursor + reconnect logic against this
// interface, callers (`agent messages --follow` and `events --include
// agent,terminal`) share one implementation regardless of mode.
type Transport interface {
	// OpenWatchEvents starts a WatchEvents subscription with the
	// given request. onFrame is called once per delivered
	// WatchEventsResponse. The returned Handle sends revisions on
	// the same stream and cancels it when done.
	OpenWatchEvents(ctx context.Context, req *leapmuxv1.WatchEventsRequest,
		onFrame func(*leapmuxv1.WatchEventsResponse)) (Handle, error)
}

// channelLike is the subset of `*tunnel.Channel` Transport needs.
// Pulled into an interface so tests don't need a real Noise_NK
// responder; production wires it to *tunnel.Channel directly.
//
// Close() is deliberately absent: the transport does NOT own the channel's
// lifecycle (see ChannelTransport's doc), so exposing Close here would invite a
// future teardown edit to close a channel a caller still expects to reuse. The
// interface is exactly the subset the transport calls, no more.
type channelLike interface {
	SendRPCNoWait(ctx context.Context, method string, payload []byte, handlers tunnel.RPCHandlers) (uint64, error)
	SendStreamRequest(ctx context.Context, reqID uint64, payload []byte, cancel bool) error
	CancelStream(ctx context.Context, reqID uint64) error
	UnregisterStream(reqID uint64)
	UnregisterPending(reqID uint64)
	Context() context.Context
}

// ChannelTransport runs a WatchEvents subscription over an existing
// E2EE channel. Use this in hub-bound mode where the channel was
// opened via `*remote.Client.OpenE2EEChannel`.
//
// The transport does NOT own the channel's lifecycle — callers open
// the channel, hand it in, and close it when they're done with the
// worker (e.g. on snapshot eviction). Multiple revisions on one
// stream are sent via Handle.Update; CancelStream retires it.
type ChannelTransport struct {
	channel channelLike
	logger  *slog.Logger
}

// NewChannelTransport wraps ch. A nil logger falls back to slog.Default() so the
// transport never nil-derefs when surfacing a malformed frame.
func NewChannelTransport(ch channelLike, logger *slog.Logger) *ChannelTransport {
	return &ChannelTransport{channel: ch, logger: transportLogger(logger)}
}

type channelHandle struct {
	ch     channelLike
	reqID  uint64
	ctx    context.Context
	cancel context.CancelFunc
	done   <-chan struct{}
}

func (h *channelHandle) Update(req *leapmuxv1.WatchEventsRequest) error {
	// A trailing Update after Cancel/Done silently drops on the wire; surface
	// it so the caller knows the revision did not land (see localIPCHandle).
	select {
	case <-h.done:
		return errHandleClosed
	default:
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal WatchEventsRequest: %w", err)
	}
	return h.ch.SendStreamRequest(h.ctx, h.reqID, payload, false)
}

func (h *channelHandle) Cancel() {
	// Detach from the stream ctx so a Done-raced cancel still reaches the worker.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.ctx), 5*time.Second)
	defer cancel()
	if err := h.ch.CancelStream(ctx, h.reqID); err != nil {
		slog.Debug("channelHandle.Cancel: CancelStream failed", "error", err)
	}
	// Wake the done goroutine: CancelStream retires the worker side and the
	// local demux registration, but this transport's lifecycle is keyed off
	// ctx, which nothing else cancels for a client-initiated retire.
	h.cancel()
}

func (h *channelHandle) Done() <-chan struct{} { return h.done }

// OpenWatchEvents implements Transport.
func (t *ChannelTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (Handle, error) {
	if t.channel == nil {
		return nil, errors.New("nil channel")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(parentCtx)
	// `closed` lets a frame already in flight when teardown runs observe that the
	// subscription has ended and drop itself: the channel demux invokes this cb from
	// its OWN goroutine and releases the channel lock before calling it, so a frame
	// can race teardown.
	var closed atomic.Bool
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := t.channel.SendRPCNoWait(ctx, "WatchEvents", payload, tunnel.RPCHandlers{
		Response: respCh,
		Stream: func(msg *leapmuxv1.InnerStreamMessage) {
			if closed.Load() {
				return
			}
			if msg.GetIsError() {
				logTerminalStreamError(transportLogger(t.logger), "channel", msg.GetErrorCode(), msg.GetErrorMessage())
				cancel()
				return
			}
			if msg.GetEnd() && len(msg.GetPayload()) == 0 {
				cancel()
				return
			}
			resp, ok := decodeWatchFrame(t.logger, "channel", msg.GetPayload())
			if !ok {
				return
			}
			onFrame(resp)
		},
	})
	if err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
		case <-t.channel.Context().Done():
		case resp := <-respCh:
			if resp.GetIsError() {
				logTerminalStreamError(transportLogger(t.logger), "channel", resp.GetErrorCode(), resp.GetErrorMessage())
			}
		}
		closed.Store(true)
		t.channel.UnregisterStream(reqID)
		t.channel.UnregisterPending(reqID)
		cancel()
	}()
	return &channelHandle{ch: t.channel, reqID: reqID, ctx: ctx, cancel: cancel, done: done}, nil
}

// LocalIPCTransport runs a WatchEvents subscription via the per-agent
// IPC server's StreamInner method. Used by worker-spawned CLI mode
// (`LEAPMUX_REMOTE_SOCK`). The router on the worker side proxies the
// stream to the appropriate inner-RPC handler.
type LocalIPCTransport struct {
	client leapmuxv1connect.RemoteIPCServiceClient
	// targetWorkerID is the worker the WatchEvents subscription is
	// for. Local-IPC routes to the spawning worker by default; this
	// lets `events --include agent,terminal` direct subscriptions to
	// sibling workers via the router's cross-worker dispatch.
	targetWorkerID string
	logger         *slog.Logger
	nextReqID      atomic.Uint64
}

// NewLocalIPCTransport wires the local-IPC client + target. A nil
// logger falls back to slog.Default() so a malformed frame is never nil-deref.
func NewLocalIPCTransport(client leapmuxv1connect.RemoteIPCServiceClient, targetWorkerID string, logger *slog.Logger) *LocalIPCTransport {
	return &LocalIPCTransport{client: client, targetWorkerID: targetWorkerID, logger: transportLogger(logger)}
}

type localIPCHandle struct {
	client      leapmuxv1connect.RemoteIPCServiceClient
	clientReqID string
	parentCtx   context.Context
	cancel      context.CancelFunc
	done        <-chan struct{}
	cancelled   atomic.Bool
}

func (h *localIPCHandle) Update(req *leapmuxv1.WatchEventsRequest) error {
	// Guard against a trailing Update that races Cancel/Done: once cancelled
	// the worker side has retired the clientReqID and the router silently drops
	// the frame while returning nil — so without this the caller would advance
	// its local interest believing the revision landed, and never re-state it.
	// Return a non-nil error so the caller treats the revision as unsent.
	if h.cancelled.Load() {
		return errHandleClosed
	}
	select {
	case <-h.done:
		return errHandleClosed
	default:
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal WatchEventsRequest: %w", err)
	}
	_, err = h.client.UpdateStream(h.parentCtx, connect.NewRequest(&leapmuxv1.UpdateStreamRequest{
		ClientRequestId: h.clientReqID,
		Payload:         payload,
	}))
	return err
}

func (h *localIPCHandle) Cancel() {
	if h.cancelled.Swap(true) {
		return
	}
	// Detach from parentCtx so a cancelled parent still retires the worker stream.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(h.parentCtx), 5*time.Second)
	defer cancel()
	if _, err := h.client.Cancel(ctx, connect.NewRequest(&leapmuxv1.CancelRequest{
		ClientRequestId: h.clientReqID,
	})); err != nil {
		slog.Debug("localIPCHandle.Cancel failed", "error", err)
	}
	h.cancel()
}

func (h *localIPCHandle) Done() <-chan struct{} { return h.done }

// OpenWatchEvents implements Transport.
func (t *LocalIPCTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (Handle, error) {
	if t.client == nil {
		return nil, errors.New("nil RemoteIPCService client")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, err
	}
	clientReqID := fmt.Sprintf("watch-%d", t.nextReqID.Add(1))
	ctx, cancel := context.WithCancel(parentCtx)
	stream, err := t.client.StreamInner(ctx, connect.NewRequest(&leapmuxv1.StreamInnerRequest{
		Method:          "worker.WatchEvents",
		Payload:         payload,
		TargetWorkerId:  t.targetWorkerID,
		ClientRequestId: clientReqID,
	}))
	if err != nil {
		cancel()
		return nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() { _ = stream.Close() }()
		for stream.Receive() {
			env := stream.Msg()
			if env.GetIsError() {
				logTerminalStreamError(transportLogger(t.logger), "local-ipc", env.GetErrorCode(), env.GetErrorMessage())
				return
			}
			if len(env.GetPayload()) == 0 {
				if env.GetEnd() {
					return
				}
				continue
			}
			resp, ok := decodeWatchFrame(t.logger, "local-ipc", env.GetPayload())
			if !ok {
				continue
			}
			onFrame(resp)
			if env.GetEnd() {
				return
			}
		}
	}()
	go func() {
		<-done
		cancel()
	}()
	return &localIPCHandle{
		client:      t.client,
		clientReqID: clientReqID,
		parentCtx:   parentCtx,
		cancel:      cancel,
		done:        done,
	}, nil
}
