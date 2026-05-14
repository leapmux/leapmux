// Package revocationwatcher polls the auth tables for newly-revoked
// rows and drives the matching in-memory teardown (cache eviction +
// channel close + worker notification).
//
// Why a hub-side watcher instead of having admin commands directly
// invalidate caches?
//
//   - The admin CLI runs in a separate process from the live hub.
//     Mutating the DB is straightforward; reaching into another
//     process's `auth.SessionCache` and `channelmgr.Manager` is not.
//   - The previous design relied on the 30s `sessionCacheTTL` for
//     admin revocations to take effect, which the plan flagged as
//     racy. Bearer-authenticated channels would survive even longer
//     because the hub does not re-validate the bearer per
//     inner-RPC.
//   - Polling a small `revoked_at` index every couple of seconds is
//     cheap (rows are short-lived; we only fetch ones past the
//     watcher's high-water mark) and avoids cross-process IPC, hub
//     URL configuration, and admin auth tokens just to invalidate a
//     cache entry.
//
// What the watcher polls:
//
//   - `api_tokens.revoked_at`        → EvictBearer + CloseChannelsByBearer per row.
//   - `delegation_tokens.revoked_at` → same per row.
//   - `users.tokens_revoked_at`      → EvictByUserID + EvictBearersByUserID + CloseChannelsByUser.
//
// In-process callers (UserService.ChangePassword, the
// per-token revoke handler) still drive the same close paths
// directly so the in-process latency stays at zero — the watcher
// is the cross-process safety net, not the primary path.
package revocationwatcher

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/periodic"
)

// DefaultInterval is how often the watcher sweeps the DB. 2s gives
// users near-immediate revocation feedback while keeping the load
// trivial — the queries hit the small `revoked_at` index and only
// return rows newer than the in-memory high-water mark.
const DefaultInterval = 2 * time.Second

// ChannelCloser is the subset of *service.ChannelService the
// watcher needs. The interface keeps the package free of a service
// import.
type ChannelCloser interface {
	CloseChannelsByBearer(tokenID string) int
	CloseChannelsByUser(userID string) int
}

// Watcher is constructed once at hub bootstrap and started via
// StartLoop. The exported fields are intentionally minimal — the
// revocation tables, cache, and channel closer are all the
// watcher needs to do its job.
type Watcher struct {
	Store    store.Store
	Cache    *auth.SessionCache
	Closer   ChannelCloser
	Interval time.Duration

	mu sync.Mutex
	// Per-table watermarks. Each advances only when its own sweep
	// succeeded, so a transient failure on one table never causes the
	// other table's watermark to skip past rows the failing query
	// would have returned next time. A shared watermark used to cause
	// exactly that bug (e.g. api succeeds + advance HWM; delegation
	// fails; next sweep's delegation ListRevokedSince(HWM) misses
	// rows between the old HWM and the new one).
	apiTokenWatermark        time.Time
	delegationTokenWatermark time.Time
	userWatermark            time.Time
}

// New returns a watcher with sensible defaults. Either Cache or
// Closer can be nil at construction time but both are required for
// useful behaviour; the watcher silently no-ops on missing
// dependencies so unit tests can stub one side.
//
// Watermarks start at the zero time and advance to the latest
// historical revocation on the first sweep. Seeding them at `now()`
// at construction would race ms-resolution timestamps (a revoke that
// lands in the same ms as the watcher's start has revoked_at == HWM,
// excluded by the `> HWM` filter); the zero seed is the only choice
// that loses nothing. The first sweep's cost is bounded by the
// strftime index on `revoked_at`, and the per-row teardown skips for
// pre-startup rows (see RunOnce — those tokens' channels were
// already torn down when their original revoke handler ran in this
// or a previous hub process; the watcher only acts on rows revoked
// AFTER the watcher came up).
func New(st store.Store, cache *auth.SessionCache, closer ChannelCloser) *Watcher {
	return &Watcher{
		Store:                    st,
		Cache:                    cache,
		Closer:                   closer,
		Interval:                 DefaultInterval,
		apiTokenWatermark:        time.Time{},
		delegationTokenWatermark: time.Time{},
		userWatermark:            time.Time{},
	}
}

// SeedWatermarks advances the watermarks past every revocation
// already in the DB at watcher startup, WITHOUT running per-row
// teardowns. Pre-startup revocations had their cache eviction +
// channel teardown driven by the original (in-process) revoke
// handler — running those ops again on a freshly-booted hub is
// harmless but wasteful, and on a hub with many historical
// revocations the waste scales with row count.
//
// Implementation reads the per-table MaxRevokedAt aggregates so the
// seed cost stays O(log N) (index seek) instead of O(N) — important
// for hubs that have accumulated millions of historical revocations
// across long lifetimes.
//
// Call once from hub bootstrap before StartLoop. Errors from the
// underlying store calls are returned so the caller can decide
// whether to log + continue (the watcher still works correctly
// with zero-seeded watermarks, just with the redundant first-sweep
// cost) or fail bootstrap.
func (w *Watcher) SeedWatermarks(ctx context.Context) error {
	apiMax, err := w.Store.APITokens().MaxRevokedAt(ctx)
	if err != nil {
		return fmt.Errorf("api_tokens MaxRevokedAt: %w", err)
	}
	delMax, err := w.Store.DelegationTokens().MaxRevokedAt(ctx)
	if err != nil {
		return fmt.Errorf("delegation_tokens MaxRevokedAt: %w", err)
	}
	userMax, err := w.Store.Users().MaxTokensRevokedAt(ctx)
	if err != nil {
		return fmt.Errorf("users MaxTokensRevokedAt: %w", err)
	}
	w.mu.Lock()
	w.apiTokenWatermark = apiMax
	w.delegationTokenWatermark = delMax
	w.userWatermark = userMax
	w.mu.Unlock()
	return nil
}

