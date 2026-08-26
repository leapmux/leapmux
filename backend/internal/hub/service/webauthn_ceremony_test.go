package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

const passkeyTestOrigin = "https://localhost"

type passkeyCeremony struct {
	rp            virtualwebauthn.RelyingParty
	authenticator virtualwebauthn.Authenticator
	credential    virtualwebauthn.Credential
}

func newPasskeyCeremony() *passkeyCeremony {
	return &passkeyCeremony{
		rp: virtualwebauthn.RelyingParty{
			Name:   "LeapMux",
			ID:     "localhost",
			Origin: passkeyTestOrigin,
		},
		authenticator: virtualwebauthn.NewAuthenticator(),
		credential:    virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
}

func (c *passkeyCeremony) registrationResponse(optionsJSON string) (string, error) {
	parsed, err := virtualwebauthn.ParseAttestationOptions(optionsJSON)
	if err != nil {
		return "", err
	}
	resp := virtualwebauthn.CreateAttestationResponse(c.rp, c.authenticator, c.credential, *parsed)
	c.authenticator.AddCredential(c.credential)
	return resp, nil
}

func (c *passkeyCeremony) assertionResponse(optionsJSON string) (string, error) {
	parsed, err := virtualwebauthn.ParseAssertionOptions(optionsJSON)
	if err != nil {
		return "", err
	}
	return virtualwebauthn.CreateAssertionResponse(c.rp, c.authenticator, c.credential, *parsed), nil
}

type passkeyAuthTestEnv struct {
	client leapmuxv1connect.AuthServiceClient
	store  store.Store
}

// setupPasskeyAuthTestServer builds a passkey-capable auth server whose
// database already holds the default admin, so a sign-up under it is an
// ordinary public one.
func setupPasskeyAuthTestServer(t *testing.T, seed authTestSeed, mailSender mail.Sender) passkeyAuthTestEnv {
	t.Helper()

	env := setupEmptyPasskeyAuthTestServer(t, seed, mailSender)
	hubtestutil.CreateTestAdmin(t, env.store)
	return env
}

// setupEmptyPasskeyAuthTestServer is the same server with an EMPTY database,
// which is the state the first-administrator flow runs in. The admin fixture
// is created afterwards by the wrapper above rather than here, so setup mode
// is reachable without a second copy of this construction.
func setupEmptyPasskeyAuthTestServer(t *testing.T, seed authTestSeed, mailSender mail.Sender) passkeyAuthTestEnv {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	key, err := keystore.GenerateKey()
	require.NoError(t, err)
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)

	set := servicetest.NewSettingsManager(t, st, ks)
	require.NoError(t, settings.KeyPublicURL.Set(context.Background(), set, passkeyTestOrigin))
	if seed != nil {
		seed(t, set)
	}

	mux := http.NewServeMux()
	interceptor, sc := hubtestutil.NewAuthInterceptor(t, auth.InterceptorOptions{Store: st})
	t.Cleanup(sc.Stop)
	opts := connect.WithInterceptors(interceptor)

	if mailSender == nil {
		mailSender = mail.NewStubSender()
	}
	authSvc := service.NewAuthService(service.AuthServiceDeps{
		Store:     st,
		Config:    testConfig(),
		Settings:  set,
		Lifecycle: auth.NewCredentialLifecycleEffects(sc, nil, nil),
		Mail:      mailSender,
		Renderer:  mail.Renderer{},
		Keystore:  ks,
	})
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc, opts)
	mux.Handle(path, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return passkeyAuthTestEnv{
		client: leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL),
		store:  st,
	}
}

// beginPasskeySignUpRequest builds the Begin request with the browser origin
// the ceremony needs. Separate from beginPasskeySignUp so a test that expects
// a REFUSAL can send the same request and read the error, rather than
// re-spelling the header a helper already owns.
func beginPasskeySignUpRequest(username, email string) *connect.Request[leapmuxv1.BeginPasskeySignUpRequest] {
	req := connect.NewRequest(&leapmuxv1.BeginPasskeySignUpRequest{
		Username:    username,
		DisplayName: username,
		Email:       email,
	})
	req.Header().Set("Origin", passkeyTestOrigin)
	return req
}

func beginPasskeySignUp(t *testing.T, client leapmuxv1connect.AuthServiceClient, username, email string) *leapmuxv1.BeginPasskeySignUpResponse {
	t.Helper()
	resp, err := client.BeginPasskeySignUp(context.Background(), beginPasskeySignUpRequest(username, email))
	require.NoError(t, err)
	return resp.Msg
}

// finishPasskeySignUp completes the registration ceremony. It returns the
// response and the error, like finishPasskeyLogin, because several callers
// assert on a refusal at this leg.
func finishPasskeySignUp(t *testing.T, client leapmuxv1connect.AuthServiceClient, sessionID, credentialJSON string) (*connect.Response[leapmuxv1.FinishPasskeySignUpResponse], error) {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.FinishPasskeySignUpRequest{
		SessionId:      sessionID,
		CredentialJson: credentialJSON,
	})
	req.Header().Set("Origin", passkeyTestOrigin)
	return client.FinishPasskeySignUp(context.Background(), req)
}

func beginPasskeyLogin(t *testing.T, client leapmuxv1connect.AuthServiceClient, username string) *leapmuxv1.BeginPasskeyLoginResponse {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.BeginPasskeyLoginRequest{Username: username})
	req.Header().Set("Origin", passkeyTestOrigin)
	resp, err := client.BeginPasskeyLogin(context.Background(), req)
	require.NoError(t, err)
	return resp.Msg
}

func finishPasskeyLogin(t *testing.T, client leapmuxv1connect.AuthServiceClient, sessionID, credentialJSON string) (*connect.Response[leapmuxv1.FinishPasskeyLoginResponse], error) {
	t.Helper()
	req := connect.NewRequest(&leapmuxv1.FinishPasskeyLoginRequest{
		SessionId:      sessionID,
		CredentialJson: credentialJSON,
	})
	req.Header().Set("Origin", passkeyTestOrigin)
	return client.FinishPasskeyLogin(context.Background(), req)
}
