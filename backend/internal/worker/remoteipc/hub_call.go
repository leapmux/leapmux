package remoteipc

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/internal/hubrpc"
	"github.com/leapmux/leapmux/internal/worker/crossworker"
)

// HubWorkspaceBridge implements HubBridge by talking to the hub's
// WorkspaceService, WorkerManagementService, and OrgCRDT services over
// ConnectRPC, authenticated with a per-(user, workspace) delegation-token
// bearer.
//
// Method dispatch is by bare method name (the "hub." namespace prefix
// is stripped by the Router before CallHub is invoked) and follows the
// shared `internal/hubrpc.Registry` — the CLI's `hubCallDirect` walks
// the same table, so adding a hub method requires editing one entry.
//
// **Why per-method dispatch instead of a single transparent proxy?**
// The bridge is the security boundary that picks the delegation
// scope, and it has to know which workspace_id to mint against. Some
// requests carry workspace_id in the payload (GetTab, AddTab, …) and
// some don't (ListWorkspaces, ListWorkers). Per-method dispatch makes
// the policy explicit at every call site rather than buried in a
// generic proxy that would have to introspect the request.
//
// **Why mirror HubWorkspaceStreamer instead of folding both into one
// type?** Streaming and unary go through different ConnectRPC code
// paths (`stream.Receive()` loop vs. unary `Response.Msg`) and pool
// differently. Sharing a delegation provider + http transport keeps
// the duplication small while letting each lane evolve independently.
type HubWorkspaceBridge struct {
	Delegation crossworker.DelegationProvider
	HTTPClient *http.Client
	ConnectURL string
}

// NewHubWorkspaceBridge returns a bridge that mints delegation
// bearers via dp and forwards unary hub RPCs to hubURL. The transport
// matches HubWorkspaceStreamer's so both lanes share connection pool
// behavior over `unix:` / `npipe:` and real HTTPS hubs alike.
func NewHubWorkspaceBridge(hubURL string, dp crossworker.DelegationProvider) *HubWorkspaceBridge {
	httpClient, connectURL := streamClientForHubURL(hubURL)
	return &HubWorkspaceBridge{
		Delegation: dp,
		HTTPClient: httpClient,
		ConnectURL: connectURL,
	}
}

// CallHub satisfies HubBridge. workspaceID is the delegation scope
// the router resolved from the IPC request; method is the bare hub
// method name (e.g. "GetTab", "AddTab", "ListWorkspaces").
func (b *HubWorkspaceBridge) CallHub(ctx context.Context, userID, workspaceID, method string, payload []byte) ([]byte, error) {
	if b.Delegation == nil {
		return nil, errors.New("remoteipc: delegation provider not configured")
	}
	if userID == "" {
		return nil, errors.New("remoteipc: user_id required for hub call")
	}
	desc, err := hubrpc.Lookup(method)
	if err != nil {
		return nil, fmt.Errorf("remoteipc: %w", err)
	}
	in := desc.NewRequest()
	out := desc.NewResponse()
	if err := proto.Unmarshal(payload, in); err != nil {
		return nil, fmt.Errorf("remoteipc: decode %s request: %w", method, err)
	}
	scope := scopeFromRequest(in)
	if scope == "" {
		scope = workspaceID
	}
	if scope == "" {
		return nil, fmt.Errorf("remoteipc: %s requires a workspace scope (set --workspace-id or invoke from inside an agent)", method)
	}
	bearer, err := b.Delegation.GetBearer(ctx, crossworker.DelegationScope{UserID: userID, WorkspaceID: scope})
	if err != nil {
		return nil, fmt.Errorf("delegation bearer: %w", err)
	}
	if err := desc.Invoke(ctx, b.HTTPClient, b.ConnectURL, in, out, connect.WithInterceptors(bearerInterceptor(bearer))); err != nil {
		return nil, err
	}
	return proto.Marshal(out)
}

// scopeFromRequest pulls the workspace_id field out of a request when
// the proto carries one. Methods with no workspace_id (ListWorkspaces,
// CreateWorkspace, ListWorkers) return "" and the caller falls back
// to the router-supplied scope.
func scopeFromRequest(in proto.Message) string {
	type workspaceScoped interface{ GetWorkspaceId() string }
	if ws, ok := in.(workspaceScoped); ok {
		return ws.GetWorkspaceId()
	}
	return ""
}

// bearerInterceptor sets `Authorization: Bearer <token>` on every
// outbound unary call. ConnectRPC's interceptor chain runs once per
// connect.NewClient, so it costs nothing to construct per call.
func bearerInterceptor(bearer string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+bearer)
			return next(ctx, req)
		}
	})
}
