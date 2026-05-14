package cmd

import (
	"context"
	"errors"
	"flag"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/internal/hubrpc"
)

// parseHubOnly is the canonical prologue for "hub-only" commands
// (auth logout / status, worker pins list/show/remove) that bind no
// entity flags. Returns the resolved --hub URL or a remote.EmitError
// envelope when --hub is missing.
//
// Callers that need to register one or two extra flags pass an
// `extra` callback; the helper hands it the FlagSet AFTER --hub
// binding so the extra flags appear under "--hub" in --help output.
// Pass nil when no extra flags are needed.
func parseHubOnly(rawCtx any, args []string, extra func(fs *flag.FlagSet)) (string, error) {
	cmd := asCtx(rawCtx)
	var hub string
	fs := flagSet(cmd, &hub)
	if extra != nil {
		extra(fs)
	}
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return "", err
	}
	if hub == "" {
		return "", remote.EmitError("invalid_request", "--hub is required")
	}
	return hub, nil
}

// hubCallUnaryEmitOn dispatches a hub-bound unary RPC over an
// already-open client and emits the response — the closure form
// for resolveAndEmit bodies. Equivalent to the inline trio
//
//	if err := hubCallUnary(...); err != nil {
//	    return remote.EmitErrorWith(classifyHubError(err), err)
//	}
//	return remote.EmitData(shape())
//
// used by every read-only resolver-driven CLI verb (RunWorkerGet,
// RunTabGet, RunWorkspaceGet, …). Mirrors workerUnaryEmitOn for the
// hub side.
func hubCallUnaryEmitOn(ctx context.Context, c *remote.Client, method, workspaceID string, in, out proto.Message, shape func() any) error {
	if err := hubCallUnary(ctx, c, method, workspaceID, in, out); err != nil {
		return remote.EmitErrorWith(classifyHubError(err), err)
	}
	return remote.EmitData(shape())
}

// hubCallUnary invokes a hub-side WorkspaceService / WorkerManagementService
// method using whichever transport the client is using.
//
// Hub-bound clients (`--hub <url>`) speak ConnectRPC straight to the hub.
// Local-IPC clients (worker-spawned agent / terminal) speak through the
// per-agent socket; the worker's RemoteIPC router dispatches "hub.<Method>"
// calls via the worker-side delegation bearer (mints lazily on first use).
//
// Without this dispatch agents could only invoke worker-scoped inner RPCs;
// hub-bound orchestration steps (SubmitOps for tile/tab mutations,
// CreateWorkspace, DeleteWorkspace, …) would silently no-op when invoked
// from inside an agent. The plan calls these out as the second half of
// every "two-step orchestration" (agent open, agent close, terminal open,
// terminal close, tab close), so we route them uniformly.
//
// Method names map 1:1 onto `internal/hubrpc.Registry` entries, which
// the worker-side RemoteIPC bridge also reads — one place to add or
// rename a hub method.
func hubCallUnary(ctx context.Context, c *remote.Client, method, workspaceID string, in proto.Message, out proto.Message) error {
	if !c.IsLocal() {
		return hubCallDirect(ctx, c, method, in, out)
	}
	return hubCallLocalIPC(ctx, c, method, workspaceID, in, out)
}

func hubCallLocalIPC(ctx context.Context, c *remote.Client, method, workspaceID string, in proto.Message, out proto.Message) error {
	payload, err := proto.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal %s request: %w", method, err)
	}
	resp, err := c.RemoteIPCService().CallInner(ctx, connect.NewRequest(&leapmuxv1.CallInnerRequest{
		Method:      "hub." + method,
		Payload:     payload,
		WorkspaceId: workspaceID,
	}))
	if err != nil {
		return err
	}
	if resp.Msg.GetIsError() {
		return errors.New(resp.Msg.GetErrorMessage())
	}
	if out != nil && len(resp.Msg.GetPayload()) > 0 {
		if err := proto.Unmarshal(resp.Msg.GetPayload(), out); err != nil {
			return fmt.Errorf("unmarshal %s response: %w", method, err)
		}
	}
	return nil
}

// hubCallDirect dispatches a hub-bound RPC over the direct ConnectRPC
// transport. The shared `hubrpc.Registry` lookup handles the typed
// `connect.NewClient[Req, Resp]` instantiation; we contribute the
// session-auth interceptor that `c.WorkspaceService()` & friends
// would otherwise apply.
func hubCallDirect(ctx context.Context, c *remote.Client, method string, in proto.Message, out proto.Message) error {
	desc, err := hubrpc.Lookup(method)
	if err != nil {
		return fmt.Errorf("unsupported hub method: %w", err)
	}
	return desc.Invoke(ctx, c.HTTPClient, c.ConnectURL(), in, out, connect.WithInterceptors(c.AuthInterceptor()))
}