// StartLoop schedules the watcher's periodic sweep on ctx. The
// goroutine exits when ctx is cancelled.
func (w *Watcher) StartLoop(ctx context.Context) {
	if w.Interval <= 0 {
		w.Interval = DefaultInterval
	}
	periodic.Start(ctx, periodic.Schedule{Interval: w.Interval, SkipFirstRun: false}, func(ctx context.Context) {
		w.RunOnce(ctx)
	})
}

// RunOnce sweeps the three revocation tables once. Exported so
// tests (and operators in a debug session) can drive the watcher
// synchronously without waiting for the periodic schedule.
//
// Each table tracks its own watermark; only the table whose query
// succeeded advances. A transient failure on one table therefore
// can't strand revocations on the other (the failing table re-reads
// from its own old watermark on the next sweep instead of getting
// skipped past by a shared HWM the sibling advanced).
//
// Errors from the underlying store calls are logged at warn level
// and otherwise swallowed: the next sweep gets another shot, and
// failing the entire sweep on one transient error would leave
// other revocations stranded.
func (w *Watcher) RunOnce(ctx context.Context) {
	w.mu.Lock()
	apiSince := w.apiTokenWatermark
	delSince := w.delegationTokenWatermark
	w.mu.Unlock()

	// Run the three independent table sweeps in parallel. Each query
	// hits a different small `revoked_at` index; with a remote DB at
	// ~5ms RTT, fanning out cuts per-tick latency by ~3x. Errors are
	// swallowed (logged at warn level) so the next sweep gets another
	// shot — failing the whole RunOnce on one transient error would
	// strand unrelated revocations.
	var (
		wg               sync.WaitGroup
		apiRows, delRows []store.TokenRevocationRecord
		apiErr, delErr   error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		apiRows, apiErr = w.Store.APITokens().ListRevokedSince(ctx, apiSince)
	}()
	go func() {
		defer wg.Done()
		delRows, delErr = w.Store.DelegationTokens().ListRevokedSince(ctx, delSince)
	}()
	go func() {
		defer wg.Done()
		w.sweepUsers(ctx)
	}()
	wg.Wait()

	// Compute both new watermarks first, then advance both under a
	// single lock acquisition. The prior shape took w.mu twice (once
	// per table); consolidating drops the lock-cost on the hot path
	// and keeps the advance atomic if a SeedWatermarks call sneaks in.
	var newAPIMax, newDelMax time.Time
	if apiErr != nil {
		slog.Warn("revocation watcher: api_tokens sweep failed", "error", apiErr)
	} else {
		newAPIMax = w.applyTokenRows(apiRows, apiSince)
	}
	if delErr != nil {
		slog.Warn("revocation watcher: delegation_tokens sweep failed", "error", delErr)
	} else {
		newDelMax = w.applyTokenRows(delRows, delSince)
	}
	if newAPIMax.After(apiSince) || newDelMax.After(delSince) {
		w.mu.Lock()
		if newAPIMax.After(w.apiTokenWatermark) {
			w.apiTokenWatermark = newAPIMax
		}
		if newDelMax.After(w.delegationTokenWatermark) {
			w.delegationTokenWatermark = newDelMax
		}
		w.mu.Unlock()
	}
}

// applyTokenRows fires per-row teardown and returns the new max
// observed revoked_at (compared against `prev` so the caller can
// fold api + delegation results into one final advance).
func (w *Watcher) applyTokenRows(rows []store.TokenRevocationRecord, prev time.Time) time.Time {
	if len(rows) == 0 {
		return prev
	}
	maxRevoked := prev
	for _, r := range rows {
		// Cache eviction + channel teardown are both safe to call
		// repeatedly: EvictBearer is a sync.Map.Delete and
		// CloseChannelsByBearer no-ops when the channel set for
		// that bearer is empty.
		if w.Cache != nil {
			w.Cache.EvictBearer(r.ID)
		}
		if w.Closer != nil {
			w.Closer.CloseChannelsByBearer(r.ID)
		}
		if r.RevokedAt.After(maxRevoked) {
			maxRevoked = r.RevokedAt
		}
	}
	return maxRevoked
}

func (w *Watcher) sweepUsers(ctx context.Context) {
	w.mu.Lock()
	since := w.userWatermark
	w.mu.Unlock()
	rows, err := w.Store.Users().ListWithTokensRevokedSince(ctx, since)
	if err != nil {
		slog.Warn("revocation watcher: users sweep failed", "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	var maxRevoked time.Time
	for _, r := range rows {
		// User-wide revocation: cookies, bearers, and any channels
		// the user holds open all need to die. The cache evictions
		// are no-ops if the user has no cached entries.
		if w.Cache != nil {
			w.Cache.EvictByUserID(r.UserID)
			w.Cache.EvictBearersByUserID(r.UserID)
		}
		if w.Closer != nil {
			w.Closer.CloseChannelsByUser(r.UserID)
		}
		if r.TokensRevokedAt.After(maxRevoked) {
			maxRevoked = r.TokensRevokedAt
		}
	}
	w.mu.Lock()
	if maxRevoked.After(w.userWatermark) {
		w.userWatermark = maxRevoked
	}
	w.mu.Unlock()
}
