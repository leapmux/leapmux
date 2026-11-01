package service_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
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
	client authTestClient
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
// which is the state the first-administrator flow runs in. The wrapper above
// creates the admin fixture afterwards rather than here, so setup mode is
// reachable without a second copy of this construction.
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
	connectServer := connect.NewServer(interceptor)
	mountAuthAndSystem(connectServer, authSvc)
	connecthttp.Mount(mux, connectServer, connecthttp.WithReadMaxBytes(0))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return passkeyAuthTestEnv{
		client: newAuthTestClient(server.Client(), server.URL),
		store:  st,
	}
}

// passkeyCtx returns a context carrying the browser origin the passkey
// ceremonies need. Connect v2 carries request headers on the call context,
// not on the message.
func passkeyCtx(ctx context.Context) context.Context {
	ctx, info := connect.NewClientContext(ctx)
	info.RequestHeader().Set("Origin", passkeyTestOrigin)
	return ctx
}

// beginPasskeySignUpRequest builds the Begin request. Separate from
// beginPasskeySignUp so a test that expects a REFUSAL can send the same
// request and read the error, rather than re-spelling the request a helper
// already owns. The caller sends it through passkeyCtx.
func beginPasskeySignUpRequest(username, email string) *leapmuxv1.BeginPasskeySignUpRequest {
	return &leapmuxv1.BeginPasskeySignUpRequest{
		Username:    username,
		DisplayName: username,
		Email:       email,
	}
}

func beginPasskeySignUp(t *testing.T, client leapmuxv1connect.AuthServiceClient, username, email string) *leapmuxv1.BeginPasskeySignUpResponse {
	t.Helper()
	resp, err := client.BeginPasskeySignUp(passkeyCtx(context.Background()), beginPasskeySignUpRequest(username, email))
	require.NoError(t, err)
	return resp
}

// finishPasskeySignUp completes the registration ceremony. It returns the
// response and the error, like finishPasskeyLogin, because several callers
// assert on a refusal at this leg.
func finishPasskeySignUp(t *testing.T, client leapmuxv1connect.AuthServiceClient, sessionID, credentialJSON string) (*leapmuxv1.FinishPasskeySignUpResponse, error) {
	t.Helper()
	return client.FinishPasskeySignUp(passkeyCtx(context.Background()), &leapmuxv1.FinishPasskeySignUpRequest{
		Credential: &leapmuxv1.WebAuthnCredential{SessionId: sessionID, CredentialJson: credentialJSON},
	})
}

func beginPasskeyLogin(t *testing.T, client leapmuxv1connect.AuthServiceClient, username string) *leapmuxv1.BeginPasskeyLoginResponse {
	t.Helper()
	resp, err := client.BeginPasskeyLogin(passkeyCtx(context.Background()), &leapmuxv1.BeginPasskeyLoginRequest{Username: username})
	require.NoError(t, err)
	return resp
}

func finishPasskeyLogin(t *testing.T, client leapmuxv1connect.AuthServiceClient, sessionID, credentialJSON string) (*leapmuxv1.FinishPasskeyLoginResponse, error) {
	t.Helper()
	return client.FinishPasskeyLogin(passkeyCtx(context.Background()), &leapmuxv1.FinishPasskeyLoginRequest{
		Credential: &leapmuxv1.WebAuthnCredential{SessionId: sessionID, CredentialJson: credentialJSON},
	})
}

// TestFinishPasskeyLoginRefusesAnEmptyCredential pins the request check: a
// Finish call that carries no ceremony session or no credential is the
// caller's malformed request, answered InvalidArgument before the WebAuthn
// library runs. Without the check the library's own error went through the
// credential classifier and came back as another code.
func TestFinishPasskeyLoginRefusesAnEmptyCredential(t *testing.T) {
	t.Parallel()
	env := setupPasskeyAuthTestServer(t, nil, nil)

	for name, req := range map[string]*leapmuxv1.FinishPasskeyLoginRequest{
		"an absent credential": {},
		"no session id":        {Credential: &leapmuxv1.WebAuthnCredential{CredentialJson: `{"id":"x"}`}},
		"no credential":        {Credential: &leapmuxv1.WebAuthnCredential{SessionId: "session"}},
		"neither":              {Credential: &leapmuxv1.WebAuthnCredential{}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := env.client.FinishPasskeyLogin(passkeyCtx(context.Background()), req)
			require.Error(t, err)
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "got %v", err)
		})
	}
}

// TestAuthFinishHandlersRefuseAnEmptyCredential is the consistency half of
// the request check: every anonymous Finish refuses an absent credential, an
// empty session id and an empty credential with InvalidArgument, before any
// ceremony runs.
func TestAuthFinishHandlersRefuseAnEmptyCredential(t *testing.T) {
	t.Parallel()
	env := setupPasskeyAuthTestServer(t, nil, nil)

	for name, cred := range map[string]*leapmuxv1.WebAuthnCredential{
		"an absent credential": nil,
		"no session id":        {CredentialJson: `{"id":"x"}`},
		"no credential":        {SessionId: "session"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := passkeyCtx(context.Background())
			_, err := env.client.FinishPasskeySignUp(ctx, &leapmuxv1.FinishPasskeySignUpRequest{Credential: cred})
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "FinishPasskeySignUp: %v", err)

			// The recovery token is checked first, so the request carries one.
			_, err = env.client.FinishAccountRecoveryPasskey(ctx, &leapmuxv1.FinishAccountRecoveryPasskeyRequest{
				Token: "recovery-token", Credential: cred,
			})
			require.Equal(t, connect.CodeInvalidArgument, connect.CodeOf(err), "FinishAccountRecoveryPasskey: %v", err)
		})
	}
}
