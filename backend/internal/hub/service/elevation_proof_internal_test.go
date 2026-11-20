package service

import (
	"context"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionElevationRequiresAnExplicitFactor(t *testing.T) {
	_, err := grantSessionElevation(t.Context(), nil, nil, "session", userid.MustNew("owner"), time.Now(), nil)
	require.ErrorContains(t, err, "requires a verified factor")
	current := &store.User{ID: "owner", FirstCredentialExempt: true, PasswordHash: "verified hash"}
	for _, proof := range []sessionElevationProof{
		passwordElevationProof{},
		passwordElevationProof{passwordHash: "another hash"},
		passwordElevationProof{passwordHash: current.PasswordHash, authGeneration: 1},
		providerElevationProof{},
		passkeyElevationProof{},
		passkeyElevationProof{identity: webauthn.VerifiedCredentialIdentity{RowID: "row"}},
	} {
		require.ErrorIs(t, proof.validate(t.Context(), nil, current), errElevationProofChanged)
	}
}

func TestGrantSessionElevationRefusesAnAppWhosePolicyIsDisabled(t *testing.T) {
	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	userID := hubtestutil.CreateTestUser(t, st, "desktop-policy", "testpass")
	user, err := st.Users().GetByID(ctx, userID)
	require.NoError(t, err)
	clientID := id.Generate()
	_, err = st.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
		ClientID:           clientID,
		OwnerUserID:        userID,
		ClientName:         "Desktop policy test",
		RedirectURIs:       "http://127.0.0.1/callback",
		GrantTypes:         "authorization_code",
		RegistrationSource: leapmuxv1.AppRegistrationSource_APP_REGISTRATION_SOURCE_USER,
	})
	require.NoError(t, err)
	session, err := auth.CreateSession(ctx, st, hubtestutil.TestSecretHasher(), userid.MustNew(userID),
		auth.SessionCreationOptions{
			Lifetime:       auth.DefaultSessionDuration,
			Proof:          auth.SignInProvedNoFactor,
			IssuedToClient: clientID,
		})
	require.NoError(t, err)

	_, err = grantSessionElevation(ctx, st, auth.NewCredentialLifecycleEffects(nil, nil, nil),
		session.ID, userid.MustNew(userID), time.Now().UTC(), passwordElevationProof{
			passwordHash:   user.PasswordHash,
			authGeneration: user.AuthGeneration,
		})
	require.ErrorIs(t, err, errElevationAppNotAllowed)
	stored, loadErr := st.Sessions().GetByID(ctx, session.ID, time.Now().UTC())
	require.NoError(t, loadErr)
	assert.Nil(t, stored.ElevationExpiresAt)
}
