package storetest

import (
	"errors"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// expiringLeaseDuration is the lease life used by the cases that need a lease to
// lapse. Every assertion that depends on it waits a multiple of it, never races
// it: a test must not require a lease to still be live across a store round-trip,
// because a loaded CI runner (or a distributed store, where a single statement
// costs tens of milliseconds) makes that a coin flip.
const expiringLeaseDuration = 25 * time.Millisecond

func (s *Suite) testTokenRevocation(t *testing.T) {
	t.Run("API refresh rotation creates one cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		tokenID := id.Generate()
		oldRefreshHash := []byte("old-refresh-hash")
		require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID: tokenID, UserID: userid.MustNew(user.ID), ClientID: oauthapp.ControlCLIClientID, InstallationName: "test", GrantedScopes: authscope.NonAdminGrant().String(),
			SecretHash: []byte("old-access-hash"), RefreshHash: oldRefreshHash,
		}))
		expiresAt := time.Now().Add(time.Hour)
		refreshExpiresAt := time.Now().Add(24 * time.Hour)
		previousRefreshExpiresAt := time.Now().Add(time.Minute)
		params := store.RotateAPITokenRefreshParams{
			ID:                       tokenID,
			NewSecretHash:            []byte("new-access-hash"),
			NewExpiresAt:             &expiresAt,
			NewRefreshHash:           []byte("new-refresh-hash"),
			NewRefreshExpiresAt:      &refreshExpiresAt,
			PreviousRefreshHash:      oldRefreshHash,
			PreviousRefreshExpiresAt: &previousRefreshExpiresAt,
		}

		n, err := st.APITokens().RotateRefresh(ctx, params)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindAPITokenRotation, events[0].Event.Kind)
		assert.Equal(t, tokenID, events[0].Event.SubjectID)
		assert.Equal(t, user.ID, events[0].Event.UserID)
		assert.False(t, events[0].Event.RevokedAt.IsZero())

		n, err = st.APITokens().RotateRefresh(ctx, params)
		require.NoError(t, err)
		assert.Zero(t, n)
		published, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published, "failed refresh CAS must not emit an event")
	})

	t.Run("single token revoke creates one durable event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		apiToken := seedAPIToken(t, st, user.ID)
		delegationToken := seedDelegationToken(t, st, user.ID)

		n, err := st.APITokens().Revoke(ctx, apiToken)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
		n, err = st.DelegationTokens().Revoke(ctx, delegationToken)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(2), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		assert.ElementsMatch(t, []string{apiToken, delegationToken}, store.MapSlice(events, func(e store.PublishedRevocationEvent) string {
			return e.Event.SubjectID
		}))
		assert.ElementsMatch(t, []string{store.RevocationEventKindAPIToken, store.RevocationEventKindDelegationToken}, store.MapSlice(events, func(e store.PublishedRevocationEvent) string {
			return e.Event.Kind
		}))
	})

	t.Run("idempotent re-revoke creates no extra event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		tokenID := seedAPIToken(t, st, user.ID)

		n, err := st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)
		require.Equal(t, int64(1), n)
		n, err = st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)
		require.Equal(t, int64(0), n)

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
	})

	t.Run("bulk revoke fast-revokes live rows without per-token events", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		seedAPIToken(t, st, user.ID)
		seedAPIToken(t, st, user.ID)
		seedAPIToken(t, st, user.ID)
		alreadyRevoked := seedAPIToken(t, st, user.ID)
		_, err := st.APITokens().Revoke(ctx, alreadyRevoked)
		require.NoError(t, err)
		_, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)

		n, err := st.APITokens().RevokeByUser(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		require.Equal(t, int64(3), n, "only the three still-live tokens are revoked")

		// Bulk user revoke is the fast path: it emits no per-token events
		// because the user-wide RevokeUserTokens event carries the signal.
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published, "fast bulk revoke must not emit per-token events")
	})

	t.Run("bulk revoke with an exclusion keeps the acting credential", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		acting := seedAPIToken(t, st, user.ID)
		firstOther := seedAPIToken(t, st, user.ID)
		secondOther := seedAPIToken(t, st, user.ID)
		stranger := SeedUser(t, st, "stranger")
		strangerToken := seedAPIToken(t, st, stranger.ID)

		n, err := st.APITokens().RevokeOthers(ctx, store.RevokeOtherAPITokensParams{
			UserID: userid.MustNew(user.ID),
			KeepID: acting,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), n, "the two other live tokens of this account are revoked")

		assertAPITokenRevoked(t, st, acting, false)
		assertAPITokenRevoked(t, st, firstOther, true)
		assertAPITokenRevoked(t, st, secondOther, true)
		assertAPITokenRevoked(t, st, strangerToken, false)

		// The exclusion path takes the same fast statement, so it emits no
		// per-token events either; the caller's user-wide generation event
		// carries the signal.
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published, "the exclusion path must not emit per-token events")
	})

	t.Run("bulk revoke with an empty exclusion revokes every row", func(t *testing.T) {
		// The behaviour every administrator path depends on: a reset and an
		// account delete keep nothing. RevokeByUser binds this shape.
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		first := seedAPIToken(t, st, user.ID)
		second := seedAPIToken(t, st, user.ID)

		n, err := st.APITokens().RevokeOthers(ctx, store.RevokeOtherAPITokensParams{
			UserID: userid.MustNew(user.ID),
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), n, "an empty exclusion keeps nothing")
		assertAPITokenRevoked(t, st, first, true)
		assertAPITokenRevoked(t, st, second, true)
	})

	t.Run("bulk revoke with an exclusion refuses a zero user id", func(t *testing.T) {
		// An unminted caller owns nothing, and a bulk revoke that reported
		// success while it addressed no row is the shape a revocation must
		// never have.
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		token := seedAPIToken(t, st, user.ID)

		_, err := st.APITokens().RevokeOthers(ctx, store.RevokeOtherAPITokensParams{
			UserID: userid.UserID{},
			KeepID: "keep",
		})
		require.ErrorIs(t, err, store.ErrInvalidArgument)
		assertAPITokenRevoked(t, st, token, false)
	})

	t.Run("api token auth generation restamp moves the kept credential forward", func(t *testing.T) {
		// The other half of "preserve this credential". Bearer validation
		// refuses a row behind users.auth_generation whether or not
		// revoked_at is set, so an exclusion without a restamp keeps a row
		// that still reads as revoked.
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		owner := userid.MustNew(user.ID)
		acting := seedAPIToken(t, st, user.ID)

		before, err := st.APITokens().GetByID(ctx, acting)
		require.NoError(t, err)

		bumped, err := st.Users().RevokeUserTokens(ctx, owner)
		require.NoError(t, err)
		require.Equal(t, int64(1), bumped)
		updatedUser, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		require.Greater(t, updatedUser.AuthGeneration, before.AuthGeneration,
			"the epoch bump must leave the token row behind, or this case proves nothing")

		n, err := st.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: acting,
			UserID:  owner,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)

		after, err := st.APITokens().GetByID(ctx, acting)
		require.NoError(t, err)
		assert.Equal(t, updatedUser.AuthGeneration, after.AuthGeneration,
			"the kept credential now sits at the account's new epoch")
	})

	t.Run("api token auth generation restamp addresses no other row", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		owner := userid.MustNew(user.ID)
		revoked := seedAPIToken(t, st, user.ID)
		_, err := st.APITokens().Revoke(ctx, revoked)
		require.NoError(t, err)
		stranger := SeedUser(t, st, "stranger")
		strangerToken := seedAPIToken(t, st, stranger.ID)

		// A REVOKED row: the restamp keeps a live credential live, and must
		// never move a dead one onto the new epoch.
		n, err := st.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: revoked, UserID: owner,
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a revoked row is not restamped")

		// Somebody else's row: the owner equality is in the statement.
		n, err = st.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: strangerToken, UserID: owner,
		})
		require.NoError(t, err)
		assert.Zero(t, n, "another account's row is not restamped")

		// A row that no longer exists.
		n, err = st.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: id.Generate(), UserID: owner,
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a missing row is not restamped")

		// An unminted caller, which reports zero rather than an error --
		// the session twin answers the same way, and the caller treats
		// "nothing to restamp" as tolerable.
		n, err = st.APITokens().RefreshAuthGeneration(ctx, store.RefreshAPITokenAuthGenerationParams{
			TokenID: strangerToken, UserID: userid.UserID{},
		})
		require.NoError(t, err)
		assert.Zero(t, n, "a zero user id addresses no row")
	})

	t.Run("user-wide bulk revoke emits only the generation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		seedAPIToken(t, st, user.ID)
		seedDelegationToken(t, st, user.ID)

		require.NoError(t, st.RunInTransaction(ctx, func(tx store.Store) error {
			apiCount, err := tx.APITokens().RevokeByUser(ctx, userid.MustNew(user.ID))
			if err != nil {
				return err
			}
			require.Equal(t, int64(1), apiCount)
			delegationCount, err := tx.DelegationTokens().RevokeByUser(ctx, userid.MustNew(user.ID))
			if err != nil {
				return err
			}
			require.Equal(t, int64(1), delegationCount)
			_, err = tx.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
			return err
		}))

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindUserTokens, events[0].Event.Kind)
		assert.Equal(t, int64(1), events[0].Event.UserAuthGeneration)
	})

	t.Run("user bumps create timestamped user-token events", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		for range 2 {
			n, err := st.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
		}
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(2), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2)
		for _, event := range events {
			assert.Equal(t, store.RevocationEventKindUserTokens, event.Event.Kind)
			assert.Equal(t, user.ID, event.Event.UserID)
			assert.False(t, event.Event.RevokedAt.IsZero())
		}
		assert.ElementsMatch(t, []int64{1, 2}, []int64{
			events[0].Event.UserAuthGeneration,
			events[1].Event.UserAuthGeneration,
		})
	})

	t.Run("user-token revoke still fires after soft-delete", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		require.NoError(t, st.Users().Delete(ctx, user.ID))

		// A revoke-after-soft-delete caller must still bump the generation
		// and emit the durable user_tokens event so cross-process teardown
		// runs even though the user row is soft-deleted.
		n, err := st.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)
		require.Equal(t, int64(1), n, "soft-deleted user must still be revoked")

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindUserTokens, events[0].Event.Kind)
		assert.Equal(t, user.ID, events[0].Event.UserID)
		assert.Equal(t, int64(1), events[0].Event.UserAuthGeneration)
	})

	t.Run("user-token revoke on missing user is a no-op", func(t *testing.T) {
		st := s.NewStore(t)

		n, err := st.Users().RevokeUserTokens(ctx, userid.MustNew("does-not-exist"))
		require.NoError(t, err)
		require.Zero(t, n, "a missing user row is the only case that revokes nothing")

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published)
	})

	t.Run("admin change emits a user_info cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		// Grant (false→true) stays on the soft user_info path.
		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      user.ID,
			IsAdmin: true,
		}))

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindUserInfo, events[0].Event.Kind)
		assert.Equal(t, user.ID, events[0].Event.SubjectID)
		assert.Equal(t, user.ID, events[0].Event.UserID)
		assert.Equal(t, int64(0), events[0].Event.UserAuthGeneration, "user_info is a cache signal, not generation-bearing")
		assert.False(t, events[0].Event.RevokedAt.IsZero())
	})

	t.Run("admin demotion (is_admin true->false) emits a generation-bearing user_tokens revocation", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID: user.ID, IsAdmin: true,
		}))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		before, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		genBefore := before.AuthGeneration

		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID: user.ID, IsAdmin: false,
		}))
		published, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2)
		ev := events[1].Event
		assert.Equal(t, store.RevocationEventKindUserTokens, ev.Kind)
		assert.Greater(t, ev.UserAuthGeneration, int64(0))
		assert.Equal(t, user.ID, ev.SubjectID)
		assert.Equal(t, user.ID, ev.UserID)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, genBefore+1, after.AuthGeneration)
		assert.Equal(t, after.AuthGeneration, ev.UserAuthGeneration)
	})

	t.Run("admin change on missing user emits nothing", func(t *testing.T) {
		st := s.NewStore(t)

		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      "does-not-exist",
			IsAdmin: true,
		}))

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published)
	})

	t.Run("user_info mutation on a soft-deleted user still applies", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		require.NoError(t, st.Users().Delete(ctx, user.ID)) // soft delete: sets deleted_at

		// runUserInfoMutation serializes its before/after cached-field projection
		// with LockUserRow, which -- unlike LockUserAuthState -- does NOT filter
		// deleted_at. A mutation on a soft-deleted user therefore still applies,
		// instead of aborting because the lock returned not-found.
		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID:      user.ID,
			IsAdmin: true,
		}))
		got, err := st.Users().GetByIDIncludeDeleted(ctx, user.ID)
		require.NoError(t, err)
		assert.True(t, got.IsAdmin, "the admin flag must apply even though the user is soft-deleted")
	})

	// The username, email, and email_verified fields are all cached in
	// auth.UserInfo (email_verified is a live auth gate), so an out-of-process
	// mutation to any of them must invalidate the cache cross-process just like an
	// IsAdmin change -- otherwise a running Hub serves the stale value until the
	// cache TTL elapses.
	assertUserInfoEvent := func(t *testing.T, st store.Store, userID string, mutate func() error) {
		t.Helper()
		require.NoError(t, mutate())
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, store.RevocationEventKindUserInfo, events[0].Event.Kind)
		assert.Equal(t, userID, events[0].Event.SubjectID)
		assert.Equal(t, userID, events[0].Event.UserID)
		assert.Equal(t, int64(0), events[0].Event.UserAuthGeneration, "user_info is a cache signal, not generation-bearing")
		assert.False(t, events[0].Event.RevokedAt.IsZero())
	}

	t.Run("profile change emits a user_info cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		assertUserInfoEvent(t, st, user.ID, func() error {
			return st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
				ID:          user.ID,
				Username:    "user-renamed",
				DisplayName: "Renamed",
			})
		})
	})

	t.Run("display-name-only profile change emits no user_info event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		// The username (the only cached UserInfo field this mutation touches) is
		// unchanged, so RunUserInfoMutation derives that no cached field changed
		// (before == after projection) and suppresses the fleet-wide user_info
		// invalidation. The row still updates -- only the durable event is skipped.
		require.NoError(t, st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID:          user.ID,
			Username:    user.Username,
			DisplayName: "New Display Name",
		}))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published, "a display-name-only edit must not emit a user_info event")
		updated, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "New Display Name", updated.DisplayName, "the display-name update must still persist")
	})

	t.Run("email change emits a user_info cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		assertUserInfoEvent(t, st, user.ID, func() error {
			return st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
				ID:            user.ID,
				Email:         "new@example.com",
				EmailVerified: true,
			})
		})
	})

	t.Run("email_verified change emits a user_info cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		// Seed unverified so the flip to true is a grant (soft user_info path).
		userID := id.Generate()
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			Username:      "unverified-grant",
			PasswordHash:  "hash",
			DisplayName:   "Unverified",
			Email:         "unverified-grant@example.com",
			EmailVerified: false,
			PasswordSet:   true,
			IsAdmin:       false,
		}))
		assertUserInfoEvent(t, st, userID, func() error {
			return st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
				ID:            userID,
				EmailVerified: true,
			})
		})
	})

	t.Run("email un-verify (email_verified true->false) emits a generation-bearing user_tokens revocation", func(t *testing.T) {
		st := s.NewStore(t)
		userID := id.Generate()
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID:            userID,
			Username:      "to-unverify",
			PasswordHash:  "hash",
			DisplayName:   "To Unverify",
			Email:         "to-unverify@example.com",
			EmailVerified: false,
			PasswordSet:   true,
			IsAdmin:       false,
		}))

		require.NoError(t, st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
			ID: userID, EmailVerified: true,
		}))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		before, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		genBefore := before.AuthGeneration

		require.NoError(t, st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
			ID: userID, EmailVerified: false,
		}))
		published, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2)
		ev := events[1].Event
		assert.Equal(t, store.RevocationEventKindUserTokens, ev.Kind)
		assert.Greater(t, ev.UserAuthGeneration, int64(0))
		assert.Equal(t, userID, ev.SubjectID)
		assert.Equal(t, userID, ev.UserID)

		after, err := st.Users().GetByID(ctx, userID)
		require.NoError(t, err)
		assert.Equal(t, genBefore+1, after.AuthGeneration)
		assert.Equal(t, after.AuthGeneration, ev.UserAuthGeneration)
	})

	t.Run("email address change dropping verified stays on the soft user_info path", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user") // seeded email_verified = true

		// Establish a distinct email+verified state via UpdateEmail (soft), drain it.
		require.NoError(t, st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: user.ID, Email: "verified@example.com", EmailVerified: true,
		}))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		before, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		genBefore := before.AuthGeneration

		// Address change that also drops verified must NOT fence (self-service path).
		require.NoError(t, st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: user.ID, Email: "new@example.com", EmailVerified: false,
		}))
		published, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2)
		ev := events[1].Event
		assert.Equal(t, store.RevocationEventKindUserInfo, ev.Kind)
		assert.Equal(t, int64(0), ev.UserAuthGeneration)

		after, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, genBefore, after.AuthGeneration)
		assert.False(t, after.EmailVerified)
		assert.Equal(t, "new@example.com", after.Email)
	})

	t.Run("no-op update to a cached field emits no user_info event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user") // seeded is_admin = false, email_verified = true
		// Re-set each cached field to its current value: RunUserInfoMutation
		// compares the before/after projection and must suppress the fleet-wide
		// eviction when nothing a cached field observes actually changed.
		require.NoError(t, st.Users().UpdateAdmin(ctx, store.UpdateUserAdminParams{
			ID: user.ID, IsAdmin: false,
		}))
		require.NoError(t, st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
			ID: user.ID, EmailVerified: true,
		}))
		require.NoError(t, st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: user.ID, Email: user.Email, EmailVerified: true,
		}))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published, "re-setting cached fields to their current values must emit nothing")
	})

	t.Run("pending-email promotion emits a user_info cache-invalidation event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		// Staging the pending email is a plain mutation (no event); only the
		// promotion changes the cached email/email_verified and must emit.
		expiresAt := time.Now().Add(time.Hour)
		minted, err := st.Users().SetPendingEmail(ctx, store.SetPendingEmailParams{
			ID:                    user.ID,
			PendingEmail:          "promoted@example.com",
			PendingEmailToken:     "tok",
			PendingEmailExpiresAt: &expiresAt,
			CooldownCutoff:        store.UnconditionalMintCutoff(),
		})
		mustSetPendingEmail(t, minted, err)
		assertUserInfoEvent(t, st, user.ID, func() error {
			return st.Users().PromotePendingEmail(ctx, user.ID)
		})
		promoted, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		assert.Equal(t, "promoted@example.com", promoted.Email)
		assert.True(t, promoted.EmailVerified)
	})

	t.Run("profile/email/email_verified/promote changes on missing user emit nothing", func(t *testing.T) {
		st := s.NewStore(t)

		require.NoError(t, st.Users().UpdateProfile(ctx, store.UpdateUserProfileParams{
			ID: "does-not-exist", Username: "ghost", DisplayName: "Ghost",
		}))
		require.NoError(t, st.Users().UpdateEmail(ctx, store.UpdateUserEmailParams{
			ID: "does-not-exist", Email: "ghost@example.com", EmailVerified: true,
		}))
		require.NoError(t, st.Users().UpdateEmailVerified(ctx, store.UpdateUserEmailVerifiedParams{
			ID: "does-not-exist", EmailVerified: true,
		}))
		// A promotion with no pending email in flight matches zero rows and is a
		// no-op with no event.
		require.NoError(t, st.Users().PromotePendingEmail(ctx, "does-not-exist"))

		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published)
	})

	t.Run("promotion with no pending email in flight emits nothing", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		require.NoError(t, st.Users().PromotePendingEmail(ctx, user.ID))
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Zero(t, published)
	})

	t.Run("new credentials copy current user auth generation", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		_, err := st.Users().RevokeUserTokens(ctx, userid.MustNew(user.ID))
		require.NoError(t, err)

		apiTokenID := seedAPIToken(t, st, user.ID)
		apiToken, err := st.APITokens().GetByID(ctx, apiTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), apiToken.AuthGeneration)

		delegationTokenID := seedDelegationToken(t, st, user.ID)
		delegationToken, err := st.DelegationTokens().GetByID(ctx, delegationTokenID)
		require.NoError(t, err)
		assert.Equal(t, int64(1), delegationToken.AuthGeneration)
	})

	t.Run("transaction rollback removes state transition and event", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		tokenID := seedAPIToken(t, st, user.ID)
		rollbackErr := errors.New("rollback")

		err := st.RunInTransaction(ctx, func(tx store.Store) error {
			n, err := tx.APITokens().Revoke(ctx, tokenID)
			require.NoError(t, err)
			require.Equal(t, int64(1), n)
			return rollbackErr
		})
		require.ErrorIs(t, err, rollbackErr)

		token, err := st.APITokens().GetByID(ctx, tokenID)
		require.NoError(t, err)
		require.Nil(t, token.RevokedAt)
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		assert.Equal(t, int64(0), published)
	})

	t.Run("publish is idempotent and assigns gapless sequence pages", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		tokens := []string{
			seedAPIToken(t, st, user.ID),
			seedAPIToken(t, st, user.ID),
			seedAPIToken(t, st, user.ID),
		}
		for _, token := range tokens {
			_, err := st.APITokens().Revoke(ctx, token)
			require.NoError(t, err)
		}

		published, err := st.RevocationEvents().PublishPending(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, int64(2), published)
		published, err = st.RevocationEvents().PublishPending(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, int64(1), published)
		published, err = st.RevocationEvents().PublishPending(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, int64(0), published)

		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 2)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, int64(1), events[0].Seq)
		assert.Equal(t, int64(2), events[1].Seq)
		events, err = st.RevocationEvents().ListPublishedAfter(ctx, 2, 2)
		require.NoError(t, err)
		require.Len(t, events, 1)
		assert.Equal(t, int64(3), events[0].Seq)
		maxSeq, err := st.RevocationEvents().MaxPublishedSeq(ctx)
		require.NoError(t, err)
		assert.Equal(t, int64(3), maxSeq)
	})

	t.Run("singleton Hub lease bounds compaction and supports takeover", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")
		_, err := st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.Error(t, err)
		_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "invalid-duration", PublishLimit: 10, LeaseDuration: time.Nanosecond,
		})
		require.Error(t, err)

		fence, err := st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "first", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		require.Zero(t, fence)
		_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "second", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.ErrorIs(t, err, store.ErrHubAlreadyRunning)
		advanced, err := st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID: "impostor", CursorSeq: 0, LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		require.False(t, advanced)
		released, err := st.RevocationEvents().ReleaseHubRuntimeLease(ctx, "impostor")
		require.NoError(t, err)
		require.Zero(t, released)

		for range 3 {
			tokenID := seedAPIToken(t, st, user.ID)
			_, err := st.APITokens().Revoke(ctx, tokenID)
			require.NoError(t, err)
		}
		_, err = st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)

		// Park the cursor at seq 1 under a LONG lease. The bound this asserts is
		// the live lease's cursor_seq, and CompactPublished deletes the expired
		// lease row before compacting, so a lease that lapses between this renew
		// and the compaction below would silently unbound the sweep to last_seq
		// and delete all three events. Under a short lease that gap is two store
		// round-trips wide -- survivable on SQLite, not on a distributed store.
		// The expiry half of this test drives its own short lease further down.
		advanced, err = st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID:      "first",
			CursorSeq:     1,
			LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		require.True(t, advanced)

		deleted, err := st.Cleanup().CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
			Cutoff: time.Now().UTC().Add(time.Hour),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted, "a live lease must bound compaction to its cursor")
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2)
		assert.Equal(t, int64(2), events[0].Seq)

		// Now shorten the lease and outlive it. Re-renewing at the same cursor is
		// permitted (the guard is cursor_seq <= arg), so this only pulls the
		// deadline in. Unlike the compaction above, what follows needs the lease
		// merely to have lapsed -- a lower bound that CI load can only help.
		advanced, err = st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID:      "first",
			CursorSeq:     1,
			LeaseDuration: expiringLeaseDuration,
		})
		require.NoError(t, err)
		require.True(t, advanced)

		time.Sleep(4 * expiringLeaseDuration)
		advanced, err = st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID:      "first",
			CursorSeq:     3,
			LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		require.False(t, advanced, "an expired lease must not renew itself")

		fence, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "second", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		require.Equal(t, int64(3), fence)

		deleted, err = st.Cleanup().CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
			Cutoff: time.Now().UTC().Add(time.Hour),
		})
		require.NoError(t, err)
		require.Equal(t, int64(2), deleted)
		events, err = st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Empty(t, events)

		released, err = st.RevocationEvents().ReleaseHubRuntimeLease(ctx, "second")
		require.NoError(t, err)
		require.Equal(t, int64(1), released)
		_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "third", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.NoError(t, err, "clean release must permit immediate handoff")
	})

	// A Hub whose own lease lapsed while it could not renew -- a suspended
	// laptop, a paused VM -- must be able to take that lease back WITHOUT losing
	// its place in the stream, and must be refused whenever any other holder's
	// row is present.
	t.Run("a lapsed Hub lease is reacquirable by its own holder at its own cursor", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		require.Error(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "", CursorSeq: 0, LeaseDuration: time.Hour,
		}), "an empty holder must be rejected")
		require.Error(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: -1, LeaseDuration: time.Hour,
		}), "a negative cursor must be rejected")
		require.Error(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 0, LeaseDuration: time.Nanosecond,
		}), "a sub-millisecond lease must be rejected")

		_, err := st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "first", PublishLimit: 10, LeaseDuration: expiringLeaseDuration,
		})
		require.NoError(t, err)

		// A rival must not take a live row. The holder itself may: local clocks
		// can report a lapse while this row is still live, and that must become
		// a force-renewal rather than a false "another Hub" fence.
		require.ErrorIs(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "second", CursorSeq: 0, LeaseDuration: time.Hour,
		}), store.ErrHubAlreadyRunning)
		require.NoError(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 0, LeaseDuration: expiringLeaseDuration,
		}))

		for range 3 {
			tokenID := seedAPIToken(t, st, user.ID)
			_, revokeErr := st.APITokens().Revoke(ctx, tokenID)
			require.NoError(t, revokeErr)
		}
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(3), published)

		time.Sleep(4 * expiringLeaseDuration)

		// Another holder's EXPIRED row also refuses: that Hub may have consumed
		// and compacted past this cursor, which no reacquisition can undo.
		require.ErrorIs(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "second", CursorSeq: 0, LeaseDuration: time.Hour,
		}), store.ErrHubAlreadyRunning)

		// The original holder takes its own lapsed lease back, keeping cursor 1
		// where AcquireHubRuntimeLease would have fenced to the head at 3.
		require.NoError(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 1, LeaseDuration: time.Hour,
		}))

		// The lease is live again: a rival is fenced out, and the recovered
		// cursor -- not the head of the stream -- bounds compaction, so the two
		// events this Hub still owes delivery on survive.
		_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "rival", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.ErrorIs(t, err, store.ErrHubAlreadyRunning)
		deleted, err := st.Cleanup().CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
			Cutoff: time.Now().UTC().Add(time.Hour),
		})
		require.NoError(t, err)
		require.Equal(t, int64(1), deleted, "the reacquired cursor must bound compaction to seq 1")
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Len(t, events, 2, "events past the recovered cursor must survive for replay")
		assert.Equal(t, int64(2), events[0].Seq)

		// Renewal works on the reacquired row, so recovery hands a usable lease
		// back to the normal heartbeat rather than a row only reacquisition can
		// touch again.
		advanced, err := st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 3, LeaseDuration: time.Hour,
		})
		require.NoError(t, err)
		assert.True(t, advanced)
	})

	// A lease whose row was swept entirely -- CompactPublished deletes an expired
	// row before it compacts -- must still be reacquirable. This is the ordinary
	// state a Hub wakes into after sleeping through a cleanup pass.
	t.Run("a swept Hub lease is reacquirable at its own cursor", func(t *testing.T) {
		st := s.NewStore(t)

		_, err := st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "first", PublishLimit: 10, LeaseDuration: expiringLeaseDuration,
		})
		require.NoError(t, err)
		time.Sleep(4 * expiringLeaseDuration)

		// The cleanup pass that sweeps the expired lease row.
		_, err = st.Cleanup().CompactPublishedRevocationEvents(ctx, store.CompactRevocationEventsParams{
			Cutoff: time.Now().UTC().Add(time.Hour),
		})
		require.NoError(t, err)

		require.NoError(t, st.RevocationEvents().ReacquireHubRuntimeLease(ctx, store.ReacquireHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 0, LeaseDuration: time.Hour,
		}))
		_, err = st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "rival", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.ErrorIs(t, err, store.ErrHubAlreadyRunning,
			"reacquiring a swept lease must restore a live, exclusive row")
	})

	// The bound compaction applies is the LIVE lease's cursor; an abandoned Hub
	// must not pin the backlog forever. CompactPublished deletes the expired
	// lease row before compacting, so the COALESCE bound falls back to last_seq
	// once the lease lapses. That fallback is exactly what a lease which quietly
	// expired mid-test turned into a full sweep, so assert it deliberately here
	// rather than leaving it as the silent failure mode of another case.
	t.Run("an expired Hub lease stops bounding compaction", func(t *testing.T) {
		st := s.NewStore(t)
		user := SeedUser(t, st, "user")

		_, err := st.RevocationEvents().AcquireHubRuntimeLease(ctx, store.AcquireHubRuntimeLeaseParams{
			HolderID: "first", PublishLimit: 10, LeaseDuration: time.Hour,
		})
		require.NoError(t, err)

		for range 2 {
			tokenID := seedAPIToken(t, st, user.ID)
			_, err := st.APITokens().Revoke(ctx, tokenID)
			require.NoError(t, err)
		}
		published, err := st.RevocationEvents().PublishPending(ctx, 10)
		require.NoError(t, err)
		require.Equal(t, int64(2), published)

		compact := func() int64 {
			deleted, compactErr := st.Cleanup().CompactPublishedRevocationEvents(
				ctx,
				store.CompactRevocationEventsParams{Cutoff: time.Now().UTC().Add(time.Hour)},
			)
			require.NoError(t, compactErr)
			return deleted
		}

		// The lease was acquired before either event existed, so its cursor sits at
		// seq 0: a live Hub has consumed nothing, and no cutoff may collect events
		// it still owes delivery on.
		require.Zero(t, compact(), "a live lease at cursor 0 must block compaction entirely")

		// Same holder, deliberately short lease -- then outlive it.
		advanced, err := st.RevocationEvents().RenewHubRuntimeLease(ctx, store.RenewHubRuntimeLeaseParams{
			HolderID: "first", CursorSeq: 0, LeaseDuration: expiringLeaseDuration,
		})
		require.NoError(t, err)
		require.True(t, advanced)
		time.Sleep(4 * expiringLeaseDuration)

		require.Equal(t, int64(2), compact(), "an expired lease must stop bounding compaction")
		events, err := st.RevocationEvents().ListPublishedAfter(ctx, 0, 10)
		require.NoError(t, err)
		require.Empty(t, events)
	})
}

