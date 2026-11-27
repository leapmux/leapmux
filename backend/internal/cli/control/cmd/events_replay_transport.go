package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/tunnel"
)

const semanticWatchProcedure = leapmuxv1connect.WorkerWatchServiceWatchSemanticEventsProcedure

type semanticWatchChannel interface {
	Context() context.Context
	SendRPCNoWait(context.Context, string, []byte, tunnel.RPCHandlers) (uint64, error)
	UnregisterStream(uint64)
}

type eventFrame struct {
	response *leapmuxv1.WatchSemanticEventsResponse
	err      error
	end      bool
}

// remoteStreamEndedError marks a stream that the Worker or channel ended while
// the caller's context was still active. Unwrap preserves the underlying io.EOF
// or Connect cancellation error while CLIError classifies it as retryable
// CodeUnavailable without misclassifying local context cancellation.
type remoteStreamEndedError struct {
	cause error
}

func (e *remoteStreamEndedError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	return "the Worker ended the event history stream"
}

func (e *remoteStreamEndedError) Unwrap() error { return e.cause }

func (e *remoteStreamEndedError) CLIError() *cli.Error {
	return cli.Newf(cli.CodeUnavailable, "the Worker ended the event history stream")
}

var _ cli.Classifier = (*remoteStreamEndedError)(nil)

func semanticFrameError(rpcError *leapmuxv1.RpcError) error {
	if err := channelwire.ErrorOf(rpcError); err != nil {
		return inBandError(err)
	}
	return nil
}

// streamWorkerInner streams inner RPC frames from workerID and passes each
// non-empty payload to onFrame. It delivers any non-empty payload on an end
// frame before returning a remoteStreamEndedError wrapping io.EOF.
func streamWorkerInner(ctx context.Context, c control.Peer, workerID, procedure string, payload []byte, onFrame func(raw []byte) error) error {
	return streamWorkerInnerRaw(ctx, c, workerID, procedure, payload, func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
		if err := semanticFrameError(rpcError); err != nil {
			return err
		}
		if len(raw) != 0 {
			if err := onFrame(raw); err != nil {
				return err
			}
		}
		if end {
			return io.EOF
		}
		return nil
	})
}

func (s *session) receiveSemanticWatch(ctx context.Context, workerID string, request *leapmuxv1.WatchSemanticEventsRequest, out chan<- eventFrame) error {
	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	forward := func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
		if err := semanticFrameError(rpcError); err != nil {
			return err
		}
		if len(raw) != 0 {
			var response leapmuxv1.WatchSemanticEventsResponse
			if err := proto.Unmarshal(raw, &response); err != nil || response.GetEvent() == nil {
				return cli.Newf(cli.CodeInternal, "the Worker sent an invalid event history frame")
			}
			select {
			case out <- eventFrame{response: &response, end: end}:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		if end {
			return io.EOF
		}
		return nil
	}
	return streamWorkerInnerRaw(ctx, s.c, workerID, semanticWatchProcedure, payload, forward)
}

func streamWorkerInnerRaw(ctx context.Context, peer control.Peer, workerID, procedure string, payload []byte,
	forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	switch c := peer.(type) {
	case *control.WorkerIPCClient:
		return streamWorkerLocalIPCRaw(ctx, c, workerID, procedure, payload, forward)
	case *control.HubClient:
		openCtx, cancelOpen := rpcDeadline(ctx)
		ch, err := c.OpenE2EEChannel(openCtx, ctx, workerID)
		cancelOpen()
		if err != nil {
			return err
		}
		defer ch.Close()
		return readEncryptedSemanticWatch(ctx, ch, procedure, payload, forward)
	default:
		return fmt.Errorf("unsupported control peer %T", peer)
	}
}

func streamWorkerLocalIPCRaw(ctx context.Context, c *control.WorkerIPCClient, workerID, procedure string, payload []byte,
	forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	client, err := c.ControlIPCStreamService()
	if err != nil {
		return err
	}
	stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{
		Method: "worker." + procedure, TargetWorkerId: workerID,
		Payload: payload, ClientRequestId: id.Generate(),
	})
	if err != nil {
		return err
	}
	defer func() { _ = stream.Close() }()
	for {
		frame, err := stream.Receive()
		if err != nil {
			if ctx.Err() == nil && (errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled) {
				return &remoteStreamEndedError{cause: err}
			}
			return err
		}
		if err := forward(frame.GetPayload(), frame.GetError(), frame.GetEnd()); err != nil {
			if ctx.Err() == nil && errors.Is(err, io.EOF) {
				return &remoteStreamEndedError{cause: err}
			}
			return err
		}
	}
}

