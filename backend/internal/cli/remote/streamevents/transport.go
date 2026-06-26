package streamevents

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
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

// channelLike is the subset of `*tunnel.Channel` Transport needs.
// Pulled into an interface so tests don't need a real Noise_NK
// responder; production wires it to *tunnel.Channel directly.
type channelLike interface {
	SendRPCNoWait(method string, payload []byte, pendingCh ...chan *leapmuxv1.InnerRpcResponse) (uint32, error)
	RegisterStream(reqID uint32, cb func(*leapmuxv1.InnerStreamMessage))
	UnregisterStream(reqID uint32)
	UnregisterPending(reqID uint32)
	Context() context.Context
	Close()
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
	respCh := make(chan *leapmuxv1.InnerRpcResponse, 1)
	reqID, err := t.channel.SendRPCNoWait("WatchEvents", payload, respCh)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	// `closed` lets a frame already in flight when teardown runs observe that the
	// subscription has ended and drop itself: the channel demux invokes this cb from
	// its OWN goroutine and releases the channel lock before calling it, so a frame
	// can race teardown.
	//
	// The mutex is held ONLY across the `closed` check, NOT across `onFrame`. An
	// earlier version held it across onFrame to guarantee no frame is delivered after
	// Done() closes, but that deadlocks: onFrame chains into the consumer's synchronous
	// stdout encode, and if stdout back-pressures (a paused `--follow` reader) the cb
	// blocks holding the mutex, so the teardown goroutine below can never acquire it to
	// set `closed`/`cancel()` -- Done() never closes and Cancel()/Update() hang forever.
	// Releasing before onFrame lets teardown close `done` promptly while a blocked frame
	// drains on its own. The cost is a narrow window where a late frame's onFrame runs
	// just after Done() closes; consumers already tolerate this (the `agent messages
	// --follow` loop treats a late `delivered` flip as a harmless backoff reset), and it
	// matches the LocalIPCTransport, which delivers with no such guard at all.
	var mu sync.Mutex
	closed := false
	t.channel.RegisterStream(reqID, func(msg *leapmuxv1.InnerStreamMessage) {
		mu.Lock()
		isClosed := closed
		mu.Unlock()
		if isClosed {
			return
		}
		resp, ok := decodeWatchFrame(t.logger, "channel", msg.GetPayload())
		if !ok {
			return
		}
		onFrame(resp)
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			// Clean shutdown: caller invoked cancel.
		case <-t.channel.Context().Done():
			// Channel disconnected. Caller observes via Done().
		case <-respCh:
			// Server returned a non-stream response (typically an
			// error envelope). Treat as terminal — the consumer
			// observes via Done() and may resubscribe.
		}
		// Flip `closed` BEFORE unregistering/closing so any frame the demux delivers
		// from here on drops itself (see the cb guard above). A frame already inside
		// onFrame is NOT waited for -- that is what keeps teardown from deadlocking
		// behind a back-pressured stdout encode.
		mu.Lock()
		closed = true
		mu.Unlock()
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
	return cancel, done, nil
}
