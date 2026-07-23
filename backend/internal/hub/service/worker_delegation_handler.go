package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// WorkerDelegationHandler exposes /worker/delegation-tokens/{mint,revoke}.
// Workers authenticate with their existing auth_token (Bearer) and ask
// the hub to mint a short-lived delegation token bound to (user_id,
// workspace_id). The minted bearer is what spawned agents present when
// calling into the hub or another worker through the cross-worker path.
//
// Revocation is routed through the required credential lifecycle effects so
// cached validation, authenticated leases, and E2EE channels cannot drift.
//
// MintTabPropagationTimeout / MintTabPropagationStep govern the bounded
// backoff used when the (workspace_id, tab_id, worker_id) row hasn't
// yet appeared in workspace_tabs. Worker spawn-time minting can race
// AddTab propagation; we retry the lookup until the deadline before
// returning 403 so legitimate requests aren't rejected on cold races.
type WorkerDelegationHandler struct {
	store                     store.Store
	validator                 *auth.TokenValidator
	lifecycle                 *auth.CredentialLifecycleEffects
	MintTabPropagationTimeout time.Duration
	MintTabPropagationStep    time.Duration
}

// DefaultMintTabPropagationTimeout caps how long handleMint will wait
// for a freshly-added tab to become visible before giving up. Two
// seconds is enough cushion for AddTab commit + replication on every
// supported store backend, and short enough that a genuinely missing
// tab still surfaces a 403 quickly.
const DefaultMintTabPropagationTimeout = 2 * time.Second

// DefaultMintTabPropagationStep is the polling interval between tab
// lookups. Tight enough that the typical race resolves within a few
// hops, wide enough that hot-loop polling doesn't pummel the store.
const DefaultMintTabPropagationStep = 50 * time.Millisecond

func NewWorkerDelegationHandler(st store.Store, v *auth.TokenValidator, lifecycle *auth.CredentialLifecycleEffects) *WorkerDelegationHandler {
	if lifecycle == nil {
		panic("worker delegation handler requires credential lifecycle effects")
	}
	return &WorkerDelegationHandler{
		store:                     st,
		validator:                 v,
		lifecycle:                 lifecycle,
		MintTabPropagationTimeout: DefaultMintTabPropagationTimeout,
		MintTabPropagationStep:    DefaultMintTabPropagationStep,
	}
}

func (h *WorkerDelegationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/worker/delegation-tokens/mint", h.handleMint)
	mux.HandleFunc("/worker/delegation-tokens/revoke", h.handleRevoke)
}

type mintRequest struct {
	UserID           string `json:"user_id"`
	WorkspaceID      string `json:"workspace_id"`
	IssuedForTabID   string `json:"issued_for_tab_id"`
	IssuedForTabType int32  `json:"issued_for_tab_type"`
	AgentID          string `json:"agent_id"`
	TerminalID       string `json:"terminal_id"`
	TTLSeconds       int64  `json:"ttl_seconds"`
}

type mintResponse struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	TokenID          string `json:"token_id"`
}