func readEncryptedSemanticWatch(ctx context.Context, ch semanticWatchChannel, procedure string, payload []byte,
	forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	return readEncryptedStream(ctx, ch, procedure, payload, nil, forward)
}

// readEncryptedStream registers a streaming RPC on ch and delivers frames into
// forward. When overflowErr is nil, backpressure applies on the channel
// callback (preserving every frame for durable replay). When overflowErr is
// non-nil, a full handoff buffer cancels only this stream and returns
// overflowErr so a stalled consumer cannot wedge sibling streams on a shared
// channel.
func readEncryptedStream(ctx context.Context, ch semanticWatchChannel, procedure string, payload []byte,
	overflowErr error, forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	parentCtx := ctx
	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()
	type delivery struct {
		message *leapmuxv1.InnerStreamMessage
		err     error
	}
	bufSize := 32
	if overflowErr != nil {
		bufSize = 64
	}
	handoff := make(chan delivery, bufSize)
	channelDone := ch.Context().Done()
	var overflowed atomic.Bool
	deliver := func(result delivery) {
		if overflowErr != nil {
			select {
			case handoff <- result:
			case <-ctx.Done():
			case <-channelDone:
			default:
				overflowed.Store(true)
				cancel()
			}
			return
		}
		select {
		case handoff <- result:
		case <-ctx.Done():
		case <-channelDone:
		}
	}
	startCtx, cancelStart := rpcDeadline(ctx)
	requestID, err := ch.SendRPCNoWait(startCtx, procedure, payload, tunnel.RPCHandlers{
		Stream: func(message *leapmuxv1.InnerStreamMessage) {
			owned := &leapmuxv1.InnerStreamMessage{
				End: message.GetEnd(), Payload: bytes.Clone(message.GetPayload()),
			}
			if rpcError := message.GetError(); rpcError != nil {
				owned.Error = &leapmuxv1.RpcError{Code: rpcError.GetCode(), Message: rpcError.GetMessage()}
			}
			deliver(delivery{message: owned})
		},
		StreamRejected: func(err error) {
			deliver(delivery{err: cli.Wrap(cli.CodeConflict, err, "the event history frame could not cross the channel")})
		},
	})
	cancelStart()
	if err != nil {
		return err
	}
	defer func() {
		cancel()
		ch.UnregisterStream(requestID)
	}()
	process := func(result delivery) error {
		if result.err != nil {
			return result.err
		}
		frame := result.message
		err := forward(frame.GetPayload(), frame.GetError(), frame.GetEnd())
		if err != nil && parentCtx.Err() == nil && errors.Is(err, io.EOF) {
			return &remoteStreamEndedError{cause: err}
		}
		return err
	}
	for {
		select {
		case <-ctx.Done():
			if overflowed.Load() && parentCtx.Err() == nil {
				return overflowErr
			}
			return ctx.Err()
		case result := <-handoff:
			if err := process(result); err != nil {
				return err
			}
		case <-channelDone:
			// Drain every buffered delivery before reporting EOF so frames that
			// arrived just before channel teardown are not dropped.
			for {
				if parentCtx.Err() != nil {
					return parentCtx.Err()
				}
				select {
				case result := <-handoff:
					if err := process(result); err != nil {
						return err
					}
				default:
					return &remoteStreamEndedError{cause: io.EOF}
				}
			}
		}
	}
}

func eventReplayTransportError(err error) *cli.Error {
	if err == nil {
		return cli.Newf(cli.CodeUnavailable, "the Worker ended the event history stream")
	}
	return cli.FromError(err)
}
