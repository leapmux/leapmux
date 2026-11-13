package service_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/webauthn"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The old grant enters through Sessions.Elevate. The corrected grant enters
// through the user-auth transaction. Both trigger the same real mutation after
// factor verification and before the grant can take its write lock.
type proofMutationStore struct {
	store.Store
	beforeGrant func() error
	once        sync.Once
	mutationErr error
}

func (s *proofMutationStore) changeFactor() error {
	if s.beforeGrant == nil {
		return nil
	}
	s.once.Do(func() { s.mutationErr = s.beforeGrant() })
	return s.mutationErr
}
func (s *proofMutationStore) RunInUserAuthTransaction(ctx context.Context, uid userid.UserID, fn func(store.Store) error) error {
	if err := s.changeFactor(); err != nil {
		return err
	}
	return s.Store.RunInUserAuthTransaction(ctx, uid, fn)
}
func (s *proofMutationStore) Sessions() store.SessionStore {
	return proofMutationSessions{s.Store.Sessions(), s}
}

type proofMutationSessions struct {
	store.SessionStore
	parent *proofMutationStore
}

func (s proofMutationSessions) Elevate(ctx context.Context, p store.ElevateSessionParams, now time.Time) (int64, error) {
	if err := s.parent.changeFactor(); err != nil {
		return 0, err
	}
	return s.SessionStore.Elevate(ctx, p, now)
}

type proofTestUser struct {
	base                     store.Store
	wrapped                  *proofMutationStore
	service                  *service.UserService
	ks                       *keystore.Keystore
	info                     *auth.UserInfo
	token, userID, sessionID string
}

func setupProofTestUser(t *testing.T) *proofTestUser {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	uid := id.Generate()
	hash, err := password.Hash("testpass")
	require.NoError(t, err)
	require.NoError(t, st.Users().Create(t.Context(), store.CreateUserParams{ID: uid, Username: "proofuser", PasswordHash: hash, FirstCredentialExempt: true}))
	token, _, _, err := auth.Login(t.Context(), st, hubtestutil.TestSecretHasher(), "proofuser", "testpass", auth.DefaultSessionDuration)
	require.NoError(t, err)
	info, err := auth.ValidateToken(t.Context(), st, hubtestutil.TestSecretHasher(), token)
	require.NoError(t, err)
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	set := servicetest.NewSettingsManager(t, st, ks)
	require.NoError(t, settings.KeyPublicURL.Set(t.Context(), set, passkeyTestOrigin))
	wrapped := &proofMutationStore{Store: st}
	svc := service.NewUserService(wrapped, testConfig(), set, auth.NewCredentialLifecycleEffects(nil, nil, nil), mail.NewStubSender(), mail.Renderer{}, ks)
	return &proofTestUser{base: st, wrapped: wrapped, service: svc, ks: ks, info: info, token: token, userID: uid, sessionID: hubtestutil.SessionIDFromCookie(t, token)}
}
func assertProofRefusedWithoutEndingSession(t *testing.T, st store.Store, token, sessionID string) {
	t.Helper()
	_, err := auth.ValidateToken(t.Context(), st, hubtestutil.TestSecretHasher(), token)
	require.NoError(t, err, "factor changes that preserve the acting session must not be mistaken for logout")
	row, err := st.Sessions().GetByID(t.Context(), sessionID, time.Now().UTC())
	require.NoError(t, err)
	assert.Nil(t, row.ElevationProvenAt)
	assert.Nil(t, row.ElevationExpiresAt)
}
func TestPasswordElevationRefusesAChangedProofForASurvivingSession(t *testing.T) {
	for _, mutation := range []string{"password and generation", "generation only", "unchanged"} {
		t.Run(mutation, func(t *testing.T) {
			env := setupProofTestUser(t)
			replacement, err := password.Hash("replacement-password")
			require.NoError(t, err)
			if mutation != "unchanged" {
				env.wrapped.beforeGrant = func() error {
					return env.base.RunInUserAuthTransaction(t.Context(), userid.MustNew(env.userID), func(tx store.Store) error {
						if mutation == "password and generation" {
							if err := tx.Users().UpdatePassword(t.Context(), store.UpdateUserPasswordParams{ID: env.userID, PasswordHash: replacement}); err != nil {
								return err
							}
						}
						if _, err := tx.Users().RevokeUserTokens(t.Context(), userid.MustNew(env.userID)); err != nil {
							return err
						}
						n, err := tx.Sessions().RefreshAuthGeneration(t.Context(), store.RefreshSessionAuthGenerationParams{SessionID: env.sessionID, UserID: userid.MustNew(env.userID)})
						require.EqualValues(t, 1, n)
						return err
					})
				}
			}
			result, err := env.service.ElevateSession(auth.WithUser(t.Context(), env.info), &leapmuxv1.ElevateSessionRequest{CurrentPassword: "testpass"})
			if mutation == "unchanged" {
				require.NoError(t, err)
				require.NotNil(t, result.GetElevationExpiresAt())
				return
			}
			require.Error(t, err, "a verified old factor snapshot must not grant a new window")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assertProofRefusedWithoutEndingSession(t, env.base, env.token, env.sessionID)
		})
	}
}

