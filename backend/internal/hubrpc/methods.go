// Package hubrpc declares the registry of hub-side RPC methods that
// external callers (the `leapmux remote` CLI direct mode, and the
// worker's RemoteIPC bridge proxying for agent-spawned CLI calls)
// invoke. Both callers share this single source of truth so adding a
// hub method requires editing one entry instead of two parallel
// switches.
package hubrpc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// Descriptor describes how to invoke one hub method generically.
type Descriptor struct {
	// NewRequest returns a fresh zero-value request proto so callers
	// receiving a raw `[]byte` payload (worker RemoteIPC bridge) can
	// Unmarshal into it.
	NewRequest func() proto.Message
	// NewResponse returns a fresh zero-value response proto for the
	// same reason.
	NewResponse func() proto.Message
	// Procedure is the ConnectRPC procedure URL (e.g.
	// "/leapmux.v1.WorkspaceService/GetTab") used by Invoke to address
	// the right hub service.
	Procedure string
	// Invoke runs the unary call. Both in and out MUST be the proto
	// types returned by NewRequest / NewResponse — Invoke casts them
	// back to the concrete types so `connect.NewClient` gets typed
	// generics. Authentication is wired through opts (caller supplies
	// the right interceptor: bearer-per-call for the worker bridge,
	// session-cookie for the CLI).
	Invoke func(ctx context.Context, httpClient *http.Client, baseURL string, in, out proto.Message, opts ...connect.ClientOption) error
}

// Registry is the single source of truth for hub methods. Adding a
// method only requires one entry.
var Registry = map[string]Descriptor{
	"GetTab":          mk(leapmuxv1connect.WorkspaceServiceGetTabProcedure, func() proto.Message { return &leapmuxv1.GetTabRequest{} }, func() proto.Message { return &leapmuxv1.GetTabResponse{} }, callTyped[leapmuxv1.GetTabRequest, leapmuxv1.GetTabResponse]),
	"LocateTab":       mk(leapmuxv1connect.WorkspaceServiceLocateTabProcedure, func() proto.Message { return &leapmuxv1.LocateTabRequest{} }, func() proto.Message { return &leapmuxv1.LocateTabResponse{} }, callTyped[leapmuxv1.LocateTabRequest, leapmuxv1.LocateTabResponse]),
	"LocateTile":      mk(leapmuxv1connect.WorkspaceServiceLocateTileProcedure, func() proto.Message { return &leapmuxv1.LocateTileRequest{} }, func() proto.Message { return &leapmuxv1.LocateTileResponse{} }, callTyped[leapmuxv1.LocateTileRequest, leapmuxv1.LocateTileResponse]),
	"ListTabs":        mk(leapmuxv1connect.WorkspaceServiceListTabsProcedure, func() proto.Message { return &leapmuxv1.ListTabsRequest{} }, func() proto.Message { return &leapmuxv1.ListTabsResponse{} }, callTyped[leapmuxv1.ListTabsRequest, leapmuxv1.ListTabsResponse]),
	"SubmitOps":       mk(leapmuxv1connect.OrgCRDTSubmitOpsProcedure, func() proto.Message { return &leapmuxv1.SubmitOpsRequest{} }, func() proto.Message { return &leapmuxv1.SubmitOpsResponse{} }, callTyped[leapmuxv1.SubmitOpsRequest, leapmuxv1.SubmitOpsResponse]),
	"UpdatePresence":  mk(leapmuxv1connect.OrgCRDTUpdatePresenceProcedure, func() proto.Message { return &leapmuxv1.UpdatePresenceRequest{} }, func() proto.Message { return &leapmuxv1.UpdatePresenceResponse{} }, callTyped[leapmuxv1.UpdatePresenceRequest, leapmuxv1.UpdatePresenceResponse]),
	"GetMaterialized": mk(leapmuxv1connect.OrgCRDTGetMaterializedProcedure, func() proto.Message { return &leapmuxv1.GetMaterializedRequest{} }, func() proto.Message { return &leapmuxv1.GetMaterializedResponse{} }, callTyped[leapmuxv1.GetMaterializedRequest, leapmuxv1.GetMaterializedResponse]),
	"ListWorkspaces":  mk(leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure, func() proto.Message { return &leapmuxv1.ListWorkspacesRequest{} }, func() proto.Message { return &leapmuxv1.ListWorkspacesResponse{} }, callTyped[leapmuxv1.ListWorkspacesRequest, leapmuxv1.ListWorkspacesResponse]),
	"GetWorkspace":    mk(leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure, func() proto.Message { return &leapmuxv1.GetWorkspaceRequest{} }, func() proto.Message { return &leapmuxv1.GetWorkspaceResponse{} }, callTyped[leapmuxv1.GetWorkspaceRequest, leapmuxv1.GetWorkspaceResponse]),
	"CreateWorkspace": mk(leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure, func() proto.Message { return &leapmuxv1.CreateWorkspaceRequest{} }, func() proto.Message { return &leapmuxv1.CreateWorkspaceResponse{} }, callTyped[leapmuxv1.CreateWorkspaceRequest, leapmuxv1.CreateWorkspaceResponse]),
	"RenameWorkspace": mk(leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure, func() proto.Message { return &leapmuxv1.RenameWorkspaceRequest{} }, func() proto.Message { return &leapmuxv1.RenameWorkspaceResponse{} }, callTyped[leapmuxv1.RenameWorkspaceRequest, leapmuxv1.RenameWorkspaceResponse]),
	"DeleteWorkspace": mk(leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure, func() proto.Message { return &leapmuxv1.DeleteWorkspaceRequest{} }, func() proto.Message { return &leapmuxv1.DeleteWorkspaceResponse{} }, callTyped[leapmuxv1.DeleteWorkspaceRequest, leapmuxv1.DeleteWorkspaceResponse]),
	"ListWorkers":     mk(leapmuxv1connect.WorkerManagementServiceListWorkersProcedure, func() proto.Message { return &leapmuxv1.ListWorkersRequest{} }, func() proto.Message { return &leapmuxv1.ListWorkersResponse{} }, callTyped[leapmuxv1.ListWorkersRequest, leapmuxv1.ListWorkersResponse]),
	"GetWorker":       mk(leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure, func() proto.Message { return &leapmuxv1.GetWorkerRequest{} }, func() proto.Message { return &leapmuxv1.GetWorkerResponse{} }, callTyped[leapmuxv1.GetWorkerRequest, leapmuxv1.GetWorkerResponse]),
	"GetUser":         mk(leapmuxv1connect.UserServiceGetUserProcedure, func() proto.Message { return &leapmuxv1.GetUserRequest{} }, func() proto.Message { return &leapmuxv1.GetUserResponse{} }, callTyped[leapmuxv1.GetUserRequest, leapmuxv1.GetUserResponse]),
}

