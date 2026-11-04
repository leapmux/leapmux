package service_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// passkeyRegistrationOrigin is the origin that setupUserTest's public URL
// derives, so a ceremony against that env passes the hub's origin check.
const passkeyRegistrationOrigin = "http://localhost:4327"

// registerPasskey runs one real registration ceremony against the env's hub.
func registerPasskey(t *testing.T, env *userTestEnv) (*leapmuxv1.FinishPasskeyRegistrationResponse, error) {
	t.Helper()
	callCtx := func() context.Context {
		ctx, info := clientCallInfo(context.Background())
		info.RequestHeader().Set("Cookie", auth.CookieName+"="+env.token)
		info.RequestHeader().Set("Origin", passkeyRegistrationOrigin)
		return ctx
	}
	begin, err := env.client.BeginPasskeyRegistration(callCtx(), &leapmuxv1.BeginPasskeyRegistrationRequest{})
	require.NoError(t, err)

	ceremony := newPasskeyCeremony()
	ceremony.rp.Origin = passkeyRegistrationOrigin
	credentialJSON, err := ceremony.registrationResponse(begin.GetChallenge().GetOptionsJson())
	require.NoError(t, err)

	return env.client.FinishPasskeyRegistration(callCtx(), &leapmuxv1.FinishPasskeyRegistrationRequest{
		Credential: &leapmuxv1.WebAuthnCredential{
			SessionId:      begin.GetChallenge().GetSessionId(),
			CredentialJson: credentialJSON,
		},
		FriendlyName: "Laptop",
	})
}

// seedTwoLinkedProviders links two enabled providers to the env's account.
func seedTwoLinkedProviders(t *testing.T, env *userTestEnv) {
	t.Helper()
	ctx := context.Background()
	for providerID, providerType := range map[string]leapmuxv1.IdentityProviderType{
		"github-1": leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB,
		"google-1": leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC,
	} {
		require.NoError(t, env.store.OAuthProviders().Create(ctx, store.CreateOAuthProviderParams{
			ID: providerID, ProviderType: providerType, Name: providerID,
			ClientID: "client", ClientSecret: []byte("secret"), Scopes: "openid", Enabled: true,
		}))
		require.NoError(t, env.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
			UserID: userid.MustNew(env.userID), ProviderID: providerID, ProviderSubject: "sub-" + providerID,
		}))
	}
}

// accountMutation is one RPC that changes the signed-in account and answers
// with the stored account.
type accountMutation struct {
	name string
	// setup builds the env. wrap wraps the store that the UserService alone
	// reads.
	setup func(t *testing.T, wrap func(store.Store) store.Store) *userTestEnv
	// run calls the RPC and returns the account that its response carries.
	run func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error)
	// assertChange states the fact that only a read after the commit reports.
	assertChange func(t *testing.T, user *leapmuxv1.User)
}

func elevatedPasswordUser(t *testing.T, wrap func(store.Store) store.Store) *userTestEnv {
	t.Helper()
	return setupUserTestOver(t, wrap).elevated(t)
}

