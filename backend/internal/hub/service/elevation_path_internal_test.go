package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
)

// TestAccountElevatesOnlyThroughAProvider pins the step-up path rule at its one
// source of truth.
//
// Three readers share it: the OAuth re-authentication leg ENFORCES it at both
// of its legs through providerMayElevateAccount, the first-credential branch
// of stepUpMutationAuth reads it to know an account has nothing to elevate
// WITH, and GetCurrentUser REPORTS it as may_elevate so the step-up form
// offers exactly the options the hub accepts. The form used to spell the rule
// out in TypeScript -- a second source of truth for an authorization
// decision, and the copy drifted at the first change.
//
// The rule reads the ACCOUNT and nothing else. It used to read the provider
// and the hub's passkey ability as well, for a second tier that bridged a
// passkey this hub could not run; that tier is gone, and this test covering
// only account shape is what its absence looks like.
func TestAccountElevatesOnlyThroughAProvider(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// The username is caller-supplied because Users().Create validates it,
	// and an id.Generate() value is not a legal one.
	newUser := func(t *testing.T, st store.Store, username string, passwordSet bool) *store.User {
		t.Helper()
		uid := id.Generate()
		require.NoError(t, st.Users().Create(ctx, store.CreateUserParams{
			ID: uid, Username: username, PasswordHash: "hash",
			DisplayName: "Test User", FirstCredentialExempt: passwordSet,
		}))
		user, err := st.Users().GetByID(ctx, uid)
		require.NoError(t, err)
		return user
	}

	// No password and no passkey: the provider IS the sign-in credential, so
	// the elevation is exactly as strong as the sign-in it stands on. Any
	// linked provider qualifies -- the rule never asks which.
	t.Run("an account with no other factor elevates through a provider", func(t *testing.T) {
		st := hubtestutil.OpenTestStore(t)
		user := newUser(t, st, "provideronly", false)

		may, err := accountElevatesOnlyThroughAProvider(ctx, st, user)
		require.NoError(t, err)
		assert.True(t, may)
	})

	// A password disqualifies the path. That is the one shape where the
	// account chose a secret and the provider path would let somebody past it
	// without knowing it.
	t.Run("a password disqualifies the path", func(t *testing.T) {
		st := hubtestutil.OpenTestStore(t)
		user := newUser(t, st, "haspassword", true)

		may, err := accountElevatesOnlyThroughAProvider(ctx, st, user)
		require.NoError(t, err)
		assert.False(t, may)
	})

	// A passkey disqualifies it too, and this is the case the deleted tier
	// used to admit. The account holds a factor that is STRONGER than a live
	// provider session, so a provider must not stand in for it -- not even
	// when this hub cannot currently run a ceremony, which is an
	// administrator's misconfiguration with its own remedy.
	t.Run("a passkey disqualifies the path", func(t *testing.T) {
		st := hubtestutil.OpenTestStore(t)
		user := newUser(t, st, "haspasskey", false)
		require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           id.Generate(),
			UserID:       user.ID,
			CredentialID: []byte("cred-1"),
			PublicKey:    []byte("pub"),
			FriendlyName: "laptop",
		}))

		may, err := accountElevatesOnlyThroughAProvider(ctx, st, user)
		require.NoError(t, err)
		assert.False(t, may, "a provider must not stand in for a passkey the account holds")
	})

	// A registered passkey disqualifies the path just as a password does, and
	// the rule reads the COUNT rather than whether this hub can currently run
	// a ceremony with it: an account whose passkey the hub cannot run still
	// HOLDS one.
	t.Run("a registered passkey disqualifies the provider path", func(t *testing.T) {
		st := hubtestutil.OpenTestStore(t)
		user := newUser(t, st, "haspasskey", false)
		require.NoError(t, st.PasskeyCredentials().Create(ctx, store.CreatePasskeyCredentialParams{
			ID:           id.Generate(),
			UserID:       user.ID,
			CredentialID: []byte("cred-1"),
			PublicKey:    []byte("pk-1"),
			FriendlyName: "Yubikey",
		}))

		may, err := accountElevatesOnlyThroughAProvider(ctx, st, user)
		require.NoError(t, err)
		assert.False(t, may, "an account that holds a passkey must present it")
	})
}
