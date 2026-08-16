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
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
)

// setupCaptchaAuthService wires an AuthService behind a REAL captcha
// manager (store + keystore + settings), unlike setupAuthTestServerBase
// which passes nil. Returns the client, the keystore (so tests can
// encrypt provider secrets the resolver can decrypt), the store so tests
// can inspect the config row's provisioning side effects, and the shared
// settings manager (the admin CLI's write handle).
func setupCaptchaAuthService(t *testing.T, solo bool) (leapmuxv1connect.AuthServiceClient, *keystore.Keystore, store.Store, *settings.Manager) {
	t.Helper()

	st := hubtestutil.OpenTestStore(t)
	key := [32]byte{}
	ks, err := keystore.New(map[uint32][32]byte{1: key})
	require.NoError(t, err)
	set := servicetest.NewSettingsManager(t, st, ks)
	captchaMgr := captcha.NewManager(st, set, solo)

	cfg := testConfig()
	cfg.SoloMode = solo
	authDeps := servicetest.AuthServiceDeps(st, cfg, set, auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Captcha = captchaMgr
	authSvc := service.NewAuthService(authDeps)

	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewAuthServiceHandler(authSvc)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), ks, st, set
}

func TestGetAltchaChallenge_IssuesSignedChallenge(t *testing.T) {
	client, _, _, _ := setupCaptchaAuthService(t, false)

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
	client, _, _, _ := setupCaptchaAuthService(t, true)

	resp, err := client.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.NoError(t, err)
	assert.Empty(t, resp.Msg.GetChallengeJson(), "solo mode never arms captcha")
}

func TestGetAltchaChallenge_NilCaptchaReportsDisabled(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	authSvc := service.NewAuthService(servicetest.AuthServiceDeps(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil)))

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
	client, _, _, _ := setupCaptchaAuthService(t, false)

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

// failingCaptchaStub simulates a captcha-subsystem outage on the
// challenge-issuance surface. GetSystemInfo has no captcha error path to
// exercise: Describe cannot fail (the settings snapshot serves the last
// good state and degrades invalid rows internally), so the public
// endpoint's only honest failure surface is challenge issuance.
type failingCaptchaStub struct {
	captcha.Config
	err error
}

func (s failingCaptchaStub) Describe(context.Context) captcha.Config { return s.Config }

func (s failingCaptchaStub) AltchaChallengeJSON(context.Context) (string, error) {
	return "", s.err
}

// TestGetAltchaChallenge_FailsClosedWhenCaptchaUnreachable pins the one
// captcha outage surface GetSystemInfo's sibling endpoint has: challenge
// issuance fails honestly rather than standing down, while the stub's
// config (enabled, provider) still reports through GetSystemInfo.
func TestGetAltchaChallenge_FailsClosedWhenCaptchaUnreachable(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	authDeps := servicetest.AuthServiceDeps(st, testConfig(), servicetest.NewSettingsManager(t, st, nil), auth.NewCredentialLifecycleEffects(nil, nil, nil))
	authDeps.Captcha = failingCaptchaStub{Config: captcha.DisabledConfig(), err: errors.New("settings snapshot unavailable")}
	authSvc := service.NewAuthService(authDeps)

	resp, err := authSvc.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetCaptchaEnabled(), "the stub's config reports as-is")

	_, err = authSvc.GetAltchaChallenge(context.Background(), connect.NewRequest(&leapmuxv1.GetAltchaChallengeRequest{}))
	require.Error(t, err, "challenge issuance fails honestly on the outage")
}

func TestGetSystemInfo_SoloReportsCaptchaDisabled(t *testing.T) {
	client, _, _, _ := setupCaptchaAuthService(t, true)

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.False(t, resp.Msg.GetCaptchaEnabled())
}

// TestGetSystemInfo_ReportsExternalProvider pins the provider/site-key
// surface external-provider frontends switch on: the selected provider's
// name, its public site key, and the altcha algorithm going empty.
func TestGetSystemInfo_ReportsExternalProvider(t *testing.T) {
	client, _, _, set := setupCaptchaAuthService(t, false)

	// The admin CLI's activation: the provider's row (site key plus the
	// secret in its encrypted half) and the selection.
	require.NoError(t, captcha.TurnstileKey.Set(context.Background(), set, captcha.TurnstileRow{
		SiteKey:   "1x00000000000000000000AA",
		SecretKey: "secret-key",
	}))
	require.NoError(t, captcha.CaptchaSelectedKey.Set(context.Background(), set, "turnstile"))

	resp, err := client.GetSystemInfo(context.Background(), connect.NewRequest(&leapmuxv1.GetSystemInfoRequest{}))
	require.NoError(t, err)
	assert.Equal(t, captcha.ProviderTurnstile, resp.Msg.GetCaptchaProvider())
	assert.Equal(t, "1x00000000000000000000AA", resp.Msg.GetCaptchaSiteKey())
	assert.Empty(t, resp.Msg.GetAltchaAlgorithm())
}