// assertAPITokenRevoked reads one api_tokens row and asserts whether it
// carries a revoked_at. A helper because the exclusion cases assert the state
// of four rows each, and a bare GetByID pair at every site buries the claim.
func assertAPITokenRevoked(t *testing.T, st store.Store, tokenID string, revoked bool) {
	t.Helper()
	row, err := st.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	if revoked {
		assert.NotNil(t, row.RevokedAt, "token %s must be revoked", tokenID)
		return
	}
	assert.Nil(t, row.RevokedAt, "token %s must survive", tokenID)
}

func seedAPIToken(t *testing.T, st store.Store, userID string) string {
	t.Helper()
	tokenID := id.Generate()
	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "test",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       []byte("hash"),
	}))
	return tokenID
}

func seedDelegationToken(t *testing.T, st store.Store, userID string) string {
	t.Helper()
	worker := SeedWorker(t, st, userID)
	wsID := SeedWorkspace(t, st, userID, "ws")
	tabID := id.Generate()
	require.NoError(t, st.WorkspaceTabIndex().UpsertOwned(ctx, store.UpsertOwnedTabParams{
		UserID:      userid.MustNew(userID),
		WorkspaceID: wsID,
		WorkerID:    worker.ID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		Position:    "a",
		TileID:      "tile-1",
	}))
	tokenID := id.Generate()
	require.NoError(t, st.DelegationTokens().Create(ctx, store.CreateDelegationTokenParams{
		GrantedScopes:    "workspace:read workspace:write worker:read",
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		WorkerID:         worker.ID,
		IssuedForTabID:   tabID,
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       []byte("hash"),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	return tokenID
}
