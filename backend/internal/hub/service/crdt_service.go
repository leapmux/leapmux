package service

import (
	"context"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
)

// CRDTService implements OrgCRDTHandler. It delegates every CRDT
// operation to the per-org Manager via the registry. Subscriber
// management lives in `ws_orgevents.go` — the only org-event
// streaming transport is the `/ws/orgevents` WebSocket. This service
// exposes unary RPCs only.
type CRDTService struct {
	store    store.Store
	registry *crdt.Registry
	logger   *slog.Logger
}

// NewCRDTService returns a service handler bound to the supplied
// registry. The registry is responsible for the per-org Manager
// goroutines.
func NewCRDTService(st store.Store, registry *crdt.Registry, logger *slog.Logger) *CRDTService {
	if logger == nil {
		logger = slog.Default()
	}
	return &CRDTService{store: st, registry: registry, logger: logger}
}

// SubmitOps validates the caller and forwards to the org manager.
func (s *CRDTService) SubmitOps(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SubmitOpsRequest],
) (*connect.Response[leapmuxv1.SubmitOpsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	mgr, err := s.registry.Get(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	results, err := mgr.Submit(ctx, crdt.SubmitInput{
		OrgID:            req.Msg.GetOrgId(),
		Epoch:            req.Msg.GetEpoch(),
		Batches:          req.Msg.GetBatches(),
		PrincipalID:      user.ID,
		OriginClient:     user.ID,
		WorkspaceScopeID: user.Credential.WorkspaceScopeID(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.SubmitOpsResponse{Results: results}), nil
}

// GetMaterialized returns a one-shot snapshot of the per-user CRDT
// projection for the given org. This is the unary equivalent of the
// `/ws/orgevents` initial OrgMaterialized event — useful for CLI
// callers that submit one batch and exit (`tab open`, `tile split`,
// etc.) so they don't pay the WS handshake cost or hold a streaming
// connection. Workspace filtering uses the same per-user ACL the WS
// path uses: an empty workspace_ids slice expands to every workspace
// the caller can read; explicit ids are intersected with the ACL.
func (s *CRDTService) GetMaterialized(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetMaterializedRequest],
) (*connect.Response[leapmuxv1.GetMaterializedResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	allowed, err := resolveAllowedWorkspacesSetForUser(ctx, s.store, req.Msg.GetOrgId(), req.Msg.GetWorkspaceIds(), user)
	if err != nil {
		// Only a delegation-scope PermissionDenied is a genuine authorization
		// failure; everything else -- an uncoded transient store failure, or a
		// coded CodeInternal should the resolver ever start wrapping its store
		// errors -- is not the client's fault and must be a retryable Internal,
		// never a permanent PermissionDenied that makes the frontend stop
		// retrying a transient DB blip. Keying on the specific authz code rather
		// than "any coded error" keeps this robust if the callee's error coding
		// changes. Mirrors ws_orgevents and ListTabs.
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	mgr, err := s.registry.Get(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	state := mgr.Materialized(crdt.SubscriberFilter{WorkspaceIDs: allowed})
	state.SubscriberClientId = presenceClientID(user)
	return connect.NewResponse(&leapmuxv1.GetMaterializedResponse{State: state}), nil
}

// UpdatePresence forwards the heartbeat to the manager. The
// authenticated, namespaced credential identity stamps the active
// client; the request body's client_id is ignored. SessionID
// distinguishes browser tabs of the same user (each tab opens its own
// cookie session via the auth flow); when SessionID is empty (e.g. an
// `lmx_…` bearer), we fall back to the bearer kind and token id, then
// to the user id as a last resort. Disjoint namespaces prevent equal
// raw ids from collapsing unrelated clients.
func (s *CRDTService) UpdatePresence(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdatePresenceRequest],
) (*connect.Response[leapmuxv1.UpdatePresenceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireDelegationWorkspace(user, req.Msg.GetWorkspaceId()); err != nil {
		return nil, err
	}
	allowed, err := auth.WorkspaceCanReadInOrg(
		ctx, s.store, req.Msg.GetOrgId(), req.Msg.GetWorkspaceId(), user.ID,
	)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("authorize presence workspace: %w", err))
	}
	if !allowed {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace access denied"))
	}
	mgr, err := s.registry.Get(ctx, req.Msg.GetOrgId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	clientID := presenceClientID(user)
	if err := mgr.HeartbeatPresence(ctx, req.Msg.GetWorkspaceId(), clientID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.UpdatePresenceResponse{}), nil
}

// presenceClientID derives the hub-side identity for an authenticated
// user. Cookie sessions distinguish each browser tab, so SessionID is
// preferred. Bearer-token clients fall back to their kind and token id
// (one active id per credential). Finally, the user id is the
// last-resort fallback so the gate stays usable even when both upstream
// signals are empty. The explicit namespaces make identities from the
// three sources collision-free. The same derivation is exposed via
// `OrgMaterialized.subscriber_client_id` so the active-client gate
// has something to compare against locally.
func presenceClientID(user *auth.UserInfo) string {
	if user == nil {
		return ""
	}
	if principal := user.Credential.PrincipalKey(); principal != "" {
		return principal
	}
	return "user:" + user.ID
}

// ResolveAllowedWorkspacesForTest exposes resolveAllowedWorkspaces to
// the package's external tests so they can exercise the per-user
// workspace filter without going through the WS handshake. Production
// callers should use the transport-specific helpers instead.
func ResolveAllowedWorkspacesForTest(ctx context.Context, st store.Store, orgID string, requested []string, userID string) ([]string, error) {
	return resolveAllowedWorkspaces(ctx, st, orgID, requested, userID)
}

// ResolveAllowedWorkspacesForUserForTest exposes the delegation-aware
// resolver for service tests that assert bearer scope cannot widen
// the ordinary per-user workspace filter.
func ResolveAllowedWorkspacesForUserForTest(ctx context.Context, st store.Store, orgID string, requested []string, user *auth.UserInfo) ([]string, error) {
	return resolveAllowedWorkspacesForUser(ctx, st, orgID, requested, user)
}

func resolveAllowedWorkspacesSetForUser(ctx context.Context, st store.Store, orgID string, requested []string, user *auth.UserInfo) (map[string]bool, error) {
	list, err := resolveAllowedWorkspacesForUser(ctx, st, orgID, requested, user)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, id := range list {
		set[id] = true
	}
	return set, nil
}

func resolveAllowedWorkspacesForUser(ctx context.Context, st store.Store, orgID string, requested []string, user *auth.UserInfo) ([]string, error) {
	if user == nil {
		return nil, fmt.Errorf("not authenticated")
	}
	requested, emptyResult, err := delegationScopedWorkspaceRequest(user, requested)
	if err != nil {
		return nil, err
	}
	if emptyResult {
		return nil, nil
	}
	return resolveAllowedWorkspaces(ctx, st, orgID, requested, user.ID)
}

// resolveAllowedWorkspaces is the per-user workspace filter used by
// the `/ws/orgevents` WebSocket handler. An empty `requested` slice
// means "every
// workspace I can read"; non-empty narrows the set, dropping any the
// caller can't read up front.
func resolveAllowedWorkspaces(ctx context.Context, st store.Store, orgID string, requested []string, userID string) ([]string, error) {
	if len(requested) == 0 {
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userID,
			OrgID:  orgID,
		})
		if err != nil {
			return nil, fmt.Errorf("list accessible workspaces: %w", err)
		}
		out := make([]string, 0, len(workspaces))
		for _, w := range workspaces {
			out = append(out, w.ID)
		}
		return out, nil
	}
	// A specific set was requested: resolve the canonical owner-or-grant read
	// rule for these workspaces against this user. Centralized in auth so the
	// per-op / batch-by-users / batch-by-workspaces read paths cannot drift; the
	// delegation empty-orgID contract (skip the org binding) lives there too.
	return auth.WorkspacesReadableByUser(ctx, st, orgID, userID, requested)
}
