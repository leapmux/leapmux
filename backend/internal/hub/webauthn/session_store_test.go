package webauthn

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestSessionStore_LifecycleReplacementAndConsumption(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	bob := userid.MustNew("user-bob")

	t.Run("validation and defensive cloning", func(t *testing.T) {
		s := NewSessionStore()

		assert.ErrorIs(t, s.Create(CeremonySession{Kind: KindLogin, SessionData: &gowebauthn.SessionData{Challenge: "c"}}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(CeremonySession{ID: "s1", Kind: KindUnspecified, SessionData: &gowebauthn.SessionData{Challenge: "c"}}), store.ErrInvalidArgument)
		assert.ErrorIs(t, s.Create(CeremonySession{ID: "s1", Kind: KindLogin}), store.ErrInvalidArgument)

		srcData := &gowebauthn.SessionData{
			Challenge:            "chal-1",
			UserID:               []byte("alice-handle"),
			AllowedCredentialIDs: [][]byte{[]byte("cred-1")},
		}
		require.NoError(t, s.Create(CeremonySession{
			ID:          "s1",
			Kind:        KindLogin,
			UserID:      alice,
			SessionData: srcData,
			ExpiresAt:   now.Add(time.Minute),
		}))

		// Mutate caller slices after Create.
		srcData.UserID[0] = 'X'
		srcData.AllowedCredentialIDs[0][0] = 'Y'

		got, ok := s.Get("s1")
		require.True(t, ok)
		assert.Equal(t, []byte("alice-handle"), got.SessionData.UserID)
		assert.Equal(t, []byte("cred-1"), got.SessionData.AllowedCredentialIDs[0])

		// Mutate read copy; stored state must stay intact.
		got.SessionData.UserID[0] = 'Z'
		again, ok := s.Get("s1")
		require.True(t, ok)
		assert.Equal(t, []byte("alice-handle"), again.SessionData.UserID)
	})

	t.Run("atomic per-user kind replacement and concurrent anonymous signups", func(t *testing.T) {
		s := NewSessionStore()

		// Per-user replacement for same (user, kind), keeping other kinds and other users.
		require.NoError(t, s.Create(CeremonySession{
			ID: "alice-login-1", Kind: KindLogin, UserID: alice,
			SessionData: &gowebauthn.SessionData{Challenge: "1"}, ExpiresAt: now.Add(time.Minute),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "alice-elev-1", Kind: KindElevation, UserID: alice,
			SessionData: &gowebauthn.SessionData{Challenge: "e1"}, ExpiresAt: now.Add(time.Minute),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "bob-login-1", Kind: KindLogin, UserID: bob,
			SessionData: &gowebauthn.SessionData{Challenge: "b1"}, ExpiresAt: now.Add(time.Minute),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "alice-login-2", Kind: KindLogin, UserID: alice,
			SessionData: &gowebauthn.SessionData{Challenge: "2"}, ExpiresAt: now.Add(time.Minute),
		}))

		_, ok := s.Get("alice-login-1")
		assert.False(t, ok, "prior (alice, KindLogin) must be replaced atomically")
		_, ok = s.Get("alice-login-2")
		assert.True(t, ok)
		_, ok = s.Get("alice-elev-1")
		assert.True(t, ok, "other kinds for same user must survive")
		_, ok = s.Get("bob-login-1")
		assert.True(t, ok, "same kind for another user must survive")

		// Anonymous KindSignup ceremonies coexist.
		require.NoError(t, s.Create(CeremonySession{
			ID: "signup-a", Kind: KindSignup,
			SessionData: &gowebauthn.SessionData{Challenge: "sa"}, ExpiresAt: now.Add(time.Minute),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "signup-b", Kind: KindSignup,
			SessionData: &gowebauthn.SessionData{Challenge: "sb"}, ExpiresAt: now.Add(time.Minute),
		}))
		_, ok = s.Get("signup-a")
		assert.True(t, ok)
		_, ok = s.Get("signup-b")
		assert.True(t, ok)
	})

	t.Run("consume expiry boundary, wrong kind, and token mismatch leave ceremony unconsumed", func(t *testing.T) {
		s := NewSessionStore()
		expiry := now.Add(time.Minute)

		require.NoError(t, s.Create(CeremonySession{
			ID: "rec-1", Kind: KindRecovery, UserID: alice, TokenHash: "good-hash",
			SessionData: &gowebauthn.SessionData{Challenge: "r1"}, ExpiresAt: expiry,
		}))

		// Wrong kind does not consume.
		_, err := s.Consume("rec-1", KindRegister, "good-hash", now)
		assert.ErrorIs(t, err, ErrCeremonyInvalid)
		_, ok := s.Get("rec-1")
		assert.True(t, ok)

		// Wrong token hash does not consume.
		_, err = s.Consume("rec-1", KindRecovery, "wrong-hash", now)
		assert.ErrorIs(t, err, ErrCeremonyInvalid)
		_, ok = s.Get("rec-1")
		assert.True(t, ok)

		// Exact expiry instant (ExpiresAt == now) is refused and does not consume.
		_, err = s.Consume("rec-1", KindRecovery, "good-hash", expiry)
		assert.ErrorIs(t, err, ErrCeremonyInvalid)
		_, ok = s.Get("rec-1")
		assert.True(t, ok)

		// Matching kind + token before expiry succeeds once.
		got, err := s.Consume("rec-1", KindRecovery, "good-hash", expiry.Add(-time.Millisecond))
		require.NoError(t, err)
		assert.Equal(t, "r1", got.SessionData.Challenge)

		// Replay fails.
		_, err = s.Consume("rec-1", KindRecovery, "good-hash", expiry.Add(-time.Millisecond))
		assert.ErrorIs(t, err, ErrCeremonyInvalid)
	})

	t.Run("capacity cap lazy eviction and delete expired boundaries", func(t *testing.T) {
		cur := now
		s := NewSessionStore()
		s.maxEntries = 2
		s.now = func() time.Time { return cur }

		require.NoError(t, s.Create(CeremonySession{
			ID: "c1", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "1"},
			ExpiresAt: cur.Add(time.Second),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "c2", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "2"},
			ExpiresAt: cur.Add(time.Hour),
		}))
		assert.ErrorIs(t, s.Create(CeremonySession{
			ID: "c3", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "3"},
			ExpiresAt: cur.Add(time.Hour),
		}), errWebAuthnSessionCapacity)

		cur = cur.Add(2 * time.Second)
		require.NoError(t, s.Create(CeremonySession{
			ID: "c3", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "3"},
			ExpiresAt: cur.Add(time.Hour),
		}))
		_, ok := s.Get("c1")
		assert.False(t, ok)
		_, ok = s.Get("c3")
		assert.True(t, ok)

		// Strict < boundary on DeleteExpired.
		s2 := NewSessionStore()
		require.NoError(t, s2.Create(CeremonySession{ID: "e1", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "1"}, ExpiresAt: now.Add(-time.Millisecond)}))
		require.NoError(t, s2.Create(CeremonySession{ID: "e2", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "2"}, ExpiresAt: now}))
		require.NoError(t, s2.Create(CeremonySession{ID: "e3", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "3"}, ExpiresAt: now.Add(time.Millisecond)}))
		assert.EqualValues(t, 1, s2.DeleteExpired(now))
		_, ok = s2.Get("e1")
		assert.False(t, ok)
		_, ok = s2.Get("e2")
		assert.True(t, ok)
		_, ok = s2.Get("e3")
		assert.True(t, ok)
	})

	t.Run("out-of-order expiry at capacity", func(t *testing.T) {
		cur := now
		s := NewSessionStore()
		s.maxEntries = 2
		s.now = func() time.Time { return cur }

		require.NoError(t, s.Create(CeremonySession{
			ID: "c1", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "1"},
			ExpiresAt: cur.Add(time.Hour),
		}))
		require.NoError(t, s.Create(CeremonySession{
			ID: "c2", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "2"},
			ExpiresAt: cur.Add(time.Second),
		}))

		cur = now.Add(time.Second)
		assert.ErrorIs(t, s.Create(CeremonySession{
			ID: "c3", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "3"},
			ExpiresAt: cur.Add(time.Hour),
		}), errWebAuthnSessionCapacity)

		cur = now.Add(2 * time.Second)
		require.NoError(t, s.Create(CeremonySession{
			ID: "c3", Kind: KindSignup, SessionData: &gowebauthn.SessionData{Challenge: "3"},
			ExpiresAt: cur.Add(time.Hour),
		}))
		_, ok := s.Get("c2")
		assert.False(t, ok)
		_, ok = s.Get("c1")
		assert.True(t, ok)
		_, ok = s.Get("c3")
		assert.True(t, ok)
	})

	t.Run("per-user replacement at capacity", func(t *testing.T) {
		charlie := userid.MustNew("user-charlie")

		t.Run("maxEntries=2", func(t *testing.T) {
			cur := now
			s := NewSessionStore()
			s.maxEntries = 2
			s.now = func() time.Time { return cur }

			require.NoError(t, s.Create(CeremonySession{
				ID: "alice-1", Kind: KindLogin, UserID: alice,
				SessionData: &gowebauthn.SessionData{Challenge: "a1"}, ExpiresAt: cur.Add(time.Hour),
			}))
			require.NoError(t, s.Create(CeremonySession{
				ID: "bob-1", Kind: KindLogin, UserID: bob,
				SessionData: &gowebauthn.SessionData{Challenge: "b1"}, ExpiresAt: cur.Add(2 * time.Hour),
			}))

			// Replacing (alice, KindLogin) at capacity must succeed and track the shorter expiry.
			require.NoError(t, s.Create(CeremonySession{
				ID: "alice-2", Kind: KindLogin, UserID: alice,
				SessionData: &gowebauthn.SessionData{Challenge: "a2"}, ExpiresAt: cur.Add(time.Second),
			}))
			_, ok := s.Get("alice-1")
			assert.False(t, ok)
			_, ok = s.Get("alice-2")
			assert.True(t, ok)

			cur = cur.Add(2 * time.Second)
			require.NoError(t, s.Create(CeremonySession{
				ID: "charlie-1", Kind: KindLogin, UserID: charlie,
				SessionData: &gowebauthn.SessionData{Challenge: "c1"}, ExpiresAt: cur.Add(time.Hour),
			}))
			_, ok = s.Get("alice-2")
			assert.False(t, ok)
			_, ok = s.Get("charlie-1")
			assert.True(t, ok)
			_, ok = s.Get("bob-1")
			assert.True(t, ok)
		})

		t.Run("maxEntries=1", func(t *testing.T) {
			cur := now
			s := NewSessionStore()
			s.maxEntries = 1
			s.now = func() time.Time { return cur }

			require.NoError(t, s.Create(CeremonySession{
				ID: "alice-1", Kind: KindLogin, UserID: alice,
				SessionData: &gowebauthn.SessionData{Challenge: "a1"}, ExpiresAt: cur.Add(time.Hour),
			}))
			require.NoError(t, s.Create(CeremonySession{
				ID: "alice-2", Kind: KindLogin, UserID: alice,
				SessionData: &gowebauthn.SessionData{Challenge: "a2"}, ExpiresAt: cur.Add(time.Second),
			}))
			_, ok := s.Get("alice-1")
			assert.False(t, ok)
			_, ok = s.Get("alice-2")
			assert.True(t, ok)

			cur = cur.Add(2 * time.Second)
			require.NoError(t, s.Create(CeremonySession{
				ID: "bob-1", Kind: KindLogin, UserID: bob,
				SessionData: &gowebauthn.SessionData{Challenge: "b1"}, ExpiresAt: cur.Add(time.Hour),
			}))
			_, ok = s.Get("alice-2")
			assert.False(t, ok)
			_, ok = s.Get("bob-1")
			assert.True(t, ok)
		})
	})
}

