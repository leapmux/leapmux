package service

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestMemoryOAuthStateStore_LifecycleAndValidation(t *testing.T) {
	cutoff := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("create get delete and validation", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()

		// Missing state rejected.
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// Purpose range 0 and 4 rejected.
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "bad-0", ProviderID: "gh", Purpose: OAuthStatePurposeUnspecified, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "bad-4", ProviderID: "gh", Purpose: OAuthStatePurpose(4), ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// (Purpose == Reauth) == (SessionID != "") enforced.
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "reauth-no-sess", ProviderID: "gh", Purpose: OAuthStatePurposeReauth, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "login-with-sess", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, SessionID: "s1", ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// Valid login and reauth states succeed.
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "st-login", ProviderID: "gh", PkceVerifier: "v", NonceHash: "nh",
			RedirectURI: "/app", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}))
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "st-login", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrConflict)

		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "st-reauth", ProviderID: "gh", Purpose: OAuthStatePurposeReauth, SessionID: "sess-1", ExpiresAt: cutoff.Add(time.Minute),
		}))

		got, ok := s.Get("st-login")
		require.True(t, ok)
		assert.Equal(t, OAuthStatePurposeLogin, got.Purpose)
		assert.Equal(t, "v", got.PkceVerifier)
		got.PkceVerifier = "mutated"
		again, ok := s.Get("st-login")
		require.True(t, ok)
		assert.Equal(t, "v", again.PkceVerifier, "Get must return a defensive copy")

		assert.True(t, s.Delete("st-login"))
		assert.False(t, s.Delete("st-login"))
		_, ok = s.Get("st-login")
		assert.False(t, ok)
	})

	t.Run("delete by provider and delete expired boundaries", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "gh-1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "okta-expired", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "okta-at-cutoff", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff,
		}))
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "okta-live", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Millisecond),
		}))

		assert.EqualValues(t, 1, s.DeleteByProvider("gh"))
		_, ok := s.Get("gh-1")
		assert.False(t, ok)

		assert.EqualValues(t, 1, s.DeleteExpired(cutoff))
		_, ok = s.Get("okta-expired")
		assert.False(t, ok)
		_, ok = s.Get("okta-at-cutoff")
		assert.True(t, ok, "exact-cutoff entry must survive strict < sweep")
		_, ok = s.Get("okta-live")
		assert.True(t, ok)
	})

	t.Run("capacity cap lazy eviction", func(t *testing.T) {
		now := cutoff
		s := NewMemoryOAuthStateStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Second),
		}))
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s2", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		// At capacity with both live: refused.
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}), errOAuthStateCapacity)

		// Advance past s1's expiry: lazy eviction frees a slot.
		now = now.Add(2 * time.Second)
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		_, ok := s.Get("s1")
		assert.False(t, ok)
		_, ok = s.Get("s3")
		assert.True(t, ok)
	})

	t.Run("out-of-order expiry at capacity", func(t *testing.T) {
		now := cutoff
		s := NewMemoryOAuthStateStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s2", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Second),
		}))

		// At exact s2 expiry, strict < keeps s2 alive and Create hits capacity.
		now = cutoff.Add(time.Second)
		assert.ErrorIs(t, s.Create(CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}), errOAuthStateCapacity)

		// Past s2 expiry, Create evicts s2 and keeps s1.
		now = cutoff.Add(2 * time.Second)
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		_, ok := s.Get("s2")
		assert.False(t, ok)
		_, ok = s.Get("s1")
		assert.True(t, ok)
		_, ok = s.Get("s3")
		assert.True(t, ok)
	})

	t.Run("empty-store reset restores lazy eviction after deletions", func(t *testing.T) {
		for _, method := range []string{"DeleteByProvider", "Delete"} {
			t.Run(method, func(t *testing.T) {
				now := cutoff
				s := NewMemoryOAuthStateStore()
				s.maxEntries = 2
				s.now = func() time.Time { return now }

				require.NoError(t, s.Create(CreateOAuthStateParams{
					State: "long-1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(2 * time.Hour),
				}))
				require.NoError(t, s.Create(CreateOAuthStateParams{
					State: "long-2", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(3 * time.Hour),
				}))

				if method == "DeleteByProvider" {
					assert.EqualValues(t, 2, s.DeleteByProvider("gh"))
				} else {
					assert.True(t, s.Delete("long-1"))
					assert.True(t, s.Delete("long-2"))
				}
				assert.True(t, s.minExpiresAt.IsZero(), "emptying the store must reset minExpiresAt")

				require.NoError(t, s.Create(CreateOAuthStateParams{
					State: "short", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Second),
				}))
				require.NoError(t, s.Create(CreateOAuthStateParams{
					State: "medium", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(30 * time.Minute),
				}))

				now = now.Add(2 * time.Second)
				require.NoError(t, s.Create(CreateOAuthStateParams{
					State: "fresh", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
				}))
				_, ok := s.Get("short")
				assert.False(t, ok)
				_, ok = s.Get("medium")
				assert.True(t, ok)
				_, ok = s.Get("fresh")
				assert.True(t, ok)
			})
		}
	})

	t.Run("concurrent single-use delete", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()
		require.NoError(t, s.Create(CreateOAuthStateParams{
			State: "race-state", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Hour),
		}))

		const workers = 16
		start := make(chan struct{})
		var winners atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				<-start
				if s.Delete("race-state") {
					winners.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.EqualValues(t, 1, winners.Load(), "exactly one concurrent Delete must consume the state")
	})
}

