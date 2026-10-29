package captcha

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/hub/ratelimit"
	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/metrics"
)

// mountUnary registers one stub procedure behind the interceptor on server:
// the handler answers an empty response and flips the flag it returns, so a
// test proves a request reached (or never reached) the handler. Taking the
// server lets a harness register several procedures before one Mount, each
// with its own flag.
func mountUnary[Req any, Resp any](server *connect.Server, procedure string) *bool {
	called := false
	server.Register(connect.Method{
		Spec: connect.Spec{StreamType: connect.StreamTypeUnary, Procedure: procedure},
		Handler: func(_ context.Context, _ connect.Spec, stream connect.ServerStream) error {
			var req Req
			if err := stream.Receive(&req); err != nil {
				return err
			}
			called = true
			return stream.Send(new(Resp))
		},
	})
	return &called
}

// newLoginClient wires a one-procedure Login handler behind the captcha
// interceptor, so requests reach it with a fully populated Spec exactly as
// the hub's mux would populate it. handlerCalled reports whether the
// protected handler ran.
func newLoginClient(t *testing.T, ic connect.ServerInterceptor) (leapmuxv1connect.AuthServiceClient, *bool) {
	t.Helper()
	connectServer := connect.NewServer(ic)
	called := mountUnary[leapmuxv1.LoginRequest, leapmuxv1.LoginResponse](connectServer, leapmuxv1connect.AuthServiceLoginProcedure)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))), called
}

func freshVerifiedPayload(t *testing.T, e *testEnv) string {
	t.Helper()
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	challengeJSON, err := e.m.AltchaChallengeJSON(context.Background())
	require.NoError(t, err)
	return solveChallenge(t, challengeJSON)
}

// disableSelected flips the verification switch off, the way `captcha
// disable` does.
func disableSelected(t *testing.T, e *testEnv) {
	t.Helper()
	require.NoError(t, e.m.EnsureProvisioned(context.Background()))
	require.NoError(t, CaptchaEnabledKey.Set(context.Background(), e.set, false))
}