// mk builds a Descriptor that closes the procedure URL into Invoke
// so the registry entries stay one-line.
func mk(procedure string, newReq, newResp func() proto.Message, call typedCaller) Descriptor {
	return Descriptor{
		NewRequest:  newReq,
		NewResponse: newResp,
		Procedure:   procedure,
		Invoke: func(ctx context.Context, httpClient *http.Client, baseURL string, in, out proto.Message, opts ...connect.ClientOption) error {
			return call(ctx, httpClient, baseURL+procedure, in, out, opts...)
		},
	}
}

// typedCaller is the shape of every callTyped[Req, Resp] instantiation.
type typedCaller func(ctx context.Context, httpClient *http.Client, fullURL string, in, out proto.Message, opts ...connect.ClientOption) error

// callTyped is the generic body that constructs a typed
// `connect.NewClient[*Req, *Resp]`, dispatches, and copies the response
// back into `out` via proto.Merge. Each Registry entry instantiates
// this with the concrete Req/Resp pair.
func callTyped[Req, Resp any](ctx context.Context, httpClient *http.Client, fullURL string, in, out proto.Message, opts ...connect.ClientOption) error {
	// Type parameters are `any` (proto.Message requires a pointer
	// receiver, which Go generics can't express directly), so the
	// runtime assertion happens through any().
	typedIn, ok := any(in).(*Req)
	if !ok {
		return fmt.Errorf("hubrpc: request type mismatch for %T", in)
	}
	if _, ok := any(out).(*Resp); !ok {
		return fmt.Errorf("hubrpc: response type mismatch for %T", out)
	}
	client := connect.NewClient[Req, Resp](httpClient, fullURL, opts...)
	req := connect.NewRequest(typedIn)
	resp, err := client.CallUnary(ctx, req)
	if err != nil {
		return err
	}
	// proto.Merge handles the response copy without a Marshal /
	// Unmarshal round-trip.
	respMsg, ok := any(resp.Msg).(proto.Message)
	if !ok {
		return errors.New("hubrpc: response is not a proto.Message — generated client mismatch")
	}
	proto.Reset(out)
	proto.Merge(out, respMsg)
	return nil
}

// Lookup returns the descriptor for method or an "not implemented"
// error. Caller is responsible for surfacing the error in whatever
// shape its transport demands.
func Lookup(method string) (Descriptor, error) {
	d, ok := Registry[method]
	if !ok {
		return Descriptor{}, fmt.Errorf("hub method not implemented: %s", method)
	}
	return d, nil
}