// hubUnaryEmit collapses the boilerplate every read-only / single-shot
// hub-bound CLI verb shares: open the client, call the hub method,
// emit the response payload — or emit a `rpc_failed` envelope on
// transport/server error. `shape()` returns the JSON-emittable view
// of `resp`; callers typically project a single field (e.g.
// `resp.GetWorkspace()`) so the wire output stays focused.
//
// `workspaceID` is the local-IPC routing key — pass the workspace id
// when the call has one (`GetWorkspace`, `RenameWorkspace`, …) or ""
// when it doesn't (`ListWorkspaces`).
//
// Use this for commands that:
//   - Bind a small flag set and validate it up front
//   - Need one hub RPC and emit its result
//   - Don't have post-RPC orchestration (worker fan-out, CRDT batches)
//
// Commands that do post-RPC work (e.g. `WorkspaceDelete`'s cleanup
// fan-out) stay on `hubCallUnary` directly so the extra logic stays
// at the call site.
func hubUnaryEmit(hub, method, workspaceID string, req, resp proto.Message, shape func() any) error {
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	if err := hubCallUnary(context.Background(), c, method, workspaceID, req, resp); err != nil {
		return remote.EmitErrorWith(classifyHubError(err), err)
	}
	return remote.EmitData(shape())
}

// classifyHubError maps a connect error code to the CLI envelope's
// `code` field. NotFound / PermissionDenied collapse to `not_found`
// so scripts can tell "no such entity" apart from a transport / 5xx
// failure without parsing the message. Everything else stays as
// `rpc_failed`.
func classifyHubError(err error) string {
	if isNotFoundOrForbidden(err) {
		return "not_found"
	}
	return "rpc_failed"
}

// workerUnaryEmit collapses the parallel boilerplate every worker-bound
// inner-RPC CLI verb shares: open the client, apply the rpcDeadline,
// invoke the worker's E2EE channel, emit the response payload. Mirrors
// `hubUnaryEmit` but routes through `callInnerRPC` instead of the hub
// dispatch table.
//
// Worker inner-RPCs return their error directly via callInnerRPC (it
// already wraps in a coded envelope), so the caller surfaces whatever
// `err` comes back unaltered — no `rpc_failed` re-wrap.
func workerUnaryEmit(hub, workerID, method string, req, resp proto.Message, shape func() any) error {
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	return workerUnaryEmitOn(c, workerID, method, req, resp, shape)
}

// workerUnaryEmitOn is workerUnaryEmit's already-have-a-client twin.
// Callers that already opened the client (via resolveWorker) reuse it
// instead of paying the credential-load + HTTP-transport + TOFU pin
// store construction a second time per command.
func workerUnaryEmitOn(c *remote.Client, workerID, method string, req, resp proto.Message, shape func() any) error {
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	// Worker-bound commands (file list, git status, …) all flow
	// through here. Preflight workerID once so the user gets a
	// clear "no such worker" before the E2EE channel handshake
	// produces a less actionable transport error.
	if err := maybePreflightWorker(ctx, c, workerID); err != nil {
		return err
	}
	if err := callInnerRPC(ctx, c, workerID, method, req, resp); err != nil {
		return err
	}
	return remote.EmitData(shape())
}

// resolveAndEmit collapses the entity-resolution scaffolding every
// resolver-driven CLI handler shares: open the hub client, apply
// rpcDeadline, run `resolve.Resolve` over `need`+`in`, then dispatch
// to `fn` with the resolved entities.
//
// Errors from requireClient / runResolve are already JSON-emitted
// envelopes (the helpers route through remote.EmitError), so the
// caller surfaces them unaltered. `fn` owns the post-resolve work
// (preflight, inner-RPC, op-batch submit, EmitData).
//
// Convert a handler that today opens its own client + ctx + runResolve
// trio into:
//
//	return resolveAndEmit(hub, resolve.Need{...}, in, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
//	    // post-resolve body
//	})
//
// The closure boundary is the only change visible to existing
// post-resolve logic; the resolver/preflight contract is unchanged.
func resolveAndEmit(hub string, need resolve.Need, in resolve.Inputs, fn func(ctx context.Context, c *remote.Client, got resolve.Resolved) error) error {
	c, err := requireClient(hub)
	if err != nil {
		return err
	}
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	got, err := runResolve(ctx, c, need, in)
	if err != nil {
		return err
	}
	return fn(ctx, c, got)
}
