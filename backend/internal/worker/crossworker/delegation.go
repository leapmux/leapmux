package crossworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cenkalti/backoff/v5"

	"github.com/leapmux/leapmux/locallisten"
)

// DelegationStore is the in-memory cache of (user_id, workspace_id) →
// delegation token used by the per-agent IPC server. It also knows how
// to mint a fresh token via the hub's /worker/delegation-tokens/mint
// endpoint.
//
// The store re-mints when the cached access token is within
// `MintGracePeriod` of expiry; refresh-token rotation is handled
// implicitly by minting a new pair (the old delegation row is
// revoked when its agent closes).
//
// HubURL is the user-visible address (`https://hub.example` or a
// `unix:`/`npipe:` IPC URL in solo / hub-on-socket deployments).
// requestBaseURL is what mint/revoke actually POST against: identical
// to HubURL for remote hubs, but rewritten to a placeholder
// `http://localhost` for local-IPC hubs because `net/http` rejects
// any URL whose scheme isn't http(s) with "unsupported protocol
// scheme" — the socket dial is wired into HTTPClient's Transport.
type DelegationStore struct {
	HubURL          string
	WorkerAuthToken string
	HTTPClient      *http.Client
	MintGracePeriod time.Duration
	WorkerID        string

	// MintMaxAttempts caps total attempts when the hub returns
	// "tab not owned by calling worker" (403). This races with
	// AddTab propagation: the worker AddTab's the tab and may try
	// to mint before the hub-side workspace_tabs row is visible.
	// Retries use exponential backoff starting at MintRetryBackoff.
	MintMaxAttempts  int
	MintRetryBackoff time.Duration

	requestBaseURL string

	mu       sync.Mutex
	cached   map[string]cachedDelegation
	refcount map[string]int
	// tabs holds the (issued_for_tab_id, issued_for_tab_type) per
	// (user, workspace) slot. Populated by Acquire at spawn time and
	// read by mintOnce. The hub's /worker/delegation-tokens/mint
	// endpoint requires a tab the calling worker owns; without this
	// the mint returns 400 "user_id, workspace_id, issued_for_tab_id
	// are required".
	//
	// First Acquire wins: concurrent spawns for the same
	// (user, workspace) share one cached bearer, so they share one
	// provenance tab. The hub validates "worker owns this tab",
	// which is true for any tab the worker hosts in this workspace.
	tabs map[string]tabRef
}

type tabRef struct {
	ID   string
	Type int32
}

type cachedDelegation struct {
	bearer    string
	tokenID   string
	expiresAt time.Time
}

// NewDelegationStore returns a ready-to-use store.
func NewDelegationStore(hubURL, workerAuthToken, workerID string) *DelegationStore {
	httpClient, requestBaseURL := delegationHTTPClient(hubURL)
	return &DelegationStore{
		HubURL:           hubURL,
		WorkerAuthToken:  workerAuthToken,
		HTTPClient:       httpClient,
		MintGracePeriod:  5 * time.Minute,
		WorkerID:         workerID,
		MintMaxAttempts:  6,
		MintRetryBackoff: 100 * time.Millisecond,
		requestBaseURL:   requestBaseURL,
		cached:           make(map[string]cachedDelegation),
		refcount:         make(map[string]int),
		tabs:             make(map[string]tabRef),
	}
}

// delegationHTTPClient picks the transport mint/revoke POSTs should
// use. Local-IPC hub URLs (unix:/npipe:) get a socket-aware HTTP/1.1
// transport plus a `http://localhost` placeholder URL; everything
// else flows through the default transport against the real hub URL.
func delegationHTTPClient(hubURL string) (*http.Client, string) {
	const timeout = 10 * time.Second
	return locallisten.SelectClient(
		hubURL,
		func() (*http.Client, string, error) { return locallisten.LocalHTTPClient(hubURL, timeout) },
		func() (*http.Client, string) { return &http.Client{Timeout: timeout}, hubURL },
	)
}

// GetBearer satisfies DelegationProvider. Cache key includes
// workspace_id since each delegation row is scoped to one workspace.
func (s *DelegationStore) GetBearer(ctx context.Context, scope DelegationScope) (string, error) {
	if scope.UserID == "" || scope.WorkspaceID == "" {
		return "", errors.New("crossworker: user_id and workspace_id required")
	}
	key := scope.UserID + "|" + scope.WorkspaceID
	s.mu.Lock()
	if c, ok := s.cached[key]; ok && time.Until(c.expiresAt) > s.MintGracePeriod {
		bearer := c.bearer
		s.mu.Unlock()
		return bearer, nil
	}
	s.mu.Unlock()

	minted, err := s.mint(ctx, scope)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.cached[key] = cachedDelegation{bearer: minted.Access, tokenID: minted.TokenID, expiresAt: minted.ExpiresAt}
	s.mu.Unlock()
	return minted.Access, nil
}