func accountMutations() []accountMutation {
	return []accountMutation{
		{
			name:  "RequestEmailChange",
			setup: elevatedPasswordUser,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				resp, err := env.client.RequestEmailChange(authedReq(context.Background(),
					&leapmuxv1.RequestEmailChangeRequest{NewEmail: "moved@example.com"}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.Equal(t, "moved@example.com", user.GetEmail())
			},
		},
		{
			name:  "ChangePassword",
			setup: setupOAuthUserTestOver,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				resp, err := env.client.ChangePassword(authedReq(context.Background(),
					&leapmuxv1.ChangePasswordRequest{NewPassword: "newpass123"}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.True(t, user.GetPasswordSet(), "the first password shows on the reply")
			},
		},
		{
			name:  "UnlinkOAuthProvider",
			setup: elevatedPasswordUser,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				seedTwoLinkedProviders(t, env)
				resp, err := env.client.UnlinkOAuthProvider(authedReq(context.Background(),
					&leapmuxv1.UnlinkOAuthProviderRequest{ProviderId: "github-1"}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				require.Len(t, user.GetOauthProviders(), 1, "the reply no longer lists the removed link")
				assert.Equal(t, "google-1", user.GetOauthProviders()[0].GetId())
			},
		},
		{
			name:  "FinishPasskeyRegistration",
			setup: elevatedPasswordUser,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				resp, err := registerPasskey(t, env)
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.EqualValues(t, 1, user.GetPasskeyCount(), "the reply counts the new passkey")
			},
		},
		{
			name:  "DeletePasskey",
			setup: elevatedPasswordUser,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				seedPasskeyCredential(t, env.store, env.userID, "Keep")
				doomed := seedPasskeyCredential(t, env.store, env.userID, "Remove")
				resp, err := env.client.DeletePasskey(authedReq(context.Background(),
					&leapmuxv1.DeletePasskeyRequest{Id: doomed}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.EqualValues(t, 1, user.GetPasskeyCount(), "the reply no longer counts the removed passkey")
			},
		},
		{
			name: "DeletePasskey of the last passkey on a passwordless account",
			setup: func(t *testing.T, wrap func(store.Store) store.Store) *userTestEnv {
				t.Helper()
				return setupOAuthUserTestOver(t, wrap)
			},
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				only := seedPasskeyCredential(t, env.store, env.userID, "Only")
				env.elevate(t)
				resp, err := env.client.DeletePasskey(authedReq(context.Background(),
					&leapmuxv1.DeletePasskeyRequest{Id: only, NewPassword: "newpass123"}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.Zero(t, user.GetPasskeyCount())
				assert.True(t, user.GetPasswordSet(), "the replacement password shows on the reply")
			},
		},
		{
			name:  "DeactivatePasskeyAuth",
			setup: elevatedPasswordUser,
			run: func(t *testing.T, env *userTestEnv) (*leapmuxv1.User, error) {
				t.Helper()
				seedPasskeyCredential(t, env.store, env.userID, "One")
				seedPasskeyCredential(t, env.store, env.userID, "Two")
				resp, err := env.client.DeactivatePasskeyAuth(authedReq(context.Background(),
					&leapmuxv1.DeactivatePasskeyAuthRequest{}, env.token))
				return resp.GetUser(), err
			},
			assertChange: func(t *testing.T, user *leapmuxv1.User) {
				t.Helper()
				assert.Zero(t, user.GetPasskeyCount(), "the reply counts no passkey")
			},
		},
	}
}

// Each account mutation answers with the account as the hub stores it after the
// commit, as UpdateProfile and VerifyEmail do. A client adopts that account, so
// no second read can fail and leave the old facts on screen.
func TestAccountMutationsReturnTheStoredAccount(t *testing.T) {
	t.Parallel()

	for _, mutation := range accountMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			env := mutation.setup(t, nil)

			user, err := mutation.run(t, env)
			require.NoError(t, err)
			require.NotNil(t, user, "the reply carries the account")
			assert.Equal(t, env.userID, user.GetId())
			mutation.assertChange(t, user)
		})
	}
}

// failedReadBackStore fails each read of an account by id once a user-auth
// transaction committed. That read is the reply's projection: the mutation
// itself ran before it, and the session check reads the store unwrapped.
type failedReadBackStore struct {
	store.Store
	committed *atomic.Bool
}

func (s failedReadBackStore) RunInUserAuthTransaction(ctx context.Context, userID userid.UserID, fn func(tx store.Store) error) error {
	if err := s.Store.RunInUserAuthTransaction(ctx, userID, fn); err != nil {
		return err
	}
	s.committed.Store(true)
	return nil
}

func (s failedReadBackStore) Users() store.UserStore {
	return failedReadBackUsers{UserStore: s.Store.Users(), committed: s.committed}
}

type failedReadBackUsers struct {
	store.UserStore
	committed *atomic.Bool
}

func (u failedReadBackUsers) GetByID(ctx context.Context, id string) (*store.User, error) {
	if u.committed.Load() {
		return nil, errors.New("the account read failed")
	}
	return u.UserStore.GetByID(ctx, id)
}

// The change already committed when the reply reads the account, so a failed
// read must not fail the RPC. The reply omits the account, which tells the
// client to read GetCurrentUser.
func TestAccountMutationsOmitTheAccountWhenTheReadBackFails(t *testing.T) {
	t.Parallel()

	for _, mutation := range accountMutations() {
		t.Run(mutation.name, func(t *testing.T) {
			t.Parallel()
			var committed atomic.Bool
			env := mutation.setup(t, func(st store.Store) store.Store {
				return failedReadBackStore{Store: st, committed: &committed}
			})

			user, err := mutation.run(t, env)
			require.NoError(t, err, "the change committed, so the RPC reports success")
			require.True(t, committed.Load(), "precondition: the mutation committed before the read failed")
			assert.Nil(t, user, "an unread account is absent, never a stale one")

			// The committed change is the store's, whatever the reply carried.
			stored, err := env.store.Users().GetByID(context.Background(), env.userID)
			require.NoError(t, err)
			assert.Equal(t, env.userID, stored.ID)
		})
	}
}
