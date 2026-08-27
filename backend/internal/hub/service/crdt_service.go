package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"connectrpc.com/connect"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/crdt"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// CRDTService implements UserCRDTHandler. It delegates every CRDT
// operation to the per-user Manager via the registry. Subscriber
// management lives in `ws_userevents.go` — the only user-event
// streaming transport is the `/ws/userevents` WebSocket. This service
// exposes unary RPCs only.
type CRDTService struct {
	store    store.Store
	registry *crdt.Registry
	logger   *slog.Logger
	// scopeCache memoizes the per-minter delegation worker scope SubmitOps
	// resolves on every call (see auth.DelegationScopeCache). Shared with the
	// worker-deregistration path, which evicts synchronously.
	scopeCache *auth.DelegationScopeCache
}

// NewCRDTService returns a service handler bound to the supplied
// registry. The registry is responsible for the per-user Manager
// goroutines. scopeCache may be nil (tests); a private cache over st is
// constructed then, so the field is never nil -- production passes the
// instance shared with WorkerManagementService so deregistration evicts it.
func NewCRDTService(st store.Store, registry *crdt.Registry, logger *slog.Logger, scopeCache *auth.DelegationScopeCache) *CRDTService {
	if logger == nil {
		logger = slog.Default()
	}
	if scopeCache == nil {
		scopeCache = auth.NewDelegationScopeCache(st)
	}
	return &CRDTService{store: st, registry: registry, logger: logger, scopeCache: scopeCache}
}