// tabPropagationError is returned by mintOnce when the hub responds 403
// with the "tab not owned by calling worker" message. It signals that
// the AddTab → mint race may resolve on a brief retry.
type tabPropagationError struct {
	body string
}

func (e *tabPropagationError) Error() string {
	return "crossworker: tab not yet visible to hub: " + e.body
}

// mintedToken is the success carrier for one delegation-token mint
// (single attempt or the eventual success out of the backoff loop).
// Consolidating the three components into one struct keeps the
// backoff carrier and the mint return shape aligned and means
// callers stop juggling a four-value tuple at every hop.
type mintedToken struct {
	Access    string
	TokenID   string
	ExpiresAt time.Time
}

func (s *DelegationStore) mint(ctx context.Context, scope DelegationScope) (mintedToken, error) {
	maxAttempts := s.MintMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	initial := s.MintRetryBackoff
	if initial <= 0 {
		initial = 100 * time.Millisecond
	}
	// 100ms, ~200ms, ~400ms, ~800ms, ~1.6s, capped at initial<<5
	// (~3.2s with defaults). Jitter (RandomizationFactor=0.2) avoids
	// dog-pile when many agents reconnect after a hub flap; the
	// hand-rolled prior version had none.
	b := backoff.NewExponentialBackOff()
	b.InitialInterval = initial
	b.Multiplier = 2.0
	b.MaxInterval = initial << 5
	b.RandomizationFactor = 0.2
	b.Reset()

	// MaxTries is our sole budget — disable the 15-minute default
	// elapsed-time cap so MaxAttempts is the only retry governor and
	// callers' --retry-attempts configurations behave predictably.
	return backoff.Retry(ctx, func() (mintedToken, error) {
		minted, mErr := s.mintOnce(ctx, scope)
		if mErr == nil {
			return minted, nil
		}
		// Only the AddTab → mint propagation race is worth retrying;
		// every other error (auth, missing workspace) is permanent.
		var propErr *tabPropagationError
		if !errors.As(mErr, &propErr) {
			return mintedToken{}, backoff.Permanent(mErr)
		}
		return mintedToken{}, mErr
	}, backoff.WithBackOff(b), backoff.WithMaxTries(uint(maxAttempts)), backoff.WithMaxElapsedTime(0))
}

