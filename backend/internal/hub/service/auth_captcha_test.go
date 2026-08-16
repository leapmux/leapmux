package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/captcha"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/servicetest"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// setupCaptchaAuthService wires an AuthService behind a REAL captcha
// manager (store + keystore), unlike setupAuthTestServerBase which passes
// nil. Returns the client, the keystore (so tests can encrypt provider
// secrets the resolver can decrypt), and the store so tests can inspect
// the config row's provisioning side effects.
func setupCaptchaAuthService(t *testing.T, solo bool) (leapmuxv1connect.AuthServiceClient, *keystore.Keystore, store.Store) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	captchaMgr := captcha.NewManager(st, ks, solo)

	cfg := testConfig()
	cfg.SoloMode = solo
	authDeps := servicetest.AuthServiceDeps(st, cfg, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Captcha = captchaMgr
	authSvc := service.NewAuthService(authDeps)

	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), ks, st
}

func TestGetAltchaChallenge_IssuesSignedChallenge(t *testing.T) {
	client, _, _ := setupCaptchaAuthService(t, false)

	resp, err := client.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.NoError(t, err)
	require.NotEmpty(t, resp.Msg.GetChallengeJson(), "enabled hub must hand out a challenge")

	var challenge struct {
		Parameters struct {
			Algorithm string `json:"algorithm"`
			Nonce     string `json:"nonce"`
			Salt      string `json:"salt"`
			Cost      int    `json:"cost"`
			KeyLength int    `json:"keyLength"`
			KeyPrefix string `json:"keyPrefix"`
			ExpiresAt int64  `json:"expiresAt"`
		} `json:"parameters"`
		Signature string `json:"signature"`
	}
	require.NoError(t, json.Unmarshal([]byte(resp.Msg.GetChallengeJson()), &challenge))

	assert.Equal(t, "PBKDF2/SHA-256", challenge.Parameters.Algorithm)
	assert.Equal(t, 10000, challenge.Parameters.Cost)
	assert.Equal(t, 32, challenge.Parameters.KeyLength)
	assert.NotEmpty(t, challenge.Parameters.Nonce)
	assert.NotEmpty(t, challenge.Parameters.Salt)
	assert.NotEmpty(t, challenge.Signature, "challenge must carry the HMAC signature the interceptor verifies")
	assert.Greater(t, challenge.Parameters.ExpiresAt, time.Now().Unix(), "challenge must expire in the future")
}

func TestGetAltchaChallenge_SoloReturnsEmpty(t *testing.T) {
	client, _, _ := setupCaptchaAuthService(t, true)

	resp, err := client.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetChallengeJson(), "solo mode never arms captcha")
}

func TestGetAltchaChallenge_NilCaptchaReportsDisabled(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), auth.NewCredentialLifecycleEffects(nil, nil, nil)))

	// A nil captcha dependency means "subsystem absent": challenges stand
	// down with an empty payload (the widget's stand-down signal), and
	// system info reports captcha disabled — never a hard error.
	resp, err := authSvc.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetChallengeJson())

	info, err := authSvc.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, info.Msg.GetCaptchaEnabled())
}

func TestGetSystemInfo_ReportsCaptchaState(t *testing.T) {
	client, _, _ := setupCaptchaAuthService(t, false)

	// Fresh install: defaults apply, and reporting them must NOT provision
	// the signing secret (that is challenge issuance's job).
	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetCaptchaEnabled())
	assert.Equal(t, "PBKDF2/SHA-256", resp.Msg.GetAltchaAlgorithm())

	// First challenge provisions the row; a second system-info call still
	// reports the (unchanged) effective state.
	_, err = client.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.NoError(t, err)

	resp, err = client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.True(t, resp.Msg.GetCaptchaEnabled())
}

// failingCaptchaStub simulates a config-store outage on the captcha
// surface, so GetSystemInfo's degradation can be exercised without
// breaking a real store.
type failingCaptchaStub struct {
	captcha.Config
	customized bool
	err        error
}

func (s failingCaptchaStub) Describe(context.Context) (captcha.Config, bool, error) {
	return s.Config, s.customized, s.err
}

func (s failingCaptchaStub) AltchaChallengeJSON(context.Context) (string, error) {
	return "", s.err
}

// TestGetSystemInfo_DegradesWhenCaptchaConfigUnreachable pins the
// degradation: a captcha-config read error must not fail the whole public
// endpoint (the flags pattern), while the report stays consistent with
// the interceptor's fail-closed enforcement (reporting "disabled" would
// unblock a payload-less submit that the hub then denies), and challenge
// issuance still surfaces its error.
func TestGetSystemInfo_DegradesWhenCaptchaConfigUnreachable(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	authDeps := servicetest.AuthServiceDeps(st, testConfig(), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Captcha = failingCaptchaStub{err: errors.New("no such table captcha_config")}
	authSvc := service.NewAuthService(authDeps)

	resp, err := authSvc.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err, "system info must survive a captcha-config outage")
	assert.True(t, resp.Msg.GetCaptchaEnabled(), "degraded reporting must match the fail-closed interceptor, not unlock a payload-less submit")
	assert.Equal(t, leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_UNSPECIFIED, resp.Msg.GetCaptchaProvider(), "degraded reporting must not name a provider")

	_, err = authSvc.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.Error(t, err, "challenge issuance still fails honestly on the same outage")
}

func TestGetSystemInfo_SoloReportsCaptchaDisabled(t *testing.T) {
	client, _, _ := setupCaptchaAuthService(t, true)

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetCaptchaEnabled())
}

// TestGetSystemInfo_ReportsExternalProvider pins the provider/site-key
// surface external-provider frontends switch on: the selected provider's
// name, its public site key, and the altcha algorithm going empty.
func TestGetSystemInfo_ReportsExternalProvider(t *testing.T) {
	client, ks, st := setupCaptchaAuthService(t, false)

	encrypted, err := captcha.EncryptSecret(ks, captcha.ProviderTurnstile, "secret-key")
	require.NoError(t, err)
	require.NoError(t, st.CaptchaConfig().Upsert(context.Background(), store.UpsertCaptchaConfigParams{
		Provider: captcha.ProviderTurnstile,
		Secret:   encrypted,
		Settings: `{"site_key":"1x00000000000000000000AA"}`,
	}))
	require.NoError(t, st.CaptchaConfig().Activate(context.Background(), captcha.ProviderTurnstile))

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.Equal(t, captcha.ProviderTurnstile, resp.Msg.GetCaptchaProvider())
	assert.Equal(t, "1x00000000000000000000AA", resp.Msg.GetCaptchaSiteKey())
	assert.Empty(t, resp.Msg.GetAltchaAlgorithm())
}
