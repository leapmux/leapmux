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
		OrgID:        req.Msg.GetOrgId(),
		Epoch:        req.Msg.GetEpoch(),
		Batches:      req.Msg.GetBatches(),
		PrincipalID:  user.ID,
		OriginClient: user.ID,
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
	allowed, err := resolveAllowedWorkspacesSet(ctx, s.store, req.Msg.GetOrgId(), req.Msg.GetWorkspaceIds(), user.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodePermissionDenied, err)
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
// authenticated session id stamps the active-client identity; the
// request body's client_id is ignored. SessionID distinguishes
// browser tabs of the same user (each tab opens its own cookie
// session via the auth flow); when SessionID is empty (e.g. an
// `lmx_…` bearer), we fall back to the bearer token id, then to the
// user id as a last resort. The fall-back chain keeps the gate
// useful even when the caller isn't a cookie-session client.
func (s *CRDTService) UpdatePresence(
	ctx context.Context,
	req *connect.Request[leapmuxv1.UpdatePresenceRequest],
) (*connect.Response[leapmuxv1.UpdatePresenceResponse], error) {
	user, err := auth.MustGetUser(ctx)
	if err != nil {
		return nil, err
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
// preferred. Bearer-token clients fall back to the token id (one
// active id per token). Finally, the user id is the last-resort
// fallback so the gate stays usable even when both upstream signals
// are empty. Same derivation is exposed to the client via
// `OrgMaterialized.subscriber_client_id` so the active-client gate
// has something to compare against locally.
func presenceClientID(user *auth.UserInfo) string {
	if user == nil {
		return ""
	}
	if user.SessionID != "" {
		return user.SessionID
	}
	if user.BearerTokenID != "" {
		return user.BearerTokenID
	}
	return user.ID
}

// ResolveAllowedWorkspacesForTest exposes resolveAllowedWorkspaces to
// the package's external tests so they can exercise the per-user
// workspace filter without going through the WS handshake. Production
// callers should use the transport-specific helpers instead.
func ResolveAllowedWorkspacesForTest(ctx context.Context, st store.Store, orgID string, requested []string, userID string) ([]string, error) {
	return resolveAllowedWorkspaces(ctx, st, orgID, requested, userID)
}

// resolveAllowedWorkspacesSet runs resolveAllowedWorkspaces and
// returns its result as a map[string]bool, which is the shape both
// CRDT subscriber filters and the orgevents WS handshake consume.
// Returns nil set on empty list (allow-all semantics live in the
// caller via the CRDT filter contract).
func resolveAllowedWorkspacesSet(ctx context.Context, st store.Store, orgID string, requested []string, userID string) (map[string]bool, error) {
	list, err := resolveAllowedWorkspaces(ctx, st, orgID, requested, userID)
	if err != nil {
		return nil, err
	}
	set := make(map[string]bool, len(list))
	for _, id := range list {
		set[id] = true
	}
	return set, nil
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
	// Dedup + drop empties so the bulk lookups stay tight and the
	// access query doesn't ask the store about repeats.
	dedup := make([]string, 0, len(requested))
	seen := make(map[string]struct{}, len(requested))
	for _, wsID := range requested {
		if wsID == "" {
			continue
		}
		if _, dup := seen[wsID]; dup {
			continue
		}
		seen[wsID] = struct{}{}
		dedup = append(dedup, wsID)
	}
	if len(dedup) == 0 {
		return nil, nil
	}
	rows, err := st.Workspaces().ListByIDs(ctx, dedup)
	if err != nil {
		return nil, err
	}
	// Workspaces the caller owns are auto-granted; the rest need an
	// explicit ACL row. Resolve both classes with at most two queries
	// (the access list is skipped when every requested workspace is
	// owned by the caller).
	wsByID := make(map[string]*store.Workspace, len(rows))
	for i := range rows {
		wsByID[rows[i].ID] = &rows[i]
	}
	out := make([]string, 0, len(dedup))
	var needCheck []string
	for _, wsID := range dedup {
		ws, ok := wsByID[wsID]
		if !ok {
			continue
		}
		if orgID != "" && ws.OrgID != orgID {
			continue
		}
		if ws.OwnerUserID == userID {
			out = append(out, wsID)
			continue
		}
		needCheck = append(needCheck, wsID)
	}
	if len(needCheck) > 0 {
		granted, err := st.WorkspaceAccess().ListForUserIn(ctx, userID, needCheck)
		if err != nil {
			return nil, err
		}
		grantedSet := make(map[string]struct{}, len(granted))
		for _, id := range granted {
			grantedSet[id] = struct{}{}
		}
		for _, wsID := range needCheck {
			if _, ok := grantedSet[wsID]; ok {
				out = append(out, wsID)
			}
		}
	}
	return out, nil
}
