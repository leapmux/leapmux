package streamevents

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

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
}

// NewChannelTransport wraps ch.
func NewChannelTransport(ch channelLike) *ChannelTransport {
	return &ChannelTransport{channel: ch}
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
	t.channel.RegisterStream(reqID, func(msg *leapmuxv1.InnerStreamMessage) {
		var resp leapmuxv1.WatchEventsResponse
		if uerr := proto.Unmarshal(msg.GetPayload(), &resp); uerr != nil {
			return
		}
		onFrame(&resp)
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
}

// NewLocalIPCTransport wires the local-IPC client + workspace + target.
func NewLocalIPCTransport(client leapmuxv1connect.RemoteIPCServiceClient, workspaceID, targetWorkerID string) *LocalIPCTransport {
	return &LocalIPCTransport{client: client, workspaceID: workspaceID, targetWorkerID: targetWorkerID}
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
			var resp leapmuxv1.WatchEventsResponse
			if uerr := proto.Unmarshal(env.GetPayload(), &resp); uerr != nil {
				continue
			}
			onFrame(&resp)
			if env.GetEnd() {
				return
			}
		}
		// Receive returned false; stream ended (or errored).
		// The caller's Done() observer notices and can decide to retry.
	}()
	return cancel, done, nil
}