func prepareProofPasskey(t *testing.T, st store.Store, ks *keystore.Keystore, userID string) (*passkeyCeremony, webauthn.FinishedSignUpCredential, *webauthn.Service) {
	t.Helper()
	wa, err := webauthn.NewService(webauthn.RPConfig{RPID: "localhost", RPDisplayName: "LeapMux", RPOrigins: []string{passkeyTestOrigin}}, st, ks)
	require.NoError(t, err)
	ceremony := newPasskeyCeremony()
	sid, options, err := wa.BeginRegistration(t.Context(), userID, passkeyTestOrigin)
	require.NoError(t, err)
	attestation, err := ceremony.registrationResponse(options)
	require.NoError(t, err)
	credential, err := wa.VerifyRegistration(t.Context(), userID, sid, attestation)
	require.NoError(t, err)
	return ceremony, credential, wa
}
func TestPasskeyElevationRefusesAReplacedVerifiedCredential(t *testing.T) {
	for _, mutation := range []string{"unchanged", "verified credential removed while another remains", "verified credential registered again with the same authenticator id"} {
		t.Run(mutation, func(t *testing.T) {
			changed := mutation != "unchanged"
			env := setupProofTestUser(t)
			ceremony, credential, wa := prepareProofPasskey(t, env.base, env.ks, env.userID)
			verified, err := wa.StoreCredential(t.Context(), env.base, id.Generate(), env.userID, credential, "Verified key")
			require.NoError(t, err)
			_, other, otherWA := prepareProofPasskey(t, env.base, env.ks, env.userID)
			_, err = otherWA.StoreCredential(t.Context(), env.base, id.Generate(), env.userID, other, "Other key")
			require.NoError(t, err)
			var begin *leapmuxv1.BeginPasskeyElevationResponse
			_, err = callUnderServerInfo(auth.WithUser(t.Context(), env.info), func(ctx context.Context) error {
				var callErr error
				begin, callErr = env.service.BeginPasskeyElevation(ctx, &leapmuxv1.BeginPasskeyElevationRequest{})
				return callErr
			})
			require.NoError(t, err)
			assertion, err := ceremony.assertionResponse(begin.GetChallenge().GetOptionsJson())
			require.NoError(t, err)
			if changed {
				env.wrapped.beforeGrant = func() error {
					return env.base.RunInUserAuthTransaction(t.Context(), userid.MustNew(env.userID), func(tx store.Store) error {
						if err := tx.PasskeyCredentials().Delete(t.Context(), verified.ID, env.userID); err != nil {
							return err
						}
						if mutation == "verified credential registered again with the same authenticator id" {
							_, err := wa.StoreCredential(t.Context(), tx, id.Generate(), env.userID, credential, "Registered again")
							return err
						}
						return nil
					})
				}
			}
			var finish *leapmuxv1.FinishPasskeyElevationResponse
			_, err = callUnderServerInfo(auth.WithUser(t.Context(), env.info), func(ctx context.Context) error {
				var callErr error
				finish, callErr = env.service.FinishPasskeyElevation(ctx, &leapmuxv1.FinishPasskeyElevationRequest{Credential: &leapmuxv1.WebAuthnCredential{SessionId: begin.GetChallenge().GetSessionId(), CredentialJson: assertion}})
				return callErr
			})
			if !changed {
				require.NoError(t, err)
				require.NotNil(t, finish.GetElevationExpiresAt())
				return
			}
			require.Error(t, err, "a different remaining key cannot replace the credential that verified")
			assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
			assertProofRefusedWithoutEndingSession(t, env.base, env.token, env.sessionID)
		})
	}
}
func TestOAuthElevationRechecksTheProvedFactorUnderItsGrantLock(t *testing.T) {
	for _, mutation := range []string{"first passkey added", "proved link removed", "unchanged"} {
		t.Run(mutation, func(t *testing.T) {
			var wrapped *proofMutationStore
			server, st, ks, _ := setupOAuthTestServerOver(t, ":4327", func(st store.Store) store.Store { wrapped = &proofMutationStore{Store: st}; return wrapped })
			const subject = "current-proof-subject"
			provider := createTestOIDCProviderWithStubClaims(t, st, ks, "current-proof@example.com", subject)
			cookie, userID, sessionID := reauthSession(t, st, provider, subject)
			state, nonce, _ := beginReauth(t, server, provider, cookie, "/after-proof")
			switch mutation {
			case "first passkey added":
				_, credential, wa := prepareProofPasskey(t, st, ks, userID)
				wrapped.beforeGrant = func() error {
					return st.RunInUserAuthTransaction(t.Context(), userid.MustNew(userID), func(tx store.Store) error {
						_, err := wa.StoreCredential(t.Context(), tx, id.Generate(), userID, credential, "First key")
						return err
					})
				}
			case "proved link removed":
				other := createTestProvider(t, st, ks)
				require.NoError(t, st.OAuthUserLinks().Create(t.Context(), store.CreateOAuthUserLinkParams{UserID: userid.MustNew(userID), ProviderID: other, ProviderSubject: "remaining-provider-subject"}))
				wrapped.beforeGrant = func() error {
					return st.RunInUserAuthTransaction(t.Context(), userid.MustNew(userID), func(tx store.Store) error {
						return tx.OAuthUserLinks().Delete(t.Context(), store.DeleteOAuthUserLinkParams{UserID: userid.MustNew(userID), ProviderID: provider})
					})
				}
			}
			response := completeCallback(t, server, provider, state, nonce)
			if mutation == "unchanged" {
				require.Equal(t, http.StatusFound, response.StatusCode, readBody(t, response))
				assert.Equal(t, "/after-proof", response.Header.Get("Location"))
				return
			}
			require.Equal(t, http.StatusForbidden, response.StatusCode, readBody(t, response))
			assertProofRefusedWithoutEndingSession(t, st, cookie.Value, sessionID)
		})
	}
}
