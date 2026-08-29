package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestResolveApp_ZeroCallerReachesNoPrivateApp pins the whole visibility rule,
// and the zero-caller case it turns on.
//
// A PRIVATE app is visible and authorizable only to its owner. A HUB-WIDE one
// is visible to everybody, including an anonymous leg -- the device-code first
// hop carries no session at all, and that is the honest consequence of a flow
// whose first request has no identity.
//
// The comparison is a GRANT, so false means deny: a zero caller and a blank
// stored owner must not read as the same person. A hub-wide app stores SQL NULL
// (loading as ""), which is exactly the value a naive comparison would match.
func TestResolveApp_ZeroCallerReachesNoPrivateApp(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	owner, err := st.Users().GetByUsername(ctx, "admin")
	require.NoError(t, err)

	h := &OAuthServerHandler{store: st}

	privateID := id.Generate()
	_, createErr := st.OAuthClients().Create(ctx, store.CreateOAuthClientParams{
		ClientID:           privateID,
		OwnerUserID:        owner.ID,
		CreatedBy:          owner.ID,
		ClientName:         "Alice's private app",
		RedirectURIs:       "https://app.example.com/cb",
		Scopes:             "workspace:read",
		GrantTypes:         "authorization_code refresh_token",
		RegistrationSource: store.OAuthClientSourceUser,
	})
	require.NoError(t, createErr)

	// A hub-wide app: the built-in control CLI, seeded by the migration.
	hubWide, err := h.resolveApp(ctx, oauthapp.ControlCLIClientID, nil)
	require.NoError(t, err, "a hub-wide app is reachable with no caller at all")
	assert.True(t, hubWide.IsHubWide())

	_, err = h.resolveApp(ctx, privateID, nil)
	assert.ErrorIs(t, err, errAppUnavailable, "no caller resolves no private app")

	_, err = h.resolveApp(ctx, privateID, &auth.UserInfo{})
	assert.ErrorIs(t, err, errAppUnavailable,
		"a ZERO caller id must not match a private app, whatever the stored owner is")

	stranger := &auth.UserInfo{ID: userid.MustNew("u-stranger")}
	_, err = h.resolveApp(ctx, privateID, stranger)
	assert.ErrorIs(t, err, errAppUnavailable, "another user's private app is not visible")

	got, err := h.resolveApp(ctx, privateID, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	require.NoError(t, err)
	assert.Equal(t, privateID, got.ClientID)

	// An UNKNOWN id and a REVOKED one answer the same error as an invisible
	// one. Distinguishing them would let any anonymous caller enumerate the
	// private registrations on the hub.
	_, err = h.resolveApp(ctx, "no-such-app", nil)
	assert.ErrorIs(t, err, errAppUnavailable)
	_, err = h.resolveApp(ctx, "", nil)
	assert.ErrorIs(t, err, errAppUnavailable)

	_, err = st.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
		ClientID: privateID, CallerUserID: userid.MustNew(owner.ID),
	})
	require.NoError(t, err)
	_, err = h.resolveApp(ctx, privateID, &auth.UserInfo{ID: userid.MustNew(owner.ID)})
	assert.ErrorIs(t, err, errAppUnavailable, "a revoked app is unavailable to its own owner")
}

// TestOAuthClientStore_WritesRefuseAnUnmintedCaller pins the store-level half.
//
// The caller travels INTO the statement, so a zero id must be refused before it
// binds: `owner_user_id = ”` would match every blank-owner row rather than
// none, and depending on a hub-wide app storing NULL instead of "" is exactly
// the reasoning userid.OwnerFilter exists to remove.
func TestOAuthClientStore_WritesRefuseAnUnmintedCaller(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	st := hubtestutil.OpenTestStore(t)

	var zero userid.UserID
	_, err := st.OAuthClients().Update(ctx, store.UpdateOAuthClientParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: zero, CallerIsAdmin: true,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	_, err = st.OAuthClients().SetElevationAllowed(ctx, store.SetOAuthClientElevationAllowedParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: zero, CallerIsAdmin: true,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	_, err = st.OAuthClients().SetIcon(ctx, store.SetOAuthClientIconParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: zero, CallerIsAdmin: true,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	_, err = st.OAuthClients().Revoke(ctx, store.OAuthClientOwnershipParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: zero, CallerIsAdmin: true,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)

	_, err = st.OAuthClients().Delete(ctx, store.OAuthClientOwnershipParams{
		ClientID: oauthapp.ControlCLIClientID, CallerUserID: zero, CallerIsAdmin: true,
	})
	assert.ErrorIs(t, err, store.ErrInvalidArgument)
}