func (h *WorkerDelegationHandler) handleMint(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	worker, err := h.authenticateWorker(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req mintRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.UserID == "" || req.WorkspaceID == "" || req.IssuedForTabID == "" {
		http.Error(w, "user_id, workspace_id, issued_for_tab_id are required", http.StatusBadRequest)
		return
	}
	// Verify the (workspace_id, tab_id, worker_id) triple exists in
	// workspace_tabs and the worker is the owner of the tab. This is
	// the authorization check: any worker can call mint, but only for
	// tabs it actually hosts.
	//
	// Lazy-mint races AddTab: an agent may invoke `leapmux remote ...`
	// the moment its process starts, which can be before the worker's
	// AddTab call has committed at the hub. Poll with bounded backoff
	// instead of failing immediately so the common case ("agent acts
	// before AddTab quiesces") doesn't surface as a hard 403.
	tabFound, lookupErr := h.waitForTabOwnership(r.Context(), req.WorkspaceID, req.IssuedForTabID, worker.ID)
	if lookupErr != nil {
		http.Error(w, "list tabs failed", http.StatusInternalServerError)
		return
	}
	if !tabFound {
		http.Error(w, "tab not owned by calling worker", http.StatusForbidden)
		return
	}
	// Verify user has access to the workspace via the canonical
	// owner-only predicate. A missing workspace also returns false
	// here (auth.WorkspaceCanRead maps NotFound to false); we
	// flatten both "no such workspace" and "not the owner" into a 403
	// so the worker doesn't get a probing oracle for workspace IDs.
	// A non-nil err is a real store failure (SQLITE_BUSY, network
	// blip) -- surface it as a retryable 500, not a permanent 403,
	// so a brief DB hiccup doesn't fail a legitimate lazy mint (the
	// tab-ownership lookup above already maps store errors to 500).
	// Unreachable behind the empty-field validation above, which already 400s a
	// blank user_id. Kept as the mint boundary's own fail-close so a future
	// relaxation of that validation cannot hand WorkspaceCanRead a zero id: 403
	// (not 400) because by this point the request is well-formed and the answer
	// is "no access", the same shape the hasAccess check below returns.
	uid, ok := userid.New(req.UserID)
	if !ok {
		http.Error(w, "user lacks workspace access", http.StatusForbidden)
		return
	}
	hasAccess, err := auth.WorkspaceCanRead(r.Context(), h.store, auth.AnyOrg(), req.WorkspaceID, uid)
	if err != nil {
		http.Error(w, "workspace access check failed", http.StatusInternalServerError)
		return
	}
	if !hasAccess {
		http.Error(w, "user lacks workspace access", http.StatusForbidden)
		return
	}
	// The token authenticates as req.UserID, so require it to be exactly the
	// calling worker's own registrant. Owner-only access plus tab-placement
	// gating make that equality hold today, but stating it here keeps the
	// mint's safety a local check rather than a transitive consequence of a
	// predicate in another package -- so a future path that ever lets a worker
	// host a tab in a workspace it does not own cannot mint a bearer
	// impersonating that workspace's owner.
	// Compare through the minted id, not the raw request string: Matches refuses
	// an empty id on either side, so a blank-registrant row cannot be matched by
	// a blank req.UserID. That pairing is what the "future relaxation" hedge
	// above is guarding against -- a raw != would fail OPEN on exactly the input
	// this check exists to refuse, minting a bearer that impersonates the
	// workspace's owner.
	if !uid.Matches(worker.RegisteredBy) {
		http.Error(w, "user is not the worker's registrant", http.StatusForbidden)
		return
	}

	ttl := time.Duration(req.TTLSeconds) * time.Second
	if ttl <= 0 || ttl > auth.DelegationTokenTTL {
		ttl = auth.DelegationTokenTTL
	}
	tokenID := id.Generate()
	pair := h.validator.MintBearerPair(auth.BearerKindDelegation, tokenID, time.Now(), ttl, auth.RefreshTokenTTL)
	if err := h.store.DelegationTokens().Create(r.Context(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           uid,
		WorkerID:         worker.ID,
		WorkspaceID:      req.WorkspaceID,
		AgentID:          req.AgentID,
		TerminalID:       req.TerminalID,
		IssuedForTabID:   req.IssuedForTabID,
		IssuedForTabType: req.IssuedForTabType,
		SecretHash:       pair.AccessHash,
		RefreshHash:      pair.RefreshHash,
		ExpiresAt:        pair.AccessExpiresAt,
		RefreshExpiresAt: &pair.RefreshExpiresAt,
	}); err != nil {
		http.Error(w, "create token failed", http.StatusInternalServerError)
		return
	}
	resp := mintResponse{
		AccessToken:      pair.AccessBearer,
		RefreshToken:     pair.RefreshBearer,
		ExpiresIn:        int64(ttl / time.Second),
		RefreshExpiresIn: int64(auth.RefreshTokenTTL / time.Second),
		TokenID:          tokenID,
	}
	writeJSON(w, http.StatusOK, resp)
}

type revokeRequest struct {
	TokenID string `json:"token_id"`
}

func (h *WorkerDelegationHandler) handleRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	worker, err := h.authenticateWorker(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	var req revokeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.TokenID == "" {
		http.Error(w, "token_id is required", http.StatusBadRequest)
		return
	}
	row, err := h.store.DelegationTokens().GetByID(r.Context(), req.TokenID)
	if err != nil {
		http.Error(w, "token not found", http.StatusNotFound)
		return
	}
	if row.WorkerID != worker.ID {
		http.Error(w, "token not minted by calling worker", http.StatusForbidden)
		return
	}
	if _, err := h.store.DelegationTokens().Revoke(r.Context(), req.TokenID); err != nil {
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	h.lifecycle.BearerRevoked(auth.BearerKindDelegation, req.TokenID)
	w.WriteHeader(http.StatusNoContent)
}

// waitForTabOwnership polls workspace_tabs for the (workspace, tab,
// worker) triple until either it appears, the request context is
// cancelled, or the configured propagation timeout elapses. Returning
// (false, nil) means the row is genuinely absent; (false, err) means
// the store query itself failed. Step interval and total timeout are
// tunable on the receiver to keep tests fast.
//
// Polling uses exponential backoff (starting at `step`, capped at
// `step*8`) so the typical race resolves in 1-3 queries while a slow
// AddTab still gets a chance to land before the deadline.
func (h *WorkerDelegationHandler) waitForTabOwnership(ctx context.Context, workspaceID, tabID, workerID string) (bool, error) {
	timeout := h.MintTabPropagationTimeout
	if timeout <= 0 {
		timeout = DefaultMintTabPropagationTimeout
	}
	step := h.MintTabPropagationStep
	if step <= 0 {
		step = DefaultMintTabPropagationStep
	}
	params := store.GetOwnedTabParams{WorkspaceID: workspaceID, TabID: tabID}
	deadline := time.Now().Add(timeout)
	maxSleep := step * 8
	sleep := step
	for {
		row, err := h.store.WorkspaceTabIndex().GetOwned(ctx, params)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return false, err
		}
		if row != nil && row.WorkerID == workerID {
			return true, nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false, nil
		}
		wait := sleep
		if wait > remaining {
			wait = remaining
		}
		select {
		case <-ctx.Done():
			return false, nil
		case <-time.After(wait):
		}
		sleep *= 2
		if sleep > maxSleep {
			sleep = maxSleep
		}
	}
}

// authenticateWorker resolves the Bearer-encoded worker auth_token to a
// Worker row.
func (h *WorkerDelegationHandler) authenticateWorker(r *http.Request) (*store.Worker, error) {
	return auth.AuthenticateWorkerBearer(r.Context(), h.store, r.Header.Get("Authorization"))
}
