package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

func TestMemoryOAuthStateStore_LifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	cutoff := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("create get delete and validation", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()

		// Missing state rejected.
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// Purpose range 0 and 4 rejected.
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "bad-0", ProviderID: "gh", Purpose: OAuthStatePurposeUnspecified, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "bad-4", ProviderID: "gh", Purpose: OAuthStatePurpose(4), ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// (Purpose == Reauth) == (SessionID != "") enforced.
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "reauth-no-sess", ProviderID: "gh", Purpose: OAuthStatePurposeReauth, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "login-with-sess", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, SessionID: "s1", ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		// Valid login and reauth states succeed.
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "st-login", ProviderID: "gh", PkceVerifier: "v", NonceHash: "nh",
			RedirectURI: "/app", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}))
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "st-login", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrConflict)

		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "st-reauth", ProviderID: "gh", Purpose: OAuthStatePurposeReauth, SessionID: "sess-1", ExpiresAt: cutoff.Add(time.Minute),
		}))

		got, err := s.Get(ctx, "st-login")
		require.NoError(t, err)
		assert.Equal(t, OAuthStatePurposeLogin, got.Purpose)
		assert.Equal(t, "v", got.PkceVerifier)
		got.PkceVerifier = "mutated"
		again, err := s.Get(ctx, "st-login")
		require.NoError(t, err)
		assert.Equal(t, "v", again.PkceVerifier, "Get must return a defensive copy")

		deleted, err := s.Delete(ctx, "st-login")
		require.NoError(t, err)
		assert.EqualValues(t, 1, deleted)
		deleted, err = s.Delete(ctx, "st-login")
		require.NoError(t, err)
		assert.Zero(t, deleted)
		_, err = s.Get(ctx, "st-login")
		assert.ErrorIs(t, err, store.ErrNotFound)
	})

	t.Run("delete by provider and delete expired boundaries", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "gh-1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Hour),
		}))
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "okta-expired", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "okta-at-cutoff", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff,
		}))
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "okta-live", ProviderID: "okta", Purpose: OAuthStatePurposeLogin, ExpiresAt: cutoff.Add(time.Millisecond),
		}))

		assert.EqualValues(t, 1, s.DeleteByProvider("gh"))
		_, err := s.Get(ctx, "gh-1")
		assert.ErrorIs(t, err, store.ErrNotFound)

		assert.EqualValues(t, 1, s.DeleteExpired(cutoff))
		_, err = s.Get(ctx, "okta-expired")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Get(ctx, "okta-at-cutoff")
		require.NoError(t, err, "exact-cutoff entry must survive strict < sweep")
		_, err = s.Get(ctx, "okta-live")
		require.NoError(t, err)
	})

	t.Run("capacity cap lazy eviction", func(t *testing.T) {
		now := cutoff
		s := NewMemoryOAuthStateStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "s1", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Second),
		}))
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "s2", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		// At capacity with both live: refused.
		assert.ErrorIs(t, s.Create(ctx, CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}), errOAuthStateCapacity)

		// Advance past s1's expiry: lazy eviction frees a slot.
		now = now.Add(2 * time.Second)
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
			State: "s3", ProviderID: "gh", Purpose: OAuthStatePurposeLogin, ExpiresAt: now.Add(time.Hour),
		}))
		_, err := s.Get(ctx, "s1")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Get(ctx, "s3")
		require.NoError(t, err)
	})

	t.Run("concurrent single-use delete", func(t *testing.T) {
		s := NewMemoryOAuthStateStore()
		require.NoError(t, s.Create(ctx, CreateOAuthStateParams{
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
				n, err := s.Delete(ctx, "race-state")
				if err == nil {
					winners.Add(n)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.EqualValues(t, 1, winners.Load(), "exactly one concurrent Delete must consume the state")
	})
}

func TestPendingOAuthSignupStore_LifecycleAndValidation(t *testing.T) {
	ctx := context.Background()
	cutoff := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)

	t.Run("create get normalize email delete and boundaries", func(t *testing.T) {
		s := NewPendingOAuthSignupStore()

		assert.ErrorIs(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			ProviderID: "gh", Email: "User@Example.COM", ExpiresAt: cutoff.Add(time.Minute),
		}), store.ErrInvalidArgument)

		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			Token: "tok-1", ProviderID: "gh", NonceHash: "nh", ProviderSubject: "sub-1",
			Email: "Alice@Example.COM", DisplayName: "Alice", RedirectURI: "/home",
			ExpiresAt: cutoff.Add(time.Hour),
		}))
		assert.ErrorIs(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			Token: "tok-1", ProviderID: "gh", ExpiresAt: cutoff.Add(time.Hour),
		}), store.ErrConflict)

		got, err := s.Get(ctx, "tok-1")
		require.NoError(t, err)
		assert.Equal(t, "alice@example.com", got.Email)
		got.DisplayName = "mutated"
		again, err := s.Get(ctx, "tok-1")
		require.NoError(t, err)
		assert.Equal(t, "Alice", again.DisplayName)

		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			Token: "okta-expired", ProviderID: "okta", ExpiresAt: cutoff.Add(-time.Millisecond),
		}))
		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			Token: "okta-at-cutoff", ProviderID: "okta", ExpiresAt: cutoff,
		}))
		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{
			Token: "okta-live", ProviderID: "okta", ExpiresAt: cutoff.Add(time.Millisecond),
		}))

		assert.EqualValues(t, 1, s.DeleteByProvider("gh"))
		_, err = s.Get(ctx, "tok-1")
		assert.ErrorIs(t, err, store.ErrNotFound)

		assert.EqualValues(t, 1, s.DeleteExpired(cutoff))
		_, err = s.Get(ctx, "okta-expired")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Get(ctx, "okta-at-cutoff")
		require.NoError(t, err)
		_, err = s.Get(ctx, "okta-live")
		require.NoError(t, err)
	})

	t.Run("capacity cap lazy eviction", func(t *testing.T) {
		now := cutoff
		s := NewPendingOAuthSignupStore()
		s.maxEntries = 2
		s.now = func() time.Time { return now }

		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{Token: "p1", ProviderID: "gh", ExpiresAt: now.Add(time.Second)}))
		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{Token: "p2", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		assert.ErrorIs(t, s.Create(ctx, CreatePendingOAuthSignupParams{Token: "p3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}), errPendingOAuthSignupCapacity)

		now = now.Add(2 * time.Second)
		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{Token: "p3", ProviderID: "gh", ExpiresAt: now.Add(time.Hour)}))
		_, err := s.Get(ctx, "p1")
		assert.ErrorIs(t, err, store.ErrNotFound)
		_, err = s.Get(ctx, "p3")
		require.NoError(t, err)
	})

	t.Run("concurrent single-use delete", func(t *testing.T) {
		s := NewPendingOAuthSignupStore()
		require.NoError(t, s.Create(ctx, CreatePendingOAuthSignupParams{Token: "race-tok", ProviderID: "gh", ExpiresAt: cutoff.Add(time.Hour)}))

		const workers = 16
		start := make(chan struct{})
		var winners atomic.Int64
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				<-start
				n, err := s.Delete(ctx, "race-tok")
				if err == nil {
					winners.Add(n)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.EqualValues(t, 1, winners.Load())
	})
}
