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

	"github.com/cenkalti/backoff/v6"

	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/locallisten"
)

// DelegationStore is the in-memory cache of user_id -> delegation token
// used by the per-agent IPC server. It also knows how to mint a fresh
// token via the hub's /worker/delegation-tokens/mint endpoint.
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
	// to mint before the hub-side workspace_tab_owned row is visible.
	// Retries use exponential backoff starting at MintRetryBackoff.
	MintMaxAttempts  int
	MintRetryBackoff time.Duration

	requestBaseURL string

	mu       sync.Mutex
	cached   map[string]cachedDelegation
	refcount map[string]int
	// inflight collapses concurrent first-time mints for one slot. Without it,
	// two callers that miss the cache together each POST the hub's mint endpoint
	// and each get a DISTINCT token_id; the loser's id is overwritten in `cached`
	// and never reaches revokeTokenID, so its credential stays live until its TTL
	// with nothing able to revoke it.
	//
	// The race became reachable when the key collapsed from (user, workspace) to
	// user alone: two spawns in two different workspaces on one worker used to
	// occupy separate slots and mint separately by design, and now share one.
	// crossworker.Client already singleflights channel opens (see channelOpen)
	// for the same reason one layer up.
	inflight map[string]*mintFlight

	// LiveTab supplies the mint's provenance tab. Required for minting; see
	// LiveTabProvider.
	LiveTab LiveTabProvider
}

// LiveTabProvider answers "which tab does this worker currently host?" for the
// mint's issued_for_tab_id. Injected rather than read from a local map, and
// injected as a closure so this package keeps no dependency on the worker DB.
//
// It replaces a shadow set that Acquire/Release maintained alongside the real
// inventory. That set was a second source of truth for a fact the worker's own
// agents/terminals tables already hold, and it could drift from them in ways that
// broke every subsequent mint: a Release that never ran (a panic in a cleanup, a
// close path added later that bypasses the registry) left a dead tab id behind,
// and the hub answered 403 "tab not owned by calling worker" for it forever.
// Nothing pruned it, because a 403 is exactly what a not-yet-propagated tab looks
// like. Reading the live tables instead makes a missed Release cost nothing.
type LiveTabProvider func() (tabID string, tabType int32, ok bool)

// tabRef is one tab's mint provenance: which tab, and of what type.
type tabRef struct {
	ID   string
	Type int32
}

// mintFlight is one in-progress mint that later arrivals wait on rather than
// duplicating. done is closed once tok/err are final.
type mintFlight struct {
	done chan struct{}
	tok  mintedToken
	err  error
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
		inflight:         make(map[string]*mintFlight),
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

// delegationKey is the cache/refcount/tab key for one user's delegation slot.
// Every map in this store is keyed by it, so the three maps cannot drift
// apart the way three hand-built projections could: change the shape here and
// every reader follows.
//
// It is the minted id's underlying string -- the maps are string-keyed
// because userid.UserID is deliberately non-comparable.
func delegationKey(userID userid.UserID) string {
	return userID.String()
}

// GetBearer satisfies DelegationProvider.
func (s *DelegationStore) GetBearer(ctx context.Context, scope DelegationScope) (string, error) {
	if scope.UserID.IsZero() {
		return "", errors.New("crossworker: user_id required")
	}
	key := delegationKey(scope.UserID)
	s.mu.Lock()
	if c, ok := s.cached[key]; ok && time.Until(c.expiresAt) > s.MintGracePeriod {
		bearer := c.bearer
		s.mu.Unlock()
		return bearer, nil
	}
	// Join an in-flight mint for this slot instead of starting a second one.
	if fl, joined := s.inflight[key]; joined {
		s.mu.Unlock()
		select {
		case <-fl.done:
			if fl.err != nil {
				return "", fl.err
			}
			return fl.tok.Access, nil
		case <-ctx.Done():
			// Leaving is safe: the leader owns the flight's cleanup, so abandoning
			// it here cannot strand the slot for the next caller.
			return "", ctx.Err()
		}
	}
	fl := &mintFlight{done: make(chan struct{})}
	s.inflight[key] = fl
	s.mu.Unlock()

	minted, err := s.mint(ctx, scope)

	s.mu.Lock()
	fl.tok, fl.err = minted, err
	delete(s.inflight, key)
	if err == nil {
		s.cached[key] = cachedDelegation{bearer: minted.Access, tokenID: minted.TokenID, expiresAt: minted.ExpiresAt}
	}
	s.mu.Unlock()
	// Closed after the map writes, so a joiner that wakes immediately observes a
	// cache already holding this token rather than racing back into a fresh miss.
	close(fl.done)

	if err != nil {
		return "", err
	}
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
		// every other error (auth, unknown tab) is permanent.
		var propErr *tabPropagationError
		if !errors.As(mErr, &propErr) {
			return mintedToken{}, backoff.Permanent(mErr)
		}
		return mintedToken{}, mErr
	}, backoff.WithBackOff(b), backoff.WithMaxTries(uint(maxAttempts)), backoff.WithMaxElapsedTime(0))
}

