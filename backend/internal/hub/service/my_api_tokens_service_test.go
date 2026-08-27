package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// seedAPIToken writes one api_tokens row for the env's user and returns its
// id, with a recognisable secret so a leak test can look for it.
func seedAPIToken(t *testing.T, env *userTestEnv, clientName string, adminScope bool) string {
	t.Helper()
	tokenID := id.Generate()
	expires := time.Now().Add(time.Hour)
	refreshExpires := time.Now().Add(90 * 24 * time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(env.userID),
		ClientType:       "cli",
		ClientName:       clientName,
		SecretHash:       []byte("SECRET-HASH-MUST-NOT-LEAK"),
		RefreshHash:      []byte("REFRESH-HASH-MUST-NOT-LEAK"),
		ExpiresAt:        &expires,
		RefreshExpiresAt: &refreshExpires,
		AdminScope:       adminScope,
	}))
	return tokenID
}

func TestListMyAPITokens_ReportsTheAccountsOwnCredentials(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	laptop := seedAPIToken(t, env, "alice@laptop", false)
	ciBot := seedAPIToken(t, env, "ci-bot", true)

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTokens(), 2)

	byID := map[string]*leapmuxv1.MyAPIToken{}
	for _, tok := range resp.Msg.GetTokens() {
		byID[tok.GetId()] = tok
	}
	require.Contains(t, byID, laptop)
	require.Contains(t, byID, ciBot)
	assert.Equal(t, "alice@laptop", byID[laptop].GetClientName())
	assert.False(t, byID[laptop].GetAdminScope())
	assert.True(t, byID[ciBot].GetAdminScope(), "an audit of hub administration needs this field")
	assert.NotNil(t, byID[laptop].GetRefreshExpiresAt(), "the deadline that sends a device back to a browser")
	// The handler derives `current` from the CALLER's own credential; a cookie
	// is not a CLI credential, so it marks nothing.
	assert.False(t, byID[laptop].GetCurrent())
	assert.False(t, byID[ciBot].GetCurrent())
}

// TestListMyAPITokens_LeaksNoSecret is the pin the plan asks for: the mapper
// copies METADATA only. A secret or a hash reaching the wire here would be a
// credential given to anything that can read one response.
func TestListMyAPITokens_LeaksNoSecret(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	seedAPIToken(t, env, "alice@laptop", false)

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)

	// Serialize the WHOLE response and search it, so this covers a field added
	// to the message later without anybody remembering to extend it.
	raw, err := protojson.Marshal(resp.Msg)
	require.NoError(t, err)
	body := string(raw)
	for _, forbidden := range []string{"SECRET-HASH", "REFRESH-HASH", "secretHash", "refreshHash", "secret_hash", "refresh_hash"} {
		assert.NotContains(t, body, forbidden, "no secret material may leave the store layer")
	}

	// And the message itself declares no such field, so the search above
	// cannot pass merely because somebody renamed the value.
	var shape map[string]any
	require.NoError(t, json.Unmarshal(raw, &shape))
	for _, tok := range shape["tokens"].([]any) {
		for key := range tok.(map[string]any) {
			assert.False(t, strings.Contains(strings.ToLower(key), "secret") || strings.Contains(strings.ToLower(key), "hash"),
				"MyAPIToken must declare no secret-bearing field; found %q", key)
		}
	}
}

// TestListMyAPITokens_IsScopedToTheCaller pins tenancy: the listing is
// per-user, which is also what makes RevokeMyAPIToken's uniform NotFound
// safe (a caller has no legitimate way to learn another user's token id).
func TestListMyAPITokens_IsScopedToTheCaller(t *testing.T) {
	t.Parallel()

	env := setupUserTest(t)
	mine := seedAPIToken(t, env, "mine", false)

	otherID := id.Generate()
	require.NoError(t, env.store.Users().Create(context.Background(), store.CreateUserParams{
		ID: otherID, Username: "other", PasswordHash: "hash", DisplayName: "Other", PasswordSet: true,
	}))
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: id.Generate(), UserID: userid.MustNew(otherID), ClientType: "cli", ClientName: "theirs",
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	resp, err := env.client.ListMyAPITokens(context.Background(), authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	require.Len(t, resp.Msg.GetTokens(), 1)
	assert.Equal(t, mine, resp.Msg.GetTokens()[0].GetId())
}

// TestRevokeMyAPIToken_NeedsNoElevation is deliberate, and stated so it
// cannot be "tightened" by accident: revoking only REDUCES access, and
// demanding a fresh factor from somebody who believes a credential is stolen
// is the wrong failure mode.
func TestRevokeMyAPIToken_NeedsNoElevation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	tokenID := seedAPIToken(t, env, "alice@laptop", false)

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err, "revocation must work on a session that proved nothing")

	row, err := env.store.APITokens().GetByID(ctx, tokenID)
	require.NoError(t, err)
	assert.NotNil(t, row.RevokedAt)

	// It leaves the listing.
	resp, err := env.client.ListMyAPITokens(ctx, authedReq(&leapmuxv1.ListMyAPITokensRequest{}, env.token))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetTokens())
}

// TestRevokeMyAPIToken_RefusesAnotherUsersToken pins that the owner check
// lives in the statement, and that the refusal does not confirm the id
// exists: a missing token and somebody else's answer identically.
func TestRevokeMyAPIToken_RefusesAnotherUsersToken(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)

	otherID := id.Generate()
	require.NoError(t, env.store.Users().Create(ctx, store.CreateUserParams{
		ID: otherID, Username: "victim", PasswordHash: "hash", DisplayName: "Victim", PasswordSet: true,
	}))
	victimToken := id.Generate()
	expires := time.Now().Add(time.Hour)
	require.NoError(t, env.store.APITokens().Create(ctx, store.CreateAPITokenParams{
		ID: victimToken, UserID: userid.MustNew(otherID), ClientType: "cli", ClientName: "victim-laptop",
		SecretHash: []byte("x"), ExpiresAt: &expires,
	}))

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: victimToken}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))

	_, missingErr := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: "no-such-token"}, env.token))
	require.Error(t, missingErr)
	assert.Equal(t, connect.CodeOf(err), connect.CodeOf(missingErr),
		"a token that belongs to somebody else must be indistinguishable from one that does not exist")

	row, err := env.store.APITokens().GetByID(ctx, victimToken)
	require.NoError(t, err)
	assert.Nil(t, row.RevokedAt, "the victim's credential must survive")
}

// TestRevokeMyAPIToken_TwiceIsNotFound pins that the handler does not
// silently report an already-revoked row as revoked again: the listing shows
// live rows only, so a second attempt is against something the caller can no
// longer see.
func TestRevokeMyAPIToken_TwiceIsNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	env := setupUserTest(t)
	tokenID := seedAPIToken(t, env, "alice@laptop", false)

	_, err := env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.NoError(t, err)
	_, err = env.client.RevokeMyAPIToken(ctx, authedReq(&leapmuxv1.RevokeMyAPITokenRequest{Id: tokenID}, env.token))
	require.Error(t, err)
	assert.Equal(t, connect.CodeNotFound, connect.CodeOf(err))
}
