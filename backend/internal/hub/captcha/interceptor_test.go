package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/metrics"
)

// newLoginClient wires a one-procedure Login handler behind the captcha
// interceptor, so requests reach it with a fully populated Spec exactly as
// the hub's mux would populate it. handlerCalled reports whether the
// protected handler ran.
func newLoginClient(t *testing.T, ic connect.UnaryInterceptorFunc) (leapmuxv1connect.AuthServiceClient, *bool) {
	t.Helper()
	handlerCalled := false
	handler := connect.NewUnaryHandler(
		leapmuxv1connect.AuthServiceLoginProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.LoginRequest]) (*connect.Response[leapmuxv1.LoginResponse], error) {
			handlerCalled = true
			return connect.NewResponse(&leapmuxv1.LoginResponse{}), nil
		},
		connect.WithInterceptors(ic),
	)
	server := httptest.NewServer(http.NewServeMux())
	server.Config.Handler.(*http.ServeMux).Handle(leapmuxv1connect.AuthServiceLoginProcedure, handler)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL), &handlerCalled
}

func freshVerifiedPayload(t *testing.T, m *Manager) string {
	t.Helper()
	applyTestAltchaSettings(t, m, cheapAltchaSettings)
	challengeJSON, err := m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	return solveChallenge(t, challengeJSON)
}

// disableSelected flips the selected row's enabled flag and busts the
// caches, the way `captcha disable` does.
func disableSelected(t *testing.T, m *Manager) {
	t.Helper()
	require.NoError(t, m.EnsureProvisioned(context.Background()))
	require.NoError(t, m.st.CaptchaConfig().SetEnabled(context.Background(), false))
	bustCaches(m)
}

// TestInterceptorPassesProcedureAction pins the procedure-to-action
// mapping: the stub answers success only for the login action, so a Login
// that passes verification proves the interceptor asked for "login".
func TestInterceptorPassesProcedureAction(t *testing.T) {
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login"}`
	m := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, m, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	client, called := newLoginClient(t, NewInterceptor(m))

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		CaptchaPayload: "token",
	}))
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestInterceptorAllowsVerifiedSubmission(t *testing.T) {
	m := newTestManager(t, false)
	payload := freshVerifiedPayload(t, m)
	client, called := newLoginClient(t, NewInterceptor(m))

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Username:       "user",
		Password:       "pass",
		CaptchaPayload: payload,
	}))
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestInterceptorDeniesMissingOrInvalidPayload(t *testing.T) {
	m := newTestManager(t, false)
	freshVerifiedPayload(t, m) // configure + provision
	client, called := newLoginClient(t, NewInterceptor(m))

	failedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed"))
	replayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed"))
	for _, payload := range []string{"", "garbage"} {
		_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
			CaptchaPayload: payload,
		}))
		require.Error(t, err, "payload %q", payload)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	}
	assert.False(t, *called)
	// Unsolvable payloads count as plain failures, never as replays: the
	// labels split exactly on salt reuse.
	assert.Equal(t, failedBefore+2, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed")))
	assert.Equal(t, replayedBefore, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed")))
}

func TestInterceptorDeniesHoneypotUniformly(t *testing.T) {
	m := newTestManager(t, false)
	payload := freshVerifiedPayload(t, m)
	client, called := newLoginClient(t, NewInterceptor(m))

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
		Honeypot:       "http://spam.example",
	}))
	require.Error(t, err)
	// Same code and message as a captcha failure: no oracle for which
	// check tripped. (ErrorIs cannot be used across the wire — the client
	// rebuilds the error from code + message.)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), ErrVerificationFailed.Error())
	assert.False(t, *called)
}

func TestInterceptorDeniesReplay(t *testing.T) {
	m := newTestManager(t, false)
	payload := freshVerifiedPayload(t, m)
	client, _ := newLoginClient(t, NewInterceptor(m))

	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
	}))
	require.NoError(t, err)

	replayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed"))
	_, err = client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	// The denial itself stays uniform; only the counter splits replay out.
	// The internal sentinel's text ("token or salt already used") must never reach
	// the client — the uniform message is what keeps the no-oracle promise.
	assert.Contains(t, err.Error(), ErrVerificationFailed.Error())
	assert.NotContains(t, err.Error(), "token or salt already used")
	assert.Equal(t, replayedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed")))
}

func TestInterceptorPassesThroughWhenDisabled(t *testing.T) {
	m := newTestManager(t, false)
	disableSelected(t, m)
	client, called := newLoginClient(t, NewInterceptor(m))
	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{}))
	require.NoError(t, err)
	assert.True(t, *called)

	solo := newTestManager(t, true)
	soloClient, soloCalled := newLoginClient(t, NewInterceptor(solo))
	_, err = soloClient.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{}))
	require.NoError(t, err)
	assert.True(t, *soloCalled)
}

// TestInterceptorHoneypotRunsWhileCaptchaDisabled pins the always-on
// honeypot: it must keep catching naive bots after `captcha disable`,
// with the same uniform denial as a captcha failure.
func TestInterceptorHoneypotRunsWhileCaptchaDisabled(t *testing.T) {
	m := newTestManager(t, false)
	disableSelected(t, m)
	client, called := newLoginClient(t, NewInterceptor(m))

	failedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed"))
	_, err := client.Login(context.Background(), connect.NewRequest(&leapmuxv1.LoginRequest{
		Honeypot: "http://spam.example",
	}))
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), ErrVerificationFailed.Error())
	assert.False(t, *called)
	assert.Equal(t, failedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed")))
}

func TestInterceptorIgnoresUnprotectedProcedures(t *testing.T) {
	m := newTestManager(t, false)
	freshVerifiedPayload(t, m)

	// Logout carries neither captcha field; routed through the same
	// interceptor it must reach its handler unconditionally.
	logoutCalled := false
	handler := connect.NewUnaryHandler(
		leapmuxv1connect.AuthServiceLogoutProcedure,
		func(ctx context.Context, req *connect.Request[leapmuxv1.LogoutRequest]) (*connect.Response[leapmuxv1.LogoutResponse], error) {
			logoutCalled = true
			return connect.NewResponse(&leapmuxv1.LogoutResponse{}), nil
		},
		connect.WithInterceptors(NewInterceptor(m)),
	)
	server := httptest.NewServer(http.NewServeMux())
	server.Config.Handler.(*http.ServeMux).Handle(leapmuxv1connect.AuthServiceLogoutProcedure, handler)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(server.Client(), server.URL)

	_, err := client.Logout(context.Background(), connect.NewRequest(&leapmuxv1.LogoutRequest{}))
	require.NoError(t, err)
	assert.True(t, logoutCalled)
}