func TestPendingOAuthSignupStore_LifecycleAndValidation(t *testing.T) {
	cutoff := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("create get normalize email delete and boundaries", func(t *testing.T) {
		s := NewPendingOAuthSignupStore()

		assert.ErrorIs(t, s.Create(CreatePendingOAuthSignupParams{
			ProviderID: "gh", Email: "User@Example.COM", ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{
			Token: "tok-1", ProviderID: "gh", NonceHash: "nh", ProviderSubject: "sub-1",
			Email: "Alice@Example.COM", DisplayName: "Alice", RedirectURI: "/home",
			ExpiresAt: cutoff.Add(time.Hour),
		}))
		assert.ErrorIs(t, s.Create(CreatePendingOAuthSignupParams{
			Token: "tok-1", ProviderID: "gh", ExpiresAt: cutoff.Add(time.Hour),
		}), store.ErrConflict)

		got, ok := s.Get("tok-1")
		require.True(t, ok)
		assert.Equal(t, "alice@example.com", got.Email)
		got.DisplayName = "mutated"
		again, ok := s.Get("tok-1")
		require.True(t, ok)
		assert.Equal(t, "Alice", again.DisplayName)

		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{
			Token: "okta-expired", ProviderID: "okta", ExpiresAt: cutoff.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{
			Token: "okta-at-cutoff", ProviderID: "okta", ExpiresAt: cutoff,
		}))
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{
			Token: "okta-live", ProviderID: "okta", ExpiresAt: cutoff.Add(time.Millisecond),
		}))

		assert.EqualValues(t, 1, s.DeleteByProvider("gh"))
		_, ok = s.Get("tok-1")
		assert.False(t, ok)

		assert.EqualValues(t, 1, s.DeleteExpired(cutoff))
		_, ok = s.Get("okta-expired")
		assert.False(t, ok)
		_, ok = s.Get("okta-at-cutoff")
		assert.True(t, ok)
		_, ok = s.Get("okta-live")
		assert.True(t, ok)
	})

	t.Run("capacity cap lazy eviction", func(t *testing.T) {
		now := cutoff
		s := NewPendingOAuthSignupStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "p1", ProviderID: "gh", ExpiresAt: now.Add(time.Second)}))
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "p2", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		assert.ErrorIs(t, s.Create(CreatePendingOAuthSignupParams{Token: "p3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}), errPendingOAuthSignupCapacity)

		now = now.Add(2 * time.Second)
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "p3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		_, ok := s.Get("p1")
		assert.False(t, ok)
		_, ok = s.Get("p3")
		assert.True(t, ok)
	})

	t.Run("out-of-order expiry at capacity", func(t *testing.T) {
		now := cutoff
		s := NewPendingOAuthSignupStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "s1", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "s2", ProviderID: "gh", ExpiresAt: now.Add(time.Second)}))

		now = cutoff.Add(time.Second)
		assert.ErrorIs(t, s.Create(CreatePendingOAuthSignupParams{Token: "s3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}), errPendingOAuthSignupCapacity)

		now = cutoff.Add(2 * time.Second)
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "s3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		_, ok := s.Get("s2")
		assert.False(t, ok)
		_, ok = s.Get("s1")
		assert.True(t, ok)
		_, ok = s.Get("s3")
		assert.True(t, ok)
	})

	t.Run("empty-store reset restores lazy eviction after deletions", func(t *testing.T) {
		for _, method := range []string{"DeleteByProvider", "Delete"} {
			t.Run(method, func(t *testing.T) {
				now := cutoff
				s := NewPendingOAuthSignupStore()
				s.maxEntries = 2
				s.now = func() time.Time { return now }

				require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "long-1", ProviderID: "gh", ExpiresAt: now.Add(2 * time.Hour)}))
				require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "long-2", ProviderID: "gh", ExpiresAt: now.Add(3 * time.Hour)}))

				if method == "DeleteByProvider" {
					assert.EqualValues(t, 2, s.DeleteByProvider("gh"))
				} else {
					assert.True(t, s.Delete("long-1"))
					assert.True(t, s.Delete("long-2"))
				}
				assert.True(t, s.minExpiresAt.IsZero(), "emptying the store must reset minExpiresAt")

				require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "short", ProviderID: "gh", ExpiresAt: now.Add(time.Second)}))
				require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "medium", ProviderID: "gh", ExpiresAt: now.Add(30 * time.Minute)}))

				now = now.Add(2 * time.Second)
				require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "fresh", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
				_, ok := s.Get("short")
				assert.False(t, ok)
				_, ok = s.Get("medium")
				assert.True(t, ok)
				_, ok = s.Get("fresh")
				assert.True(t, ok)
			})
		}
	})

	t.Run("concurrent single-use delete", func(t *testing.T) {
		s := NewPendingOAuthSignupStore()
		require.NoError(t, s.Create(CreatePendingOAuthSignupParams{Token: "race-tok", ProviderID: "gh", ExpiresAt: cutoff.Add(time.Hour)}))

		const workers = 16
		start := make(chan struct{})
		var winners atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				<-start
				if s.Delete("race-tok") {
					winners.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.EqualValues(t, 1, winners.Load())
	})
}
