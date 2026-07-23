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

// CRDTService implements OrgCRDTHandler. It delegates every CRDT
// operation to the per-org Manager via the registry. Subscriber
// management lives in `ws_orgevents.go` — the only org-event
// streaming transport is the `/ws/orgevents` WebSocket. This service
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
// registry. The registry is responsible for the per-org Manager
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

// SubmitOps validates the caller and forwards to the org manager.
func (s *CRDTService) SubmitOps(
	ctx context.Context,
	req *connect.Request[leapmuxv1.SubmitOpsRequest],
) (*connect.Response[leapmuxv1.SubmitOpsResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
	}
	// Home the request in the caller's own org before touching the registry.
	// registry.Get performs no authorization, so a caller-supplied foreign
	// org_id would otherwise materialize an arbitrary tenant's CRDT Manager
	// ahead of the per-op ACL that rejects the batches. ResolveOrgID fails
	// closed with NotFound for any foreign org and falls back to the personal
	// org when org_id is empty; the resolved value is what the manager sees.
	orgID, err := auth.ResolveOrgID(user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	// Resolve the delegation worker bound BEFORE reaching the manager: a bearer
	// whose minter cannot be established must not submit ops at all, and a
	// SetTabRegisterOp naming another user's worker is the same cross-tenant reach
	// ChannelService refuses -- SubmitOps is a delegation-allowed procedure, so it
	// is a worker-directed entrypoint whether or not it looks like one. Resolved
	// through the per-minter cache: this is the hottest delegation-bearer RPC,
	// and an uncached resolve paid one Workers().GetByID per submitted batch.
	workerScope, err := s.scopeCache.Resolve(ctx, user)
	if err != nil {
		if errors.Is(err, auth.ErrDelegationMinterUnknown) {
			return nil, connect.NewError(connect.CodePermissionDenied, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	mgr, err := s.registry.Get(ctx, orgID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("get manager: %w", err))
	}
	results, err := mgr.Submit(ctx, crdt.SubmitInput{
		OrgID:            orgID,
		Epoch:            req.Msg.GetEpoch(),
		Batches:          req.Msg.GetBatches(),
		PrincipalID:      user.ID.String(),
		OriginClient:     user.ID.String(),
		WorkspaceScopeID: user.Credential.WorkspaceScopeID(),
		WorkerScope:      workerScopePredicate(workerScope),
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
	// Refuse a foreign org id before materializing its manager (registry.Get
	// authorizes nothing). Mirrors SubmitOps and the workspace path: fail
	// closed with NotFound rather than returning an empty snapshot for a
	// tenant the caller has no part in.
	orgID, err := auth.ResolveOrgID(user, req.Msg.GetOrgId())
	if err != nil {
		return nil, err
	}
	allowed, err := resolveAllowedWorkspacesSetForUser(ctx, s.store, auth.BindOrg(orgID), req.Msg.GetWorkspaceIds(), user)
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
	mgr, err := s.registry.Get(ctx, orgID)
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
	// Presence must bind a concrete org: an absent org_id denies rather than
	// widening to "any org". Previously this rode on BindOrg("") collapsing to
	// the deny-all zero value inside the predicate; auth.BoundOrg makes the
	// requirement explicit here and unrepresentable as AnyOrg.
	bound, ok := auth.NewBoundOrg(req.Msg.GetOrgId())
	if !ok {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("workspace access denied"))
	}
	allowed, err := auth.WorkspaceCanAccessInOrg(
		ctx, s.store, bound, req.Msg.GetWorkspaceId(), user.ID,
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
	return "user:" + user.ID.String()
}

// ResolveAllowedWorkspacesForTest exposes resolveAllowedWorkspaces to
// the package's external tests so they can exercise the per-user
// workspace filter without going through the WS handshake. Production
// callers should use the transport-specific helpers instead.
func ResolveAllowedWorkspacesForTest(ctx context.Context, st store.Store, binding auth.OrgBinding, requested []string, userID userid.UserID) ([]string, error) {
	return resolveAllowedWorkspaces(ctx, st, binding, requested, userID)
}

// ResolveAllowedWorkspacesForUserForTest exposes the delegation-aware
// resolver for service tests that assert bearer scope cannot widen
// the ordinary per-user workspace filter.
func ResolveAllowedWorkspacesForUserForTest(ctx context.Context, st store.Store, binding auth.OrgBinding, requested []string, user *auth.UserInfo) ([]string, error) {
	return resolveAllowedWorkspacesForUser(ctx, st, binding, requested, user)
}

func resolveAllowedWorkspacesSetForUser(ctx context.Context, st store.Store, binding auth.OrgBinding, requested []string, user *auth.UserInfo) (map[string]bool, error) {
	list, err := resolveAllowedWorkspacesForUser(ctx, st, binding, requested, user)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, id := range list {
		set[id] = true
	}
	return set, nil
}

func resolveAllowedWorkspacesForUser(ctx context.Context, st store.Store, binding auth.OrgBinding, requested []string, user *auth.UserInfo) ([]string, error) {
	if user == nil {
		return nil, fmt.Errorf("not authenticated")
	}
	scoped, err := delegationScopedWorkspaceRequest(user, requested)
	if err != nil {
		return nil, err
	}
	if scoped.Deny {
		return nil, nil
	}
	return resolveAllowedWorkspaces(ctx, st, binding, scoped.Workspaces, user.ID)
}

// resolveAllowedWorkspaces is the per-user workspace filter used by
// the `/ws/orgevents` WebSocket handler. An empty `requested` slice
// means "every
// workspace I can read"; non-empty narrows the set, dropping any the
// caller can't read up front.
func resolveAllowedWorkspaces(ctx context.Context, st store.Store, binding auth.OrgBinding, requested []string, userID userid.UserID) ([]string, error) {
	// A deny-all binding admits nothing under EITHER arm, so both arms must
	// answer it the same way. Without this hoist they disagree in SHAPE for one
	// identical verdict: the empty-request arm refuses below (ListFilterOrgID
	// !ok -> PermissionDenied) while the bulk arm's WorkspacesReadableByUser
	// short-circuits to (nil, nil), which ListTabs renders as a 200 with no
	// tabs. "Stop retrying, you are denied" and "you have no tabs" are not
	// interchangeable to a client. Refuse, for the reason ListFilterOrgID's own
	// doc gives: a binding that admits nothing is a permanent caller/identity
	// problem, not an empty set.
	//
	// Unreachable today -- listTabsOrgBinding's only deny-all arm is user == nil,
	// which MustGetUser rejects upstream, and every auth.UserInfo mint site
	// copies a NOT NULL orgs(id) column -- so this pins the shape a FUTURE
	// deny-all source inherits rather than fixing a live bug.
	if binding.DeniesAll() {
		return nil, connect.NewError(connect.CodePermissionDenied,
			fmt.Errorf("list accessible workspaces: org binding admits no organization"))
	}
	if len(requested) == 0 {
		// "Every workspace I can read" is answered by a single-org store query,
		// so a binding that names no single org cannot be answered here.
		// Unreachable today: every caller of this branch resolves a concrete org
		// (auth.ResolveOrgID, or the ws_orgevents org_id guard), and a delegation
		// caller -- the only source of AnyOrg -- never arrives with an empty
		// `requested` because delegationScopedWorkspaceRequest substitutes its
		// pinned workspace first. Refuse rather than filter on "":
		// ListAccessibleWorkspaces matches `org_id = ?` exactly, so an empty id
		// would return no rows and report "you own nothing" for what is really a
		// caller bug.
		orgID, ok := binding.ListFilterOrgID()
		if !ok {
			// PermissionDenied, not a bare error: both callers map an uncoded
			// error to Internal, which ListTabs surfaces as a 500 and
			// /ws/orgevents closes with TryAgainLater -- and the client then
			// reconnects forever against a condition that will never change.
			// This is a permanent caller/identity problem (a blank org id), so
			// it must be reported as one.
			return nil, connect.NewError(connect.CodePermissionDenied,
				fmt.Errorf("list accessible workspaces: org binding names no single organization"))
		}
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
	// A specific set was requested: resolve the canonical owner-only read
	// rule for these workspaces against this user. Centralized in auth so the
	// per-op / batch-by-users / batch-by-workspaces read paths cannot drift; the
	// AnyOrg vs BindOrg contract lives on the OrgBinding the caller passes.
	return auth.WorkspacesReadableByUser(ctx, st, binding, userID, requested)
}