// SubmitOps validates the caller and forwards to the user manager.
func (s *CRDTService) SubmitOps(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SubmitOpsRequest],
) (*connect.Response[leapmuxv1.SubmitOpsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	userID := user.ID.String()
	// Resolve the delegation worker bound BEFORE handing the batches to the
	// manager: a bearer whose minter cannot be established must not submit ops
	// at all, and a SetTabRegisterOp naming another user's worker is the same
	// cross-tenant reach ChannelService refuses -- SubmitOps needs
	// workspace:write, which auth.CeilingFor(BearerKindDelegation) admits, so
	// it is a worker-directed entrypoint whether or not it looks like one.
	//
	// With the tenant now taken from the session rather than the request, the
	// hazard is no longer ordering against registry.Get -- it is DROPPING this
	// resolve, or softening the ErrDelegationMinterUnknown branch into a
	// tolerated miss. Either would hand crdt.SubmitInput.WorkerScope a nil
	// predicate, and nil means UNBOUNDED: every worker id the batch names would
	// pass. The PermissionDenied below is what keeps "minter unknown" from
	// degrading into "no restriction".
	//
	// Resolved through the per-minter cache: this is the hottest
	// delegation-bearer RPC, and an uncached resolve paid one Workers().GetByID
	// per submitted batch.
	workerScope, err := s.scopeCache.Resolve(ctx, user)
	if err != nil {
		if errors.Is(err, auth.ErrDelegationMinterUnknown) {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// This Get is where the session's tenant is spent: the manager it returns IS
	// the tenant, so the SubmitInput below names none. PrincipalID and
	// OriginClient below are NOT that axis even though they hold the same value
	// today -- they say WHO is writing, which a delegation bearer changes.
	mgr, err := s.registry.Get(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	results, err := mgr.Submit(ctx, crdt.SubmitInput{
		Epoch:        req.Msg.GetEpoch(),
		Batches:      req.Msg.GetBatches(),
		PrincipalID:  user.ID.String(),
		OriginClient: user.ID.String(),
		WorkerScope:  submitWorkerBound(user, workerScope),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.SubmitOpsResponse{Results: results}), nil
}

// GetMaterialized returns a one-shot snapshot of the per-user CRDT
// projection. This is the unary equivalent of the `/ws/userevents`
// initial UserMaterialized event -- useful for CLI callers that submit one
// batch and exit (`tab open`, `tile split`, ...) so they don't pay the WS
// handshake cost or hold a streaming connection.
//
// Workspace filtering uses the same per-user ACL the WS path uses: an empty
// workspace_ids slice expands to every workspace the caller can read; explicit
// ids are intersected with the ACL. The intersection is a SILENT DROP -- an id
// outside the ACL is simply absent from the result, so this RPC never reports
// "no such workspace" and a caller that names only unreadable ids gets a
// successful, empty snapshot. Callers that need to distinguish "empty" from
// "denied" must check the ACL themselves.
func (s *CRDTService) GetMaterialized(
	ctx context.Context,
	req *connect.Request[leapmuxv1.GetMaterializedRequest],
) (*connect.Response[leapmuxv1.GetMaterializedResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	userID := user.ID.String()
	allowed, err := resolveAllowedWorkspacesSetForUser(ctx, s.store, req.Msg.GetWorkspaceIds(), user)
	if err != nil {
		// Only a delegation-scope PermissionDenied is a genuine authorization
		// failure; an uncoded transient store failure must surface as a
		// retryable Internal, not a permanent PermissionDenied the frontend
		// stops retrying. Keying on the specific authz code (not "any coded
		// error") keeps this robust if the resolver's error coding changes.
		// Mirrors ws_userevents and ListTabs.
		if connect.CodeOf(err) == connect.CodePermissionDenied {
			return nil, err
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	mgr, err := s.registry.Get(ctx, userID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	state := mgr.Materialized(crdt.SubscriberFilter{WorkspaceIDs: allowed})
	state.SubscriberClientId = presenceClientID(user)
	return connect.NewResponse(&leapmuxv1.GetMaterializedResponse{State: state}), nil
}

// UpdatePresence forwards the heartbeat to the manager.
func (s *CRDTService) UpdatePresence(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdatePresenceRequest],
) (*connect.Response[leapmuxv1.UpdatePresenceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	allowed, err := auth.WorkspaceCanAccess(ctx, s.store, req.Msg.GetWorkspaceId(), user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("authorize presence workspace: %w", err))
	}
	if !allowed {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace access denied"))
	}
	mgr, err := s.registry.Get(ctx, user.ID.String())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	clientID := presenceClientID(user)
	if err := mgr.HeartbeatPresence(ctx, req.Msg.GetWorkspaceId(), clientID); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&leapmuxv1.UpdatePresenceResponse{}), nil
}

// presenceClientID derives the hub-side identity for an authenticated user.
// Cookie sessions distinguish each browser tab, so SessionID is preferred.
// Bearer-token clients fall back to their kind and token id (one active id per
// credential). Finally the user id is the last-resort fallback, so the gate
// stays usable even when both upstream signals are empty. The explicit
// namespaces (see auth.CredentialIdentity.PrincipalKey) make identities from
// the three sources collision-free.
//
// The same derivation ships to clients as
// `UserMaterialized.subscriber_client_id`, so the active-client gate has
// something to compare against locally.
func presenceClientID(user *auth.UserInfo) string {
	if user == nil {
		return ""
	}
	if principal := user.Credential.PrincipalKey(); principal != "" {
		return principal
	}
	return "user:" + user.ID.String()
}

// ResolveAllowedWorkspacesForTest exposes resolveAllowedWorkspaces to
// the package's external tests.
func ResolveAllowedWorkspacesForTest(ctx context.Context, st store.Store, requested []string, userID userid.UserID) ([]string, error) {
	return resolveAllowedWorkspaces(ctx, st, requested, userID)
}

// ResolveAllowedWorkspacesForUserForTest exposes the delegation-aware resolver.
func ResolveAllowedWorkspacesForUserForTest(ctx context.Context, st store.Store, requested []string, user *auth.UserInfo) ([]string, error) {
	return resolveAllowedWorkspacesForUser(ctx, st, requested, user)
}

func resolveAllowedWorkspacesSetForUser(ctx context.Context, st store.Store, requested []string, user *auth.UserInfo) (map[string]bool, error) {
	list, err := resolveAllowedWorkspacesForUser(ctx, st, requested, user)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, id := range list {
		set[id] = true
	}
	return set, nil
}

func resolveAllowedWorkspacesForUser(ctx context.Context, st store.Store, requested []string, user *auth.UserInfo) ([]string, error) {
	if user == nil {
		return nil, fmt.Errorf("not authenticated")
	}
	// No credential-kind branch. A delegation bearer resolves to exactly what a
	// session does -- every workspace its user owns -- because it authenticates
	// AS that user and carries no workspace bound.
	return resolveAllowedWorkspaces(ctx, st, requested, user.ID)
}

// resolveAllowedWorkspaces is the per-user workspace filter used by
// `/ws/userevents` and ListTabs. An empty `requested` slice means every
// workspace the user owns; non-empty narrows the set.
func resolveAllowedWorkspaces(ctx context.Context, st store.Store, requested []string, userID userid.UserID) ([]string, error) {
	if userID.IsZero() {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("list accessible workspaces: user id required"))
	}
	if len(requested) == 0 {
		workspaces, err := st.Workspaces().ListAccessible(ctx, store.ListAccessibleWorkspacesParams{
			UserID: userID,
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
	return auth.WorkspacesReadableByUser(ctx, st, userID, requested)
}