func TestSessionStore_DeleteByUserBeforeGeneration(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	userA := userid.MustNew("user-a")
	userB := userid.MustNew("user-b")

	s := NewSessionStore()
	require.NoError(t, s.Create(CeremonySession{
		ID: "a-gen1", Kind: KindLogin, UserID: userA, UserAuthGeneration: 1,
		SessionData: &gowebauthn.SessionData{Challenge: "a1"}, ExpiresAt: now.Add(time.Minute),
	}))
	require.NoError(t, s.Create(CeremonySession{
		ID: "a-gen2", Kind: KindElevation, UserID: userA, UserAuthGeneration: 2,
		SessionData: &gowebauthn.SessionData{Challenge: "a2"}, ExpiresAt: now.Add(time.Minute),
	}))
	require.NoError(t, s.Create(CeremonySession{
		ID: "b-gen1", Kind: KindLogin, UserID: userB, UserAuthGeneration: 1,
		SessionData: &gowebauthn.SessionData{Challenge: "b1"}, ExpiresAt: now.Add(time.Minute),
	}))

	assert.Zero(t, s.DeleteByUserBeforeGeneration(userid.UserID{}, 2), "zero user ID must evict nothing")
	assert.EqualValues(t, 1, s.DeleteByUserBeforeGeneration(userA, 2))
	_, ok := s.Get("a-gen1")
	assert.False(t, ok, "older generation ceremony for user A must be evicted")
	_, ok = s.Get("a-gen2")
	assert.True(t, ok, "current generation ceremony for user A must survive")
	_, ok = s.Get("b-gen1")
	assert.True(t, ok, "user B ceremony must survive")

	// Non-positive generation evicts all remaining sessions for user A.
	assert.EqualValues(t, 1, s.DeleteByUserBeforeGeneration(userA, 0))
	_, ok = s.Get("a-gen2")
	assert.False(t, ok)
	_, ok = s.Get("b-gen1")
	assert.True(t, ok)
}

