package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"

	"connectrpc.com/connect/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
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

func (s *session) receiveSemanticWatch(ctx context.Context, workerID string, request *leapmuxv1.WatchSemanticEventsRequest, out chan<- eventFrame) error {
	payload, err := proto.Marshal(request)
	if err != nil {
		return err
	}
	forward := func(raw []byte, rpcError *leapmuxv1.RpcError, end bool) error {
		if rpcError.GetCode() == int32(codes.ResourceExhausted) {
			return cli.Newf(cli.CodeConflict, "%s", rpcError.GetMessage())
		}
		if err := channelwire.ErrorOf(rpcError); err != nil {
			return inBandError(err)
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
	if s.c.IsWorkerIPC() {
		client, err := s.c.ControlIPCStreamService()
		if err != nil {
			return err
		}
		stream, err := client.StreamInner(ctx, &leapmuxv1.StreamInnerRequest{
			Method: "worker." + semanticWatchProcedure, TargetWorkerId: workerID,
			Payload: payload, ClientRequestId: id.Generate(),
		})
		if err != nil {
			return err
		}
		defer func() { _ = stream.Close() }()
		for {
			frame, err := stream.Receive()
			if err != nil {
				return err
			}
			if err := forward(frame.GetPayload(), frame.GetError(), frame.GetEnd()); err != nil {
				return err
			}
		}
	}
	return s.receiveEncryptedSemanticWatch(ctx, workerID, payload, forward)
}

func (s *session) receiveEncryptedSemanticWatch(ctx context.Context, workerID string, payload []byte,
	forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	openCtx, cancelOpen := rpcDeadline(ctx)
	ch, err := s.c.OpenE2EEChannel(openCtx, ctx, workerID)
	cancelOpen()
	if err != nil {
		return err
	}
	defer ch.Close()
	return readEncryptedSemanticWatch(ctx, ch, payload, forward)
}

func readEncryptedSemanticWatch(ctx context.Context, ch semanticWatchChannel, payload []byte,
	forward func([]byte, *leapmuxv1.RpcError, bool) error,
) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type delivery struct {
		message *leapmuxv1.InnerStreamMessage
		err     error
	}
	handoff := make(chan delivery, 32)
	channelDone := ch.Context().Done()
	// Backpressure applies only to this dedicated channel. Both connection
	// loss and cancellation release a callback waiting behind slow output.
	deliver := func(result delivery) {
		select {
		case handoff <- result:
		case <-ctx.Done():
		case <-channelDone:
		}
	}
	startCtx, cancelStart := rpcDeadline(ctx)
	requestID, err := ch.SendRPCNoWait(startCtx, semanticWatchProcedure, payload, tunnel.RPCHandlers{
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
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-handoff:
			if result.err != nil {
				return result.err
			}
			frame := result.message
			if err := forward(frame.GetPayload(), frame.GetError(), frame.GetEnd()); err != nil {
				return err
			}
		case <-channelDone:
			return io.EOF
		}
	}
}

func eventReplayTransportError(err error) *cli.Error {
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || connect.CodeOf(err) == connect.CodeCanceled {
		return cli.Newf(cli.CodeUnavailable, "the Worker ended the event history stream")
	}
	return cli.FromError(err)
}