// TestInterceptorPassesProcedureAction pins the procedure-to-action
// mapping: the stub answers success only for the login action, so a Login
// that passes verification proves the interceptor asked for "login".
func TestInterceptorPassesProcedureAction(t *testing.T) {
	stub := newSiteverifyStub(t)
	stub.body = `{"success":true,"action":"login"}`
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	client, called := newLoginClient(t, NewInterceptor(e.m))

	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
		CaptchaPayload: "token",
	})
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestInterceptorVerificationPrecedesRateLimitAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	finishVerification := func() { releaseOnce.Do(func() { close(release) }) }
	verification := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		w.Header().Set("Content-Type", "application/json")
		if r.FormValue("response") == "slow" {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			_, _ = w.Write([]byte(`{"success":false}`))
			return
		}
		_, _ = w.Write([]byte(`{"success":true,"action":"login"}`))
	}))
	t.Cleanup(verification.Close)
	t.Cleanup(finishVerification)
	e := newTestManager(t, false, WithTurnstileEndpoint(verification.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"site-key"}`, "secret-key")
	rateSettings := settings.NewManager(e.st, nil, ratelimit.SettingsDescriptors())
	require.NoError(t, rateSettings.Load(ctx))
	key, ok := ratelimit.LimitKey(ratelimit.OpLoginAnonymous)
	require.True(t, ok)
	require.NoError(t, key.Set(ctx, rateSettings, ratelimit.LimitValue{Enabled: true, MaxAttempts: 1, WindowSeconds: 900}))
	limiter := ratelimit.NewInterceptor(ratelimit.NewManager(rateSettings))
	client, called := newLoginClient(t, func(next connect.ServerFunc) connect.ServerFunc {
		return NewInterceptor(e.m)(limiter(next))
	})
	done := make(chan error, 1)
	go func() {
		_, err := client.Login(ctx, &leapmuxv1.LoginRequest{CaptchaPayload: "slow"})
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err := client.Login(ctx, &leapmuxv1.LoginRequest{CaptchaPayload: "valid"})
	assert.NoError(t, err, "pending verification must not occupy the login allowance")
	assert.True(t, *called)
	select {
	case err := <-done:
		t.Fatalf("the first verification ended before its release: %v", err)
	default:
	}
	finishVerification()
	select {
	case err := <-done:
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestInterceptorAllowsVerifiedSubmission(t *testing.T) {
	e := newTestManager(t, false)
	payload := freshVerifiedPayload(t, e)
	client, called := newLoginClient(t, NewInterceptor(e.m))

	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
		Username:       "user",
		Password:       "pass",
		CaptchaPayload: payload,
	})
	require.NoError(t, err)
	assert.True(t, *called)
}

func TestInterceptorDeniesMissingOrInvalidPayload(t *testing.T) {
	e := newTestManager(t, false)
	freshVerifiedPayload(t, e) // configure + provision
	client, called := newLoginClient(t, NewInterceptor(e.m))

	failedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed"))
	replayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed"))
	for _, payload := range []string{"", "garbage"} {
		_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
			CaptchaPayload: payload,
		})
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
	e := newTestManager(t, false)
	payload := freshVerifiedPayload(t, e)
	client, called := newLoginClient(t, NewInterceptor(e.m))

	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
		Honeypot:       "http://spam.example",
	})
	require.Error(t, err)
	// Same code and message as a captcha failure: no oracle for which
	// check tripped. (ErrorIs cannot be used across the wire — the client
	// rebuilds the error from code + message.)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), ErrVerificationFailed.Error())
	assert.False(t, *called)
}

func TestInterceptorDeniesReplay(t *testing.T) {
	e := newTestManager(t, false)
	payload := freshVerifiedPayload(t, e)
	client, _ := newLoginClient(t, NewInterceptor(e.m))

	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
	})
	require.NoError(t, err)

	replayedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "replayed"))
	_, err = client.Login(context.Background(), &leapmuxv1.LoginRequest{
		CaptchaPayload: payload,
	})
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
	e := newTestManager(t, false)
	disableSelected(t, e)
	client, called := newLoginClient(t, NewInterceptor(e.m))
	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{})
	require.NoError(t, err)
	assert.True(t, *called)

	solo := newTestManager(t, true)
	soloClient, soloCalled := newLoginClient(t, NewInterceptor(solo.m))
	_, err = soloClient.Login(context.Background(), &leapmuxv1.LoginRequest{})
	require.NoError(t, err)
	assert.True(t, *soloCalled)
}

// TestInterceptorIgnoresOriginForTheSecureContextGate is the security
// half of the secure-context rule, and the reason the gate stopped reading
// the request.
//
// The gate used to take its answer from the Origin header, so any caller
// could send "Origin: http://a" and switch ALTCHA off for its own request
// -- in front of Login, RequestAccountRecovery and both passkey Begin
// procedures, which have no other automation control. The hub now decides
// from its own configuration, so a forged header changes nothing.
func TestInterceptorIgnoresOriginForTheSecureContextGate(t *testing.T) {
	// A published HTTPS hub: ALTCHA is enforced here, and no header may
	// say otherwise.
	e := newTestManagerPublishedAt(t, testPublicURL, false)
	freshVerifiedPayload(t, e) // provision + cheap settings; settings stay enabled
	client, called := newLoginClient(t, NewInterceptor(e.m))

	for _, origin := range []string{
		"http://a",
		"http://192.168.1.5:8080",
		"http://evil.test",
		"null",
	} {
		*called = false
		ctx, info := connect.NewClientContext(context.Background())
		info.RequestHeader().Set("Origin", origin)
		// Referer is the other half of the old header pair, so forge both.
		info.RequestHeader().Set("Referer", "http://192.168.1.5:8080/login")
		_, err := client.Login(ctx, &leapmuxv1.LoginRequest{})
		require.Errorf(t, err, "Origin %q must not disable the captcha", origin)
		assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
		assert.Falsef(t, *called,
			"Origin %q reached the Argon2 handler with no captcha token", origin)
	}

	// A real token still passes, so the check refuses the forgery rather
	// than refusing everything.
	*called = false
	payload := freshVerifiedPayload(t, e)
	okCtx, okInfo := connect.NewClientContext(context.Background())
	okInfo.RequestHeader().Set("Origin", "http://a")
	_, err := client.Login(okCtx, &leapmuxv1.LoginRequest{CaptchaPayload: payload})
	require.NoError(t, err)
	assert.True(t, *called)
}

// TestInterceptorStandsDownWhenTheHubCannotServeASecureContext pins the
// usability half: on a hub that cannot offer a solvable ALTCHA challenge,
// a payload-less Login passes. The honeypot keeps running either way.
func TestInterceptorStandsDownWhenTheHubCannotServeASecureContext(t *testing.T) {
	e := newTestManagerPublishedAt(t, "http://192.168.1.5:8080", false)
	// Provision the ALTCHA row and its cheap tuning WITHOUT minting a
	// challenge: this hub stands ALTCHA down, so AltchaChallengeJSON
	// correctly returns nothing and there is no challenge to solve.
	applyTestAltchaSettings(t, e, cheapAltchaSettings)
	client, called := newLoginClient(t, NewInterceptor(e.m))

	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{})
	require.NoError(t, err)
	assert.True(t, *called)
	assert.True(t, CaptchaEnabledKey.Of(e.set.Snapshot(context.Background())),
		"the interceptor must not persist captcha.enabled=false")

	*called = false
	honeypotReq := &leapmuxv1.LoginRequest{Honeypot: "http://spam.example"}
	_, err = client.Login(context.Background(), honeypotReq)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, *called)
}

// TestInterceptorHoneypotRunsWhileCaptchaDisabled pins the always-on
// honeypot: it must keep catching naive bots after `captcha disable`,
// with the same uniform denial as a captcha failure.
func TestInterceptorHoneypotRunsWhileCaptchaDisabled(t *testing.T) {
	e := newTestManager(t, false)
	disableSelected(t, e)
	client, called := newLoginClient(t, NewInterceptor(e.m))

	failedBefore := testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed"))
	_, err := client.Login(context.Background(), &leapmuxv1.LoginRequest{
		Honeypot: "http://spam.example",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Contains(t, err.Error(), ErrVerificationFailed.Error())
	assert.False(t, *called)
	assert.Equal(t, failedBefore+1, testutil.ToFloat64(metrics.CaptchaVerificationsTotal.WithLabelValues("altcha", "failed")))
}

func TestInterceptorIgnoresUnprotectedProcedures(t *testing.T) {
	e := newTestManager(t, false)
	freshVerifiedPayload(t, e)

	// Logout carries neither captcha field; routed through the same
	// interceptor it must reach its handler unconditionally.
	connectServer := connect.NewServer(NewInterceptor(e.m))
	logoutCalled := mountUnary[leapmuxv1.LogoutRequest, leapmuxv1.LogoutResponse](connectServer, leapmuxv1connect.AuthServiceLogoutProcedure)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := leapmuxv1connect.NewAuthServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0))))

	_, err := client.Logout(context.Background(), &leapmuxv1.LogoutRequest{})
	require.NoError(t, err)
	assert.True(t, *logoutCalled)
}

// newVerificationClient wires the two captcha-protected UserService
// procedures behind the captcha interceptor, the way newLoginClient wires
// Login. These are the first AUTHENTICATED procedures in
// protectedProcedures, and this harness proves the map entries bite on the
// real procedure paths rather than only in the classification test.
func newVerificationClient(t *testing.T, ic connect.ServerInterceptor) (leapmuxv1connect.UserServiceClient, *bool, *bool) {
	t.Helper()
	connectServer := connect.NewServer(ic)
	verifyCalled := mountUnary[leapmuxv1.VerifyEmailRequest, leapmuxv1.VerifyEmailResponse](connectServer, leapmuxv1connect.UserServiceVerifyEmailProcedure)
	resendCalled := mountUnary[leapmuxv1.ResendVerificationEmailRequest, leapmuxv1.ResendVerificationEmailResponse](connectServer, leapmuxv1connect.UserServiceResendVerificationEmailProcedure)
	mux := http.NewServeMux()
	connecthttp.Mount(mux, connectServer)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return leapmuxv1connect.NewUserServiceClient(connect.NewClient(connecthttp.NewTransport(server.Client(), server.URL, connecthttp.WithReadMaxBytes(0)))), verifyCalled, resendCalled
}

// TestInterceptorProtectsTheVerificationProcedures pins the enforcement
// itself: both authenticated procedures deny without a solved payload and
// admit with one, exactly like the anonymous ones.
func TestInterceptorProtectsTheVerificationProcedures(t *testing.T) {
	e := newTestManager(t, false)
	payload := freshVerifiedPayload(t, e)
	client, verifyCalled, resendCalled := newVerificationClient(t, NewInterceptor(e.m))

	_, err := client.VerifyEmail(context.Background(), &leapmuxv1.VerifyEmailRequest{
		VerificationToken: "AB2CDE",
	})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, *verifyCalled, "a payload-less verify must never reach the handler")

	_, err = client.VerifyEmail(context.Background(), &leapmuxv1.VerifyEmailRequest{
		VerificationToken: "AB2CDE",
		CaptchaPayload:    payload,
	})
	require.NoError(t, err)
	assert.True(t, *verifyCalled)

	// ALTCHA rejects salt reuse, so solve a fresh challenge for the
	// resend path.
	secondPayload := freshVerifiedPayload(t, e)
	_, err = client.ResendVerificationEmail(context.Background(), &leapmuxv1.ResendVerificationEmailRequest{})
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.False(t, *resendCalled, "a payload-less resend must never reach the handler")

	_, err = client.ResendVerificationEmail(context.Background(), &leapmuxv1.ResendVerificationEmailRequest{
		CaptchaPayload: secondPayload,
	})
	require.NoError(t, err)
	assert.True(t, *resendCalled)
}

// TestInterceptorPassesVerificationProcedureActions pins the ACTION each
// procedure verifies under: the Turnstile stub answers success only for the
// action named in its body, so the passing call proves the interceptor used
// that action, and the mismatching one proves the comparison is enforced.
func TestInterceptorPassesVerificationProcedureActions(t *testing.T) {
	stub := newSiteverifyStub(t)
	e := newTestManager(t, false, WithTurnstileEndpoint(stub.server.URL))
	activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
	client, verifyCalled, resendCalled := newVerificationClient(t, NewInterceptor(e.m))

	stub.body = `{"success":true,"action":"login"}`
	_, err := client.VerifyEmail(context.Background(), &leapmuxv1.VerifyEmailRequest{
		CaptchaPayload: "token",
	})
	require.Error(t, err, "a token minted under a different action must be refused")
	assert.False(t, *verifyCalled)

	stub.body = `{"success":true,"action":"verify_email"}`
	_, err = client.VerifyEmail(context.Background(), &leapmuxv1.VerifyEmailRequest{
		CaptchaPayload: "token",
	})
	require.NoError(t, err)
	assert.True(t, *verifyCalled)

	stub.body = `{"success":true,"action":"resend_verification"}`
	_, err = client.ResendVerificationEmail(context.Background(), &leapmuxv1.ResendVerificationEmailRequest{
		CaptchaPayload: "token",
	})
	require.NoError(t, err)
	assert.True(t, *resendCalled)
}
