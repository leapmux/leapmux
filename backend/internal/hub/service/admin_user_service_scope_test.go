package service_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// TestIssueAPIToken_CannotMintACredentialWiderThanItself is the second bound
// this verb needs, and the one the scope rung alone cannot give.
//
// The rung admits any credential granted admin:users. Without the issuer
// ceiling, such a credential could mint ITSELF one carrying tunnel:open --
// arbitrary network egress from inside the account's private network, reached
// through a permission that says "administer accounts". That is a total bypass
// of the scope model rather than a wide grant.
//
// It REFUSES rather than silently narrows, for the reason every other scope
// refusal does: an operator told "issued" and then refused on the first call
// has nothing to point at.
func TestIssueAPIToken_CannotMintACredentialWiderThanItself(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupAdminUserTest(t)
	owner := userid.MustNew(env.adminID)

	mint := func(grant string) string {
		t.Helper()
		tokenID := id.Generate()
		secret := auth.MintAccessSecret()
		require.NoError(t, env.st.APITokens().Create(ctx, store.CreateAPITokenParams{
			ID:               tokenID,
			UserID:           owner,
			ClientID:         oauthapp.ControlCLIClientID,
			InstallationName: "issuer",
			GrantedScopes:    grant,
			SecretHash:       env.validator.HashSecret(secret),
		}))
		hubtestutil.ElevateAPIToken(t, env.st, tokenID, env.adminID)
		return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
	}
	issue := func(bearer string, scopes []string) error {
		req := connect.NewRequest(&leapmuxv1.IssueAPITokenRequest{
			Username: "admin", InstallationName: "ci-bot", Scopes: scopes,
		})
		req.Header().Set("Authorization", "Bearer "+bearer)
		_, err := env.client.IssueAPIToken(ctx, req)
		return err
	}

	narrow := mint("admin:read admin:users workspace:read")

	err := issue(narrow, []string{"tunnel:open"})
	require.Error(t, err, "a credential must not mint one carrying a permission it does not hold")
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))

	// The DEFAULT ask is "everything except admin:*", which a narrow issuer
	// also does not hold -- so an omitted --scope is refused too rather than
	// quietly issuing the whole vocabulary.
	assert.Error(t, issue(narrow, nil))

	// What it DOES hold, it may issue.
	require.NoError(t, issue(narrow, []string{"workspace:read"}))

	// A SESSION is unscoped, so it may issue anything its account can do.
	// That is the composition rule: a scope subtracts from the user's own
	// authority and never adds to it, so an unscoped credential is bounded by
	// the account alone.
	wide := mint(authscope.NonAdminGrant().String() + " admin:read admin:users")
	require.NoError(t, issue(wide, []string{"tunnel:open", "git:write"}))
}
