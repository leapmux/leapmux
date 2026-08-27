package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// liveElevatedCredentialAt seeds a command-line credential with a current
// elevation and returns the UserInfo a request on it would carry, together
// with the api_tokens row id.
//
// The twin of liveElevatedSessionAt, and the two are deliberately alike: the
// locked re-check must ask both kinds the same question.
func liveElevatedCredentialAt(
	t *testing.T, st store.Store, user *store.User, now time.Time,
) (*auth.UserInfo, string) {
	t.Helper()
	ctx := context.Background()
	uid := userid.MustNew(user.ID)
	tokenID := id.Generate()
	require.NoError(t, st.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID:         tokenID,
		UserID:     uid,
		ClientType: "cli",
		ClientName: "test-cli",
		SecretHash: []byte("hash"),
	}))
	n, err := st.APITokens().Elevate(ctx, store.ElevateAPITokenParams{
		TokenID:            tokenID,
		UserID:             uid,
		ElevationProvenAt:  now,
		ElevationExpiresAt: now.Add(auth.ElevationWindow),
	}, now)
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
	return &auth.UserInfo{ID: uid, Credential: auth.APICredential(tokenID)}, tokenID
}

// TestRecheckStepUpUnderLock_CommandLineCredential drives the locked
// re-check on the OTHER elevatable credential kind.
//
// The re-check used to read Credential.SessionID() alone. That is empty for
// an api_tokens row, so an elevated command-line credential passed the
// admission and then met "account credentials changed; please retry" inside
// the transaction, on every attempt, permanently. The window it proved was
// never even read.
func TestRecheckStepUpUnderLock_CommandLineCredential(t *testing.T) {
	t.Parallel()

	elevated := stepUpAdmission{}

	t.Run("a live window on a command-line credential passes", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, _ := liveElevatedCredentialAt(t, st, user, now)

		assert.NoError(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), now),
			"an elevated command-line credential must reach the mutation")
	})

	// The case the finding asks for: the window closed between the
	// admission and the lock. Nothing legitimate waits two hours on this
	// lock, so this is a refusal rather than a race to tolerate -- the same
	// answer the session branch gives for its own lapsed window.
	t.Run("a window that lapsed under the lock refuses with a retry error", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, _ := liveElevatedCredentialAt(t, st, user, now)

		require.NoError(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), now),
			"precondition: the live window passes")

		later := now.Add(auth.ElevationWindow + time.Minute)
		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), later))
	})

	// The Finish leg's grace reaches this branch too, because it is one
	// window read by two predicates. A grace that reached the session branch
	// alone would refuse a command-line Finish the admission admitted.
	t.Run("the leg's grace applies to a command-line credential", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		provenAt := time.Now().UTC()
		info, _ := liveElevatedCredentialAt(t, st, user, provenAt)

		finishAt := provenAt.Add(auth.ElevationWindow + time.Second)
		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), finishAt))
		assert.NoError(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, finishStepUp(info), finishAt))
	})

	// A single-credential revoke leaves the account's credential epoch where
	// it is, so recheckCredentialEpochUnderLock cannot see it. The column
	// can, and this is the command-line counterpart of the session branch's
	// administrator-revoke refusal.
	t.Run("a credential revoked under the lock refuses", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info, tokenID := liveElevatedCredentialAt(t, st, user, now)

		require.NoError(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), now),
			"precondition: the live credential passes")

		n, err := st.APITokens().Revoke(ctx, tokenID)
		require.NoError(t, err)
		require.EqualValues(t, 1, n)
		locked, err := st.Users().GetByID(ctx, user.ID)
		require.NoError(t, err)
		require.Equal(t, locked.AuthGeneration, info.UserAuthGeneration,
			"precondition: a single-credential revoke leaves the epoch where it is")

		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, locked, user, elevated, entryStepUp(info), now))
	})

	// An ABSENT row refuses, where the session branch tolerates one. The
	// tolerance there is for the owner's own sign-out in another tab, and a
	// command-line credential has no such verb.
	t.Run("a credential that is gone refuses", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()
		info := &auth.UserInfo{
			ID:         userid.MustNew(user.ID),
			Credential: auth.APICredential(id.Generate()),
		}

		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(info), now))
	})

	// A credential that carries no elevatable row at all keeps the refusal.
	// No caller reaches it -- stepUpMutationAuth refuses such a credential
	// before the transaction opens -- so this pins the second guard.
	t.Run("a credential that carries no window refuses", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		st := newStepUpTestStore(t)
		user := stepUpUser(t, st, true)
		now := time.Now().UTC()

		delegated := &auth.UserInfo{
			ID:         userid.MustNew(user.ID),
			Credential: auth.DelegationCredential("del-1", "w-1"),
		}
		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(delegated), now))

		// The zero value (solo mode's synthetic credential) answers the same
		// way rather than reading as a session with an empty id.
		zeroCredential := &auth.UserInfo{ID: userid.MustNew(user.ID)}
		requireStepUpStateMoved(t,
			recheckStepUpUnderLock(ctx, st, user, user, elevated, entryStepUp(zeroCredential), now))
	})
}
