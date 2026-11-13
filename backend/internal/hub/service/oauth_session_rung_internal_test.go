package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/hub/store/storetest"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

func TestOAuthRequireSessionKeepsTheBearerRungOff(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	owner := storetest.SeedUser(t, st, "browser-consent-owner")
	validator, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	tokenID, secret := id.Generate(), auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(t.Context(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(owner.ID), ClientID: oauthapp.ControlCLIClientID,
		InstallationName: "consenting-app", GrantedScopes: "workspace:read", SecretHash: validator.HashSecret(secret),
	}))
	bearer := auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	_, err = validator.ValidateBearer(t.Context(), bearer)
	require.NoError(t, err, "the app credential must otherwise authenticate")
	handler := &OAuthServerHandler{store: st, validator: validator, sessionHasher: validator.Hasher()}
	request := httptest.NewRequest(http.MethodPost, "/oauth/consent", nil)
	request.Header.Set("Authorization", "Bearer "+bearer)
	user, err := handler.requireSession(request)
	require.Nil(t, user, "an app cannot supply its own browser consent")
	require.ErrorIs(t, err, auth.ErrHTTPUnauthenticated)
}
