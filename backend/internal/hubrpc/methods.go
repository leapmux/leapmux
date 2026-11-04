// Package hubrpc declares the registry of hub-side RPC methods that
// external callers (the `leapmux control` CLI direct mode, and the
// worker's ControlIPC bridge proxying for agent-spawned CLI calls)
// invoke. Both callers share this single source of truth so adding a
// hub method requires editing one entry instead of two parallel
// switches.
//
// A caller addresses a hub method by its full procedure path, a generated
// leapmuxv1connect.*Procedure constant, as it addresses a worker method. A
// renamed or mistyped method then fails to compile instead of failing at run
// time with "hub method not implemented".
package hubrpc

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
)

// WatchUserStream is the control-IPC address of the hub's one stream, the
// user-event feed. The feed is the /ws/userevents WebSocket, not a Connect
// procedure, so no leapmuxv1connect constant exists for it: its address is
// that route. It is not a Registry entry, because the worker's hub bridge
// streams it over the WebSocket instead of calling a unary method.
const WatchUserStream = contracts.WSRouteUserEvents

// Descriptor describes how to invoke one hub method generically.
type Descriptor struct {
	// NewRequest returns a fresh zero-value request proto so callers
	// receiving a raw `[]byte` payload (worker ControlIPC bridge) can
	// Unmarshal into it.
	NewRequest func() proto.Message
	// NewResponse returns a fresh zero-value response proto for the
	// same reason.
	NewResponse func() proto.Message
	// Procedure is the ConnectRPC procedure URL (e.g.
	// "/leapmux.v1.WorkspaceService/LocateTab") used by Invoke to address
	// the right hub service.
	Procedure string
	// Invoke runs the unary call. Both in and out MUST be the proto
	// types returned by NewRequest / NewResponse — Invoke casts them
	// back to the concrete types so the dispatch is typed. The caller
	// builds client, since Connect v2 puts interceptors on the client
	// and HTTP options on its transport: bearer-per-call for the worker
	// bridge, session-cookie for the CLI.
	Invoke func(ctx context.Context, client *connect.Client, in, out proto.Message) error
}

// Registry is the single source of truth for hub methods, keyed by the full
// procedure path. Adding a method only requires one entry.
var Registry = registry(
	entry[leapmuxv1.LocateTabRequest, leapmuxv1.LocateTabResponse](leapmuxv1connect.WorkspaceServiceLocateTabProcedure),
	entry[leapmuxv1.LocateTileRequest, leapmuxv1.LocateTileResponse](leapmuxv1connect.WorkspaceServiceLocateTileProcedure),
	entry[leapmuxv1.ListTabsRequest, leapmuxv1.ListTabsResponse](leapmuxv1connect.WorkspaceServiceListTabsProcedure),
	entry[leapmuxv1.SubmitOpsRequest, leapmuxv1.SubmitOpsResponse](leapmuxv1connect.UserCRDTSubmitOpsProcedure),
	entry[leapmuxv1.UpdatePresenceRequest, leapmuxv1.UpdatePresenceResponse](leapmuxv1connect.UserCRDTUpdatePresenceProcedure),
	entry[leapmuxv1.GetMaterializedRequest, leapmuxv1.GetMaterializedResponse](leapmuxv1connect.UserCRDTGetMaterializedProcedure),
	entry[leapmuxv1.ListWorkspacesRequest, leapmuxv1.ListWorkspacesResponse](leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure),
	entry[leapmuxv1.GetWorkspaceRequest, leapmuxv1.GetWorkspaceResponse](leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure),
	entry[leapmuxv1.CreateWorkspaceRequest, leapmuxv1.CreateWorkspaceResponse](leapmuxv1connect.WorkspaceServiceCreateWorkspaceProcedure),
	entry[leapmuxv1.RenameWorkspaceRequest, leapmuxv1.RenameWorkspaceResponse](leapmuxv1connect.WorkspaceServiceRenameWorkspaceProcedure),
	entry[leapmuxv1.DeleteWorkspaceRequest, leapmuxv1.DeleteWorkspaceResponse](leapmuxv1connect.WorkspaceServiceDeleteWorkspaceProcedure),
	entry[leapmuxv1.ListWorkersRequest, leapmuxv1.ListWorkersResponse](leapmuxv1connect.WorkerManagementServiceListWorkersProcedure),
	entry[leapmuxv1.GetWorkerRequest, leapmuxv1.GetWorkerResponse](leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure),
)

// registry keys each descriptor by its full procedure path, which is the
// address a caller passes to Lookup. The path includes the service, so two
// services that declare one method name (WorkerManagementService and
// AdminWorkerService both declare ListWorkers) get two keys. It panics when
// two descriptors give one procedure, because a map would keep only the later
// one with no error.
func registry(descriptors ...Descriptor) map[string]Descriptor {
	out := make(map[string]Descriptor, len(descriptors))
	for _, descriptor := range descriptors {
		if _, ok := out[descriptor.Procedure]; ok {
			panic(fmt.Sprintf("hubrpc: two descriptors give the procedure %s", descriptor.Procedure))
		}
		out[descriptor.Procedure] = descriptor
	}
	return out
}

// entry builds the Descriptor of the unary procedure whose request is *Req and
// whose response is *Resp. It closes the procedure URL and the unary Spec into
// Invoke, so each Registry entry stays one line.
//
// PReq and PResp exist because proto.Message requires a pointer receiver, and
// a type parameter cannot state "a pointer to Req that is a proto.Message"
// without them. Go infers both from Req and Resp.
func entry[Req, Resp any, PReq interface {
	*Req
	proto.Message
}, PResp interface {
	*Resp
	proto.Message
}](procedure string) Descriptor {
	// Schema is left nil: connect v2 carries it as opaque pass-through
	// metadata, and nothing on this path reads it.
	spec := connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: procedure}
	return Descriptor{
		NewRequest:  func() proto.Message { return PReq(new(Req)) },
		NewResponse: func() proto.Message { return PResp(new(Resp)) },
		Procedure:   procedure,
		Invoke: func(ctx context.Context, client *connect.Client, in, out proto.Message) error {
			typedIn, ok := in.(PReq)
			if !ok {
				return fmt.Errorf("hubrpc: request type mismatch for %T", in)
			}
			typedOut, ok := out.(PResp)
			if !ok {
				return fmt.Errorf("hubrpc: response type mismatch for %T", out)
			}
			// Clear the previous result before the call starts. A failure
			// before decoding must not leave data from an earlier call in out.
			// typedOut and out are the same message.
			proto.Reset(typedOut)
			return client.CallUnary(ctx, spec, typedIn, typedOut)
		},
	}
}

// Lookup returns the descriptor for procedure, a full procedure path such as
// leapmuxv1connect.WorkspaceServiceLocateTabProcedure, or a "not implemented"
// error. Caller is responsible for surfacing the error in whatever shape its
// transport demands.
func Lookup(procedure string) (Descriptor, error) {
	d, ok := Registry[procedure]
	if !ok {
		return Descriptor{}, fmt.Errorf("hub method not implemented: %s", procedure)
	}
	return d, nil
}
