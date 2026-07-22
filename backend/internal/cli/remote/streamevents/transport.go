package streamevents

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/tunnel"
)

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
// worker (e.g. on snapshot eviction). Multiple Subscriptions can
// share a channel sequentially (cancel one, open another) but not
// concurrently — the WatchEvents handler is single-stream-per-channel.
type ChannelTransport struct {
	channel channelLike
	logger  *slog.Logger
}

// NewChannelTransport wraps ch. A nil logger falls back to slog.Default() so the
// transport never nil-derefs when surfacing a malformed frame.
func NewChannelTransport(ch channelLike, logger *slog.Logger) *ChannelTransport {
	return &ChannelTransport{channel: ch, logger: transportLogger(logger)}
}

// OpenWatchEvents implements Transport.
func (t *ChannelTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (context.CancelFunc, <-chan struct{}, error) {
	if t.channel == nil {
		return nil, nil, errors.New("nil channel")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parentCtx)
	// `closed` lets a frame already in flight when teardown runs observe that the
	// subscription has ended and drop itself: the channel demux invokes this cb from
	// its OWN goroutine and releases the channel lock before calling it, so a frame
	// can race teardown.
	//
	// The flag is checked ONLY before `onFrame`, and is deliberately NOT
	// synchronized with it. An earlier version held a mutex across onFrame to
	// guarantee no frame is delivered after Done() closes, but that deadlocks:
	// onFrame chains into the consumer's synchronous stdout encode, and if stdout
	// back-pressures (a paused `--follow` reader) the cb blocks holding the mutex,
	// so the teardown goroutine below can never acquire it to set
	// `closed`/`cancel()` -- Done() never closes and Cancel()/Update() hang forever.
	// Not holding it across onFrame lets teardown close `done` promptly while a
	// blocked frame drains on its own. Guarding a lone bool with no compound
	// invariant is exactly an atomic.Bool, so it is one -- the mutex it replaces
	// only made the no-compound-invariant property harder to see. The cost is a
	// narrow window where a late frame's onFrame runs just after Done() closes;
	// consumers already tolerate this (the `agent messages --follow` loop treats a
	// late `delivered` flip as a harmless backoff reset), and it
	// matches the LocalIPCTransport, which delivers with no such guard at all.
	var closed atomic.Bool
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := t.channel.SendRPCNoWait(ctx, "WatchEvents", payload, tunnel.RPCHandlers{
		Response: respCh,
		Stream: func(msg *leapmuxv1.InnerStreamMessage) {
			if closed.Load() {
				return
			}
			// A terminal frame ends the subscription. Without this the
			// error envelope was handed to decodeWatchFrame, which parsed
			// its empty payload into a blank WatchEventsResponse and
			// delivered it as if it were data -- so a stream the worker had
			// already ended kept the CLI waiting, and the reason never
			// reached the log. Only the unary respCh arm below was ever
			// treated as terminal, and a streaming handler no longer
			// answers that way.
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
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			// Clean shutdown: caller invoked cancel.
		case <-t.channel.Context().Done():
			// Channel disconnected. Caller observes via Done().
		case resp := <-respCh:
			// Server returned a non-stream response (typically an
			// error envelope). Treat as terminal — the consumer
			// observes via Done() and may resubscribe — but surface
			// WHY, or the subscription dies with no diagnostic.
			if resp.GetIsError() {
				logTerminalStreamError(transportLogger(t.logger), "channel", resp.GetErrorCode(), resp.GetErrorMessage())
			}
		}
		// Flip `closed` BEFORE unregistering/closing so any frame the demux delivers
		// from here on drops itself (see the cb guard above). A frame already inside
		// onFrame is NOT waited for -- that is what keeps teardown from deadlocking
		// behind a back-pressured stdout encode.
		closed.Store(true)
		t.channel.UnregisterStream(reqID)
		t.channel.UnregisterPending(reqID)
		cancel()
	}()
	return cancel, done, nil
}

// LocalIPCTransport runs a WatchEvents subscription via the per-agent
// IPC server's StreamInner method. Used by worker-spawned CLI mode
// (`LEAPMUX_REMOTE_SOCK`). The router on the worker side proxies the
// stream to the appropriate inner-RPC handler.
type LocalIPCTransport struct {
	client      leapmuxv1connect.RemoteIPCServiceClient
	workspaceID string
	// targetWorkerID is the worker the WatchEvents subscription is
	// for. Local-IPC routes to the spawning worker by default; this
	// lets `events --include agent,terminal` direct subscriptions to
	// sibling workers via the router's cross-worker dispatch.
	targetWorkerID string
	logger         *slog.Logger
}

// NewLocalIPCTransport wires the local-IPC client + workspace + target. A nil
// logger falls back to slog.Default() so a malformed frame is never nil-deref.
func NewLocalIPCTransport(client leapmuxv1connect.RemoteIPCServiceClient, workspaceID, targetWorkerID string, logger *slog.Logger) *LocalIPCTransport {
	return &LocalIPCTransport{client: client, workspaceID: workspaceID, targetWorkerID: targetWorkerID, logger: transportLogger(logger)}
}

// OpenWatchEvents implements Transport.
func (t *LocalIPCTransport) OpenWatchEvents(parentCtx context.Context, req *leapmuxv1.WatchEventsRequest,
	onFrame func(*leapmuxv1.WatchEventsResponse),
) (context.CancelFunc, <-chan struct{}, error) {
	if t.client == nil {
		return nil, nil, errors.New("nil RemoteIPCService client")
	}
	payload, err := proto.Marshal(req)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(parentCtx)
	stream, err := t.client.StreamInner(ctx, connect.NewRequest(&leapmuxv1.StreamInnerRequest{
		Method:         "worker.WatchEvents",
		Payload:        payload,
		TargetWorkerId: t.targetWorkerID,
		WorkspaceId:    t.workspaceID,
	}))
	if err != nil {
		cancel()
		return nil, nil, err
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
		// Receive returned false; stream ended (or errored).
		// The caller's Done() observer notices and can decide to retry.
	}()
	// Release the ctx child on every exit path, exactly as the ChannelTransport
	// sibling does. The caller's cancel is the ONLY other release, and a consumer
	// that observes Done() and stops -- rather than resubscribing -- never calls it,
	// leaving this child attached to a long-lived parent for the process lifetime.
	// Cancelling a stream that has already ended is a no-op, so this is safe to run
	// ahead of (or instead of) the caller's cancel.
	go func() {
		<-done
		cancel()
	}()
	return cancel, done, nil
}