func TestSessionStore_ConcurrentCreateAndConsume(t *testing.T) {
	now := time.Date(2030, 5, 1, 12, 0, 0, 0, time.UTC)
	alice := userid.MustNew("user-alice")
	s := NewSessionStore()

	// 16 goroutines racing Consume on a shared session: at most 1 wins.
	require.NoError(t, s.Create(CeremonySession{
		ID: "shared", Kind: KindLogin, UserID: alice,
		SessionData: &gowebauthn.SessionData{Challenge: "shared"}, ExpiresAt: now.Add(time.Minute),
	}))

	const workers = 16
	start := make(chan struct{})
	var consumedShared atomic.Int64
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := range workers {
		go func(idx int) {
			defer wg.Done()
			<-start
			if _, err := s.Consume("shared", KindLogin, "", now); err == nil {
				consumedShared.Add(1)
			}
			// Also race per-user Create replacements + consumes under -race.
			id := fmt.Sprintf("race-%d", idx)
			_ = s.Create(CeremonySession{
				ID: id, Kind: KindElevation, UserID: alice,
				SessionData: &gowebauthn.SessionData{Challenge: id}, ExpiresAt: now.Add(time.Minute),
			})
			_, _ = s.Consume(id, KindElevation, "", now)
		}(i)
	}
	close(start)
	wg.Wait()
	assert.EqualValues(t, 1, consumedShared.Load(), "at most one goroutine may consume a session ID")
}