func (s *DelegationStore) mintOnce(ctx context.Context, scope DelegationScope) (mintedToken, error) {
	s.mu.Lock()
	tab, hasTab := s.tabs[scope.UserID+"|"+scope.WorkspaceID]
	s.mu.Unlock()
	if !hasTab || tab.ID == "" {
		return mintedToken{}, fmt.Errorf("delegation mint: no tab registered for (user=%s, workspace=%s); Acquire must run with tab_id at spawn time", scope.UserID, scope.WorkspaceID)
	}
	body, _ := json.Marshal(map[string]any{
		"user_id":             scope.UserID,
		"workspace_id":        scope.WorkspaceID,
		"issued_for_tab_id":   tab.ID,
		"issued_for_tab_type": tab.Type,
		"agent_id":            scope.AgentID,
		"terminal_id":         scope.TerminalID,
	})
	url := locallisten.JoinPath(s.requestBaseURL, "/worker/delegation-tokens/mint")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return mintedToken{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.WorkerAuthToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return mintedToken{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		bodyStr := strings.TrimSpace(buf.String())
		// Detect the propagation race so the caller can retry.
		if resp.StatusCode == http.StatusForbidden && strings.Contains(bodyStr, "tab not owned by calling worker") {
			return mintedToken{}, &tabPropagationError{body: bodyStr}
		}
		return mintedToken{}, fmt.Errorf("mint failed (%d): %s", resp.StatusCode, bodyStr)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		TokenID     string `json:"token_id"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return mintedToken{}, err
	}
	if out.AccessToken == "" {
		return mintedToken{}, errors.New("mint returned empty access_token")
	}
	return mintedToken{
		Access:    out.AccessToken,
		TokenID:   out.TokenID,
		ExpiresAt: time.Now().Add(time.Duration(out.ExpiresIn) * time.Second),
	}, nil
}

// Acquire records that one more spawn (agent / opted-in terminal)
// references the (user, workspace) bearer slot, along with the
// spawn's tab identity. The tab identity is what the hub validates
// at /worker/delegation-tokens/mint: the worker must own
// (workspace_id, tab_id). Pairs with Release at teardown so the
// last referencing spawn triggers a hub-side revoke instead of
// leaving the row to expire on its own.
//
// Acquire does NOT mint a token — minting stays lazy via GetBearer
// so agents that never make hub-bound calls don't create unused
// delegation rows.
//
// First Acquire wins on tab identity: when several spawns share one
// (user, workspace) cache entry, the first spawn's tab supplies the
// `issued_for_tab_id` of the eventual mint. The hub validates "this
// worker owns that tab", which is true for any tab in this
// workspace this worker hosts.
func (s *DelegationStore) Acquire(userID, workspaceID, tabID string, tabType int32) {
	if userID == "" || workspaceID == "" {
		return
	}
	key := userID + "|" + workspaceID
	s.mu.Lock()
	s.refcount[key]++
	if tabID != "" {
		if _, has := s.tabs[key]; !has {
			s.tabs[key] = tabRef{ID: tabID, Type: tabType}
		}
	}
	s.mu.Unlock()
}

// Release decrements the refcount for (userID, workspaceID). When it
// reaches zero AND a bearer was minted at some point, the cached row
// is dropped and the hub is notified to revoke it. Returns the
// hub-side revoke error so the caller can log it (revocation failures
// are non-fatal — the row will expire — but worth surfacing).
//
// The cache delete and the refcount drop happen under one lock so a
// concurrent Acquire+GetBearer for the same (user, workspace) cannot
// observe a half-released state and reuse a soon-to-be-revoked bearer.
func (s *DelegationStore) Release(ctx context.Context, userID, workspaceID string) error {
	if userID == "" || workspaceID == "" {
		return nil
	}
	key := userID + "|" + workspaceID
	s.mu.Lock()
	if s.refcount[key] > 0 {
		s.refcount[key]--
	}
	if s.refcount[key] > 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.refcount, key)
	delete(s.tabs, key)
	c, hasCached := s.cached[key]
	if hasCached {
		delete(s.cached, key)
	}
	s.mu.Unlock()
	if !hasCached || c.tokenID == "" {
		return nil
	}
	return s.revokeTokenID(ctx, c.tokenID)
}

// Invalidate drops the cached bearer for (userID, workspaceID) without
// notifying the hub. Used by 401-handling paths that observe a
// hub-side revocation has already occurred.
func (s *DelegationStore) Invalidate(userID, workspaceID string) {
	if userID == "" || workspaceID == "" {
		return
	}
	key := userID + "|" + workspaceID
	s.mu.Lock()
	delete(s.cached, key)
	s.mu.Unlock()
}

// SweepExpired drops cached delegation rows whose access token expired
// before `cutoff` AND whose refcount is zero. Returns the number of
// entries removed.
//
// Why both conditions? An expired-but-refcounted row is still
// associated with at least one live spawn; the next GetBearer call
// will mint a fresh token through the existing slot (the cached entry
// is replaced, not leaked). A row with refcount 0, on the other hand,
// only stays in the map when Release didn't run — a defense-in-depth
// case the sweep catches without touching healthy state.
func (s *DelegationStore) SweepExpired(cutoff time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	dropped := 0
	for key, entry := range s.cached {
		if !entry.expiresAt.Before(cutoff) {
			continue
		}
		if s.refcount[key] > 0 {
			continue
		}
		delete(s.cached, key)
		delete(s.tabs, key)
		dropped++
	}
	return dropped
}

// RunJanitor sweeps expired-and-orphaned cache rows on `interval`
// until ctx is cancelled. Defense-in-depth: under healthy operation,
// Release drops cache rows the moment a spawn's last reference dies,
// so this catches entries that survived an abnormal teardown
// (panicked release path, missed defer, etc.). Callers typically run
// this on a long interval (hours) relative to the token TTL.
func (s *DelegationStore) RunJanitor(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		return
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepExpired(time.Now())
		}
	}
}

// Revoke notifies the hub to revoke the delegation token cached under
// (userID, workspaceID). Idempotent. Used by manual revoke paths;
// callers driving lifecycle should use Release so the refcount is
// also cleared.
func (s *DelegationStore) Revoke(ctx context.Context, userID, workspaceID string) error {
	key := userID + "|" + workspaceID
	s.mu.Lock()
	c, ok := s.cached[key]
	delete(s.cached, key)
	s.mu.Unlock()
	if !ok || c.tokenID == "" {
		return nil
	}
	return s.revokeTokenID(ctx, c.tokenID)
}

// revokeTokenID is the hub-call portion of Revoke / Release, factored
// out so both paths (the public Revoke and the refcount-driven
// Release) post the same payload.
func (s *DelegationStore) revokeTokenID(ctx context.Context, tokenID string) error {
	if tokenID == "" {
		return nil
	}
	body, _ := json.Marshal(map[string]string{"token_id": tokenID})
	url := locallisten.JoinPath(s.requestBaseURL, "/worker/delegation-tokens/revoke")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.WorkerAuthToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("revoke failed: %s", resp.Status)
	}
	return nil
}
