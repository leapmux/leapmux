package hub

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/mail"
	"github.com/leapmux/leapmux/internal/hub/oauthapp"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/hub/service"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/userid"
)

const (
	wiringAdminName     = "wiring-admin"
	wiringAdminPassword = "adminpass123"
	wiringRedirectURI   = "http://127.0.0.1:54321/callback"
)

// wiringHub is a running Hub and one elevated administrator. Each request goes
// through the Hub's real handler, so a dependency that NewServer fails to pass
// shows up as the behaviour a client sees.
type wiringHub struct {
	srv     *Server
	url     string
	adminID string
	session string
}

func startWiringHub(t *testing.T, opts ...ServerOption) *wiringHub {
	t.Helper()
	srv := startTestServer(t, &config.Config{}, opts...)
	api := httptest.NewServer(srv.server.Handler)
	t.Cleanup(api.Close)

	ctx := context.Background()
	hash, err := password.Hash(wiringAdminPassword)
	require.NoError(t, err)
	admin, err := service.CreateUser(ctx, srv.store, service.CreateUserParams{
		Username:              wiringAdminName,
		PasswordHash:          hash,
		DisplayName:           "Wiring Admin",
		FirstCredentialExempt: true,
		IsAdmin:               true,
		// Verified, because a credential notice goes to no other address.
		Email:         "wiring-admin@example.test",
		EmailVerified: true,
	})
	require.NoError(t, err)
	session, _, _, err := auth.Login(ctx, srv.store, wiringAdminName, wiringAdminPassword, auth.DefaultSessionDuration)
	require.NoError(t, err)
	hubtestutil.ElevateSession(t, srv.store, session, admin.ID)
	return &wiringHub{srv: srv, url: api.URL, adminID: admin.ID, session: session}
}

// storedToken writes an API token and returns its bearer and refresh strings.
func (h *wiringHub) storedToken(t *testing.T) (bearer, refresh string) {
	t.Helper()
	pepper := h.srv.keystore.Pepper()
	validator, err := auth.NewTokenValidator(h.srv.store, pepper[:])
	require.NoError(t, err)
	tokenID := id.Generate()
	secret, refreshSecret := auth.MintAccessSecret(), auth.MintAccessSecret()
	require.NoError(t, h.srv.store.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(h.adminID),
		ClientID:         oauthapp.ControlCLIClientID,
		InstallationName: "wiring",
		GrantedScopes:    authscope.NonAdminGrant().String(),
		SecretHash:       validator.HashSecret(secret),
		RefreshHash:      validator.HashSecret(refreshSecret),
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret), auth.FormatBearer(auth.BearerKindAPI, tokenID, refreshSecret)
}

// connectStatus sends one Connect call and returns its HTTP status.
func (h *wiringHub) connectStatus(t *testing.T, procedure string, msg proto.Message, authenticate func(*http.Request)) int {
	t.Helper()
	body, err := protojson.Marshal(msg)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodPost, h.url+procedure, bytes.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connect-Protocol-Version", "1")
	authenticate(req)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// bearerStatus reads the current user with a bearer, which runs the interceptor's
// bearer flight on a cold cache.
func (h *wiringHub) bearerStatus(t *testing.T, bearer string) int {
	t.Helper()
	return h.connectStatus(t, leapmuxv1connect.AuthServiceGetCurrentUserProcedure, &leapmuxv1.GetCurrentUserRequest{}, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+bearer)
	})
}