func (s *DelegationStore) mintOnce(ctx context.Context, scope DelegationScope) (mintedToken, error) {
	if s.LiveTab == nil {
		return mintedToken{}, errors.New("delegation mint: no LiveTabProvider wired")
	}
	tabID, tabType, ok := s.LiveTab()
	if !ok || tabID == "" {
		return mintedToken{}, fmt.Errorf("delegation mint: this worker hosts no open tab for user=%s", scope.UserID.String())
	}
	tab := tabRef{ID: tabID, Type: tabType}
	body, _ := json.Marshal(map[string]any{
		"user_id":             scope.UserID.String(),
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
// references the user's bearer slot, along with the spawn's tab identity.
// The tab identity is what the hub validates at
// /worker/delegation-tokens/mint: the worker must own the tab. Pairs with
// Release at teardown so the last referencing spawn triggers a hub-side
// revoke instead of leaving the row to expire on its own.
//
// Acquire does NOT mint a token — minting stays lazy via GetBearer
// so agents that never make hub-bound calls don't create unused
// delegation rows.
//
// Spawns sharing one user's cache entry each contribute their own tab, and a
// mint picks any live one as `issued_for_tab_id`. The hub validates "this
// worker owns that tab", which holds for any tab this worker currently hosts --
// so the set must track live spawns, not merely the first one ever seen.
func (s *DelegationStore) Acquire(userID userid.UserID) {
	if userID.IsZero() {
		return
	}
	key := delegationKey(userID)
	s.mu.Lock()
	s.refcount[key]++
	s.mu.Unlock()
}

// Release decrements the refcount for userID and retires tabID from the
// provenance set. When the refcount reaches zero AND a bearer was minted at
// some point, the cached row is dropped and the hub is notified to revoke it.
// Returns the hub-side revoke error so the caller can log it (revocation
// failures are non-fatal — the row will expire — but worth surfacing).
//
// tabID is the RELEASING spawn's own tab, and passing it is what keeps the
// provenance set live. Dropping only at refcount zero left a closed spawn's tab
// as the mint's `issued_for_tab_id` for as long as any sibling survived, which
// the hub then refused as "tab not owned by calling worker".
//
// The cache delete and the refcount drop happen under one lock so a
// concurrent Acquire+GetBearer for the same user cannot observe a
// half-released state and reuse a soon-to-be-revoked bearer.
func (s *DelegationStore) Release(ctx context.Context, userID userid.UserID) error {
	if userID.IsZero() {
		return nil
	}
	key := delegationKey(userID)
	s.mu.Lock()
	if s.refcount[key] > 0 {
		s.refcount[key]--
	}
	if s.refcount[key] > 0 {
		s.mu.Unlock()
		return nil
	}
	delete(s.refcount, key)
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

// revokeTokenID is the hub-call portion of Release, factored out so the
// refcount-driven retirement and any future revoke path post the same payload.
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
