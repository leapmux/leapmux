package cmd

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/hubrpc"
)

// hubCallUnary invokes a hub-side WorkspaceService / WorkerManagementService
// method using whichever transport the client is using.
//
// Hub-bound clients (`--hub <url>`) speak ConnectRPC straight to the hub.
// Local-IPC clients (worker-spawned agent / terminal) speak through the
// per-agent socket; the worker's ControlIPC router dispatches "hub.<procedure>"
// calls via the worker-side delegation bearer (mints lazily on first use).
//
// Without this dispatch agents could only invoke worker-scoped inner RPCs;
// hub-bound orchestration steps (SubmitOps for tile/tab mutations,
// CreateWorkspace, DeleteWorkspace, …) would silently no-op when invoked
// from inside an agent. The plan calls these out as the second half of
// every "two-step orchestration" (tab open and tab close, for both agent
// and terminal tabs), so we route them uniformly.
//
// procedure is a generated leapmuxv1connect.*Procedure constant, the key of
// an `internal/hubrpc.Registry` entry, which the worker-side ControlIPC
// bridge also reads — one place to add or rename a hub method, and a
// renamed one fails to compile here.
func hubCallUnary(ctx context.Context, c *control.Client, procedure string, in proto.Message, out proto.Message) error {
	if !c.IsWorkerIPC() {
		return hubCallDirect(ctx, c, procedure, in, out)
	}
	return hubCallLocalIPC(ctx, c, procedure, in, out)
}

func hubCallLocalIPC(ctx context.Context, c *control.Client, procedure string, in proto.Message, out proto.Message) error {
	payload, err := proto.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", procedure, err)
	}
	ipc, err := c.ControlIPCService()
	if err != nil {
		return err
	}
	resp, err := ipc.CallInner(ctx, &leapmuxv1.CallInnerRequest{
		Method:  "hub." + procedure,
		Payload: payload,
	})
	if err != nil {
		return err
	}
	if err := channelwire.ErrorOf(resp.GetError()); err != nil {
		// Keep the in-band code: cli.FromError reads connect.CodeOf, so
		// flattening to a bare message here made `not_found` unreachable on
		// this transport. See inBandError in innerrpc.go.
		return inBandError(err)
	}
	if out != nil && len(resp.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.GetPayload(), out); err != nil {
			return fmt.Errorf("unmarshal %s response: %w", procedure, err)
		}
	}
	return nil
}

// hubCallDirect dispatches a hub-bound RPC over the direct ConnectRPC
// transport. The shared `hubrpc.Registry` lookup handles the typed
// dispatch; we contribute the transport and the session-auth interceptor
// that `c.WorkspaceService()` & friends would otherwise apply.
func hubCallDirect(ctx context.Context, c *control.Client, procedure string, in proto.Message, out proto.Message) error {
	desc, err := hubrpc.Lookup(procedure)
	if err != nil {
		return fmt.Errorf("unsupported hub method: %w", err)
	}
	client := connect.NewClient(
		connecthttp.NewTransport(c.HTTPClient, c.ConnectURL(), connecthttp.WithReadMaxBytes(0)),
		c.AuthInterceptor(),
	)
	return desc.Invoke(ctx, client, in, out)
}