// refreshStatus runs the OAuth handler's refresh flight.
func (h *wiringHub) refreshStatus(t *testing.T, refresh string) int {
	t.Helper()
	resp, err := http.PostForm(h.url+"/oauth/token", url.Values{
		"grant_type":    {service.GrantTypeRefreshToken},
		"client_id":     {oauthapp.ControlCLIClientID},
		"refresh_token": {refresh},
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

// mintThroughOAuth runs the consent and the code exchange of the OAuth surface.
func (h *wiringHub) mintThroughOAuth(t *testing.T) {
	t.Helper()
	verifier := id.Generate()
	sum := sha256.Sum256([]byte(verifier))
	form := url.Values{
		"client_id":             {oauthapp.ControlCLIClientID},
		"response_type":         {"code"},
		"code_challenge_method": {"S256"},
		"redirect_uri":          {wiringRedirectURI},
		"state":                 {id.Generate()},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"decision":              {"allow"},
	}
	req, err := http.NewRequest(http.MethodPost, h.url+"/oauth/consent", strings.NewReader(form.Encode()))
	require.NoError(t, err)
	req.AddCookie(&http.Cookie{Name: auth.CookieName, Value: h.session})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	consent, err := noRedirect.Do(req)
	require.NoError(t, err)
	defer func() { _ = consent.Body.Close() }()
	location, err := url.Parse(consent.Header.Get("Location"))
	require.NoError(t, err)
	code := location.Query().Get("code")
	require.NotEmpty(t, code, "the consent must redirect with a code, got status %d", consent.StatusCode)

	exchange, err := http.PostForm(h.url+"/oauth/token", url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {oauthapp.ControlCLIClientID},
		"redirect_uri":  {wiringRedirectURI},
		"code":          {code},
		"code_verifier": {verifier},
	})
	require.NoError(t, err)
	defer func() { _ = exchange.Body.Close() }()
	require.Equal(t, http.StatusOK, exchange.StatusCode)
}

// mintThroughAdmin calls the RPC of the administrator surface.
func (h *wiringHub) mintThroughAdmin(t *testing.T) {
	t.Helper()
	status := h.connectStatus(t, leapmuxv1connect.AdminUserServiceIssueAPITokenProcedure, &leapmuxv1.IssueAPITokenRequest{
		User:             service.SelectUserByUsername(wiringAdminName),
		InstallationName: "wiring",
	}, func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: auth.CookieName, Value: h.session})
	})
	require.Equal(t, http.StatusOK, status)
}

// Both flights are detached from the request, so only servingCtx can end them
// before the store closes. Each surface receives that context separately, so
// each one must stop when the Hub's lifetime ends.
func TestNewServerGivesBothDetachedFlightsTheHubsLifetime(t *testing.T) {
	hub := startWiringHub(t)
	liveBearer, liveRefresh := hub.storedToken(t)
	endedBearer, endedRefresh := hub.storedToken(t)

	require.Equal(t, http.StatusOK, hub.bearerStatus(t, liveBearer), "a live Hub answers a cold bearer")
	require.Equal(t, http.StatusOK, hub.refreshStatus(t, liveRefresh), "a live Hub rotates a refresh token")

	hub.srv.cancelServing()

	assert.NotEqual(t, http.StatusOK, hub.bearerStatus(t, endedBearer),
		"the interceptor's flight must end with the Hub's lifetime")
	assert.NotEqual(t, http.StatusOK, hub.refreshStatus(t, endedRefresh),
		"the OAuth handler's flight must end with the Hub's lifetime")
}

// parkedMailSender holds each send open until the test releases it.
type parkedMailSender struct {
	entered chan struct{}
	release chan struct{}
}

func (s *parkedMailSender) Send(context.Context, mail.Message) error {
	s.entered <- struct{}{}
	<-s.release
	return nil
}

// Serve joins the notices through one counter, and two surfaces mint. A surface
// that NewServer gave no counter would send past the store close unjoined.
func TestNewServerCountsTheNoticesOfBothMintSurfaces(t *testing.T) {
	sender := &parkedMailSender{entered: make(chan struct{}, 1), release: make(chan struct{})}
	hub := startWiringHub(t, withMailSender(sender))
	// Runs before the Hub's own cleanup, whose Serve joins any parked send.
	t.Cleanup(func() { close(sender.release) })

	for _, tc := range []struct {
		name string
		mint func(*wiringHub, *testing.T)
	}{
		{name: "the OAuth code exchange", mint: (*wiringHub).mintThroughOAuth},
		{name: "the administrator RPC", mint: (*wiringHub).mintThroughAdmin},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.mint(hub, t)
			select {
			case <-sender.entered:
			case <-time.After(30 * time.Second):
				t.Fatal("the mint never reached the sender")
			}

			assert.False(t, hub.srv.credentialNotices.Wait(0, ""), "the Hub's counter must hold a running send")

			sender.release <- struct{}{}
			assert.True(t, hub.srv.credentialNotices.Wait(30*time.Second, ""), "an ended send must leave the count")
		})
	}
}
