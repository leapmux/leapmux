package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
)

// The CLI's access token lives for an hour and its refresh token for
// months, so without this the credential a login mints is usable for
// exactly one hour and then demands a browser again.

// refreshServer stands in for the /oauth/token refresh grant. It records every request
// and answers from `respond`.
type refreshServer struct {
	mu        sync.Mutex
	presented []string
	respond   func(w http.ResponseWriter, presented string)
	server    *httptest.Server
}

func newRefreshServer(t *testing.T, respond func(w http.ResponseWriter, presented string)) *refreshServer {
	t.Helper()
	rs := &refreshServer{respond: respond}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		presented := r.FormValue("refresh_token")
		rs.mu.Lock()
		rs.presented = append(rs.presented, presented)
		rs.mu.Unlock()
		rs.respond(w, presented)
	})
	rs.server = hubtransporttest.NewServer(t, mux)
	t.Cleanup(rs.server.Close)
	return rs
}

func (rs *refreshServer) calls() []string {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return append([]string(nil), rs.presented...)
}

// rotatingResponder answers a valid rotation, numbering each pair so a test
// can see which one it got.
func rotatingResponder(counter *atomic.Int64) func(http.ResponseWriter, string) {
	return func(w http.ResponseWriter, _ string) {
		n := counter.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       "lmx_a_access_" + strconv.FormatInt(n, 10),
			"refresh_token":      "lmx_a_refresh_" + strconv.FormatInt(n, 10),
			"expires_in":         3600,
			"refresh_expires_in": 7776000,
			"token_id":           "tok-1",
		})
	}
}

// seedCredentials writes a credential file for the server under a per-test
// config dir, with the access token expiring at `expiresAt`.
func seedCredentials(t *testing.T, hubURL string, expiresAt time.Time) {
	t.Helper()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(hubURL, CredentialFile{
		HubURL:       hubURL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    expiresAt,
		UserID:       "usr_1",
		Username:     "alice",
	}))
}

func TestEnsureFreshBearer_LeavesAHealthyTokenAlone(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	require.NoError(t, c.EnsureFreshBearer(context.Background()))

	assert.Empty(t, rs.calls(), "a token far from expiry must cost no request")
	assert.Equal(t, "lmx_a_access_0", c.currentBearer())
}

// TestEnsureFreshBearer_AnswersAHealthyTokenFromMemory pins the cost of the
// check, not only its answer.
//
// EnsureFreshBearer runs before EVERY unary call and every stream open, and
// the credential file read is an os.ReadFile plus a JSON decode under a
// process-wide mutex. A command that makes N calls paid N of them to
// re-learn an expiry that cannot have moved. Deleting the file after the
// client is built is what makes the read observable: if the check still
// consulted it, this would fail.
func TestEnsureFreshBearer_AnswersAHealthyTokenFromMemory(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	require.NoError(t, DeleteCredentials(rs.server.URL))

	for range 5 {
		require.NoError(t, c.EnsureFreshBearer(context.Background()))
	}
	assert.Empty(t, rs.calls(), "a token far from expiry must cost no request")
	assert.Equal(t, "lmx_a_access_0", c.currentBearer(),
		"the check must answer from the expiry it already holds, not from the file")
}

// TestEnsureFreshBearer_AdoptsATokenAnotherProcessWrote is the other half of
// the same short-circuit: it may skip a READ, never a renewal.
//
// A second process rotated the credential, so this client's in-memory token
// is stale although the file's expiry is healthy. Reaching the file at all
// is what lets it adopt the new token instead of presenting the old one
// until the hub answers 401.
func TestEnsureFreshBearer_AdoptsATokenAnotherProcessWrote(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	// Another process refreshed and wrote a healthy pair.
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_other",
		RefreshToken: "lmx_a_refresh_other",
		ExpiresAt:    time.Now().Add(time.Hour),
		UserID:       "usr_1",
		Username:     "alice",
	}))

	require.NoError(t, c.EnsureFreshBearer(context.Background()))
	assert.Empty(t, rs.calls(), "the file already held a healthy token, so no rotation was needed")
	assert.Equal(t, "lmx_a_access_other", c.currentBearer(),
		"a token another process wrote must be adopted rather than left for a 401")
}

// TestEnsureFreshBearer_CompletesARotationAfterTheCallerIsCancelled is the
// rule the hub's own handleRefresh states, applied on the client side.
//
// A refresh rotates the token single-use: once the request reaches the hub
// the old secret is unusable whatever happens next. Under the CALLER's context
// a Ctrl-C between the hub's commit and SaveCredentials left the file holding
// a secret the hub already rotated away, and presenting it later reads
// as a reuse -- which the hub answers by REVOKING the row. A credential
// nothing was wrong with would then need a browser again.
func TestEnsureFreshBearer_CompletesARotationAfterTheCallerIsCancelled(t *testing.T) {
	var counter atomic.Int64
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	rotate := rotatingResponder(&counter)
	rs := newRefreshServer(t, func(w http.ResponseWriter, presented string) {
		arrived <- struct{}{}
		<-release // Hold the hub's answer until the caller is gone.
		rotate(w, presented)
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.EnsureFreshBearer(ctx) }()

	<-arrived
	cancel() // The user pressed Ctrl-C while the hub was mid-rotation.
	close(release)

	select {
	case err := <-done:
		require.NoError(t, err, "a rotation already in flight must run to completion")
	case <-time.After(10 * time.Second):
		t.Fatal("the refresh never finished")
	}

	// The rotated pair reached the disk, so the next command presents the
	// secret the hub now holds rather than the one it rotated away.
	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_refresh_1", stored.RefreshToken)
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// TestEnsureFreshBearer_DoesNotRotateTwiceForOneRenewal pins that the
// rotation advances the client's cached expiry as well as its token.
//
// The two move together in setBearer, so a second call answers from memory.
// If only the token advanced, every later call in the same command would
// still read "near expiry", hit the file, and rotate again -- and each
// rotation invalidates the previous refresh secret.
func TestEnsureFreshBearer_DoesNotRotateTwiceForOneRenewal(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	for range 5 {
		require.NoError(t, c.EnsureFreshBearer(context.Background()))
	}
	assert.Len(t, rs.calls(), 1, "one renewal, however many calls follow it")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// TestEnsureFreshBearer_RenewsInsideTheSkew pins the proactive path. The
// skew must exceed a round trip plus clock skew, or the "still valid" check
// passes and the request arrives after the token died.
func TestEnsureFreshBearer_RenewsInsideTheSkew(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	require.NoError(t, c.EnsureFreshBearer(context.Background()))

	require.Len(t, rs.calls(), 1)
	assert.Equal(t, "lmx_a_refresh_0", rs.calls()[0], "the STORED refresh token is what is presented")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer(), "the client adopts the rotated access token")

	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_access_1", stored.AccessToken)
	assert.Equal(t, "lmx_a_refresh_1", stored.RefreshToken, "the rotated refresh must be persisted")
	assert.True(t, stored.ExpiresAt.After(time.Now().Add(50*time.Minute)))
	assert.True(t, stored.RefreshExpiresAt.After(time.Now().Add(80*24*time.Hour)),
		"the deadline that sends the device back to a browser must be recorded")
}

// TestRefresh_AdoptsTheReportedScope pins that a rotation updates the stored
// grant, not only the token pair.
//
// The hub names the credential's REACHABLE scope on every rotation -- the
// stored grant narrowed to the app registration's ceiling -- so an owner
// removing a permission from the registration reaches this file on the next
// renewal. Ignoring the field kept `auth status` printing a grant the hub
// stopped honoring, which is exactly the drift a reporting surface must not
// have.
func TestRefresh_AdoptsTheReportedScope(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       "lmx_a_access_1",
			"refresh_token":      "lmx_a_refresh_1",
			"expires_in":         3600,
			"refresh_expires_in": 7776000,
			"token_id":           "tok-1",
			"scope":              "workspace:read worker:read file:read",
		})
	})
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(refreshSkew / 2),
		Scope:        "workspace:read worker:read terminal:write file:read",
	}))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	require.NoError(t, c.EnsureFreshBearer(context.Background()))

	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "workspace:read worker:read file:read", stored.Scope,
		"a narrowed grant the hub reports must replace the stored one")
}

// TestRefresh_KeepsTheStoredScopeWhenTheHubAnswersNone is the silence guard:
// an empty field is a hub that did not answer, and wiping the stored grant on
// silence would make every credential look unscoped-or-empty to `auth status`.
func TestRefresh_KeepsTheStoredScopeWhenTheHubAnswersNone(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       "lmx_a_access_1",
			"refresh_token":      "lmx_a_refresh_1",
			"expires_in":         3600,
			"refresh_expires_in": 7776000,
			"token_id":           "tok-1",
		})
	})
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(refreshSkew / 2),
		Scope:        "workspace:read",
	}))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	require.NoError(t, c.EnsureFreshBearer(context.Background()))

	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "workspace:read", stored.Scope,
		"a hub that answers no scope must not wipe the stored one")
}

// TestRefresh_InvalidGrantDeletesTheCredential pins the permanent case. A
// revoked, reused, or lifetime-expired credential can never work again, so
// retrying it achieves nothing; deleting it makes the next command answer
// ErrNotLoggedIn, whose message already specifies the remedy.
func TestRefresh_InvalidGrantDeletesTheCredential(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"token revoked"}`))
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(-time.Minute))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	err = c.EnsureFreshBearer(context.Background())
	require.ErrorIs(t, err, ErrCredentialRejected)
	assert.Contains(t, err.Error(), "token revoked", "the hub's reason must reach the operator")

	_, err = LoadCredentials(rs.server.URL)
	assert.ErrorIs(t, err, ErrNotLoggedIn)
}

// TestRefresh_TransientFailureKeepsTheCredential is the other polarity: a
// hub that is briefly unreachable must not cost the user their login. The
// stored token may still work, and the call that follows says so.
func TestRefresh_TransientFailureKeepsTheCredential(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(-time.Minute))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	err = c.EnsureFreshBearer(context.Background())
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrCredentialRejected)

	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_refresh_0", stored.RefreshToken, "a brief failure must not discard the credential")
}

// TestEnsureFreshBearer_SwallowsABriefFailureOnALiveToken is the branch the
// fatal cases must step around, and the reason each of them is keyed on a
// sentinel rather than on "the refresh failed".
//
// A proactive renewal fires refreshSkew BEFORE the expiry, so the token in
// hand still works. A hub that is briefly unreachable during that renewal
// must cost nothing: the call that follows presents the stored token and
// reports the truth, whatever it is.
func TestEnsureFreshBearer_SwallowsABriefFailureOnALiveToken(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusBadGateway)
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	assert.NoError(t, c.EnsureFreshBearer(context.Background()),
		"a token that is still alive must survive a hub that is briefly unreachable")
	assert.Len(t, rs.calls(), 1, "the renewal was attempted")
	assert.Equal(t, "lmx_a_access_0", c.currentBearer(),
		"the stored token is what the call that follows presents")

	stored, err := LoadCredentials(rs.server.URL)
	require.NoError(t, err)
	assert.Equal(t, "lmx_a_refresh_0", stored.RefreshToken, "nothing rotated, so nothing may change on disk")
}

// TestRefresh_ConcurrentCallersRotateOnce is the property singleflight plus
// the presented-token check exist for. A refresh rotates single-use, so two
// concurrent rotations would make the second look like a REUSE -- which the
// hub treats as compromise and answers by revoking the row.
func TestRefresh_ConcurrentCallersRotateOnce(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, func(w http.ResponseWriter, presented string) {
		// A second rotation of the SAME secret is exactly what must not
		// happen; answer it as the hub would, so the test fails loudly.
		if presented != "lmx_a_refresh_0" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"refresh reuse detected"}`))
			return
		}
		time.Sleep(20 * time.Millisecond)
		rotatingResponder(&counter)(w, presented)
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(-time.Minute))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = c.EnsureFreshBearer(context.Background())
		}()
	}
	wg.Wait()

	for _, e := range errs {
		assert.NoError(t, e)
	}
	assert.Len(t, rs.calls(), 1, "concurrent callers must collapse onto one rotation")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// TestRepairAfterUnauthenticated_RefusesWhenNothingIsRefreshable keeps the
// reactive repair from doubling a genuinely unauthenticated caller's error:
// with no credential to renew there is nothing to retry with.
func TestRepairAfterUnauthenticated_RefusesWhenNothingIsRefreshable(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		t.Error("a refresh must not run here")
	})
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:      rs.server.URL,
		AccessToken: "lmx_a_access_0",
		// No refresh token: an admin-issued or legacy credential.
		ExpiresAt: time.Now().Add(-time.Minute),
	}))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	assert.ErrorIs(t, c.repairAfterUnauthenticated(context.Background()), errNothingToRefresh)
	assert.Empty(t, rs.calls())
}

// TestRefresh_AdoptsAConcurrentRotationInsteadOfPresentingADeadSecret pins
// the check inside the flight: a goroutine that waited while another one
// rotated must NOT then present the rotated-out secret, which the hub reads
// as reuse.
func TestRefresh_AdoptsAConcurrentRotationInsteadOfPresentingADeadSecret(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(-time.Minute))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	// The credential each caller ALREADY read, which is what refreshBearer
	// now takes: the file is loaded once by the path that decides to
	// refresh, not again inside it.
	stale := &CredentialFile{HubURL: rs.server.URL, RefreshToken: "lmx_a_refresh_0"}
	require.NoError(t, c.refreshBearer(context.Background(), stale))
	require.Len(t, rs.calls(), 1)

	// A late caller still holding the OLD secret: no request is made, and
	// it adopts what is on disk.
	require.NoError(t, c.refreshBearer(context.Background(), stale))
	assert.Len(t, rs.calls(), 1, "a stale presenter must not re-present a rotated-out secret")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// unauthorizedThenOKHandler answers Unauthenticated the first N times and
// succeeds afterwards, recording the bearer each call presented.
type unauthorizedThenOKHandler struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	mu       sync.Mutex
	bearers  []string
	failures int
}

func (h *unauthorizedThenOKHandler) CloseChannel(
	ctx context.Context,
	req *connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	h.mu.Lock()
	h.bearers = append(h.bearers, req.Header().Get("Authorization"))
	remaining := h.failures
	if remaining > 0 {
		h.failures--
	}
	h.mu.Unlock()
	if remaining > 0 {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("token expired"))
	}
	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), nil
}

func (h *unauthorizedThenOKHandler) presented() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bearers...)
}

// TestAuthInterceptor_RetriesOnceAfterUnauthenticated pins the reactive
// half. The proactive check reads a STORED expiry, and a stored expiry can
// be wrong -- a clock that moved, a token another process rotated, a file
// written by an older build. A unary call is safe to replay because the hub
// refused the first one and acted on nothing.
func TestAuthInterceptor_RetriesOnceAfterUnauthenticated(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	// Far from expiry, so the PROACTIVE path stands down and only the
	// reactive one can be what recovers the call.
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	handler := &unauthorizedThenOKHandler{failures: 1}
	mux := http.NewServeMux()
	path, h := leapmuxv1connect.NewChannelServiceHandler(handler)
	mux.Handle(path, h)
	// Serve the refresh endpoint from the SAME origin the credential specifies.
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		rotatingResponder(&counter)(w, r.FormValue("refresh_token"))
	})
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(srv.URL, CredentialFile{
		HubURL:       srv.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(time.Hour),
		UserID:       "usr_1",
	}))

	c, err := NewClient(srv.URL)
	require.NoError(t, err)
	_, err = c.ChannelService().CloseChannel(context.Background(),
		connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: "ch-1"}))
	require.NoError(t, err, "one 401 must be recovered by a single refresh and retry")

	presented := handler.presented()
	require.Len(t, presented, 2, "exactly one retry, never a loop")
	assert.Equal(t, "Bearer lmx_a_access_0", presented[0])
	assert.Equal(t, "Bearer lmx_a_access_1", presented[1], "the retry must carry the ROTATED token")
}

// TestAuthInterceptor_DoesNotRetryTwice keeps the single retry single: a
// credential the hub keeps refusing must surface the hub's own error rather
// than spinning.
func TestAuthInterceptor_DoesNotRetryTwice(t *testing.T) {
	var counter atomic.Int64
	handler := &unauthorizedThenOKHandler{failures: 10}
	mux := http.NewServeMux()
	path, h := leapmuxv1connect.NewChannelServiceHandler(handler)
	mux.Handle(path, h)
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		rotatingResponder(&counter)(w, r.FormValue("refresh_token"))
	})
	srv := hubtransporttest.NewServer(t, mux)
	t.Cleanup(srv.Close)

	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	require.NoError(t, SaveCredentials(srv.URL, CredentialFile{
		HubURL:       srv.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	c, err := NewClient(srv.URL)
	require.NoError(t, err)
	_, err = c.ChannelService().CloseChannel(context.Background(),
		connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: "ch-1"}))
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err), "the hub's own answer must survive")
	assert.Len(t, handler.presented(), 2, "one attempt plus one retry, and no more")
}

// --- the interceptor's repair loop ------------------------------------

// scriptedChannelHandler answers CloseChannel from a queue and records the
// bearer each attempt presented. An exhausted queue answers success.
type scriptedChannelHandler struct {
	leapmuxv1connect.UnimplementedChannelServiceHandler
	mu      sync.Mutex
	bearers []string
	answers []error
}

func (h *scriptedChannelHandler) CloseChannel(
	_ context.Context,
	req *connect.Request[leapmuxv1.CloseChannelRequest],
) (*connect.Response[leapmuxv1.CloseChannelResponse], error) {
	h.mu.Lock()
	h.bearers = append(h.bearers, req.Header().Get("Authorization"))
	var answer error
	if len(h.answers) > 0 {
		answer, h.answers = h.answers[0], h.answers[1:]
	}
	h.mu.Unlock()
	return connect.NewResponse(&leapmuxv1.CloseChannelResponse{}), answer
}

func (h *scriptedChannelHandler) presented() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.bearers...)
}

// elevationRequired is the hub's marked refusal: the code alone means half a
// dozen things a step-up cannot fix, so the CLI keys on the marker.
func elevationRequired() error {
	err := connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this action needs a recent sign-in: verify your identity and try again"))
	err.Meta().Set(ElevationRequiredHeader, "1")
	return err
}

// repairHub serves everything one restricted unary call can touch: the RPC, the
// refresh leg, and the step-up ceremony.
type repairHub struct {
	rpc       *scriptedChannelHandler
	elevation *elevationHub
	rotations *atomic.Int64
	server    *httptest.Server
}

// newRepairHub mounts them on one origin, which is what the credential file
// specifies. expiresIn is what each rotation reports; a short one makes the
// token lapse while a repair blocks on a person.
func newRepairHub(t *testing.T, expiresIn int, answers ...error) *repairHub {
	t.Helper()
	h := &repairHub{
		rpc:       &scriptedChannelHandler{answers: answers},
		elevation: newElevationRoutes(),
		rotations: &atomic.Int64{},
	}
	mux := http.NewServeMux()
	path, handler := leapmuxv1connect.NewChannelServiceHandler(h.rpc)
	mux.Handle(path, handler)
	// ONE token endpoint for both grants, routed by grant_type, because that
	// is what the hub serves: the refresh leg and the device-code poll no
	// longer have addresses of their own.
	mux.HandleFunc("/oauth/step-up", h.elevation.startLeg())
	mux.HandleFunc("/oauth/token", h.elevation.tokenEndpoint(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		n := h.rotations.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":       "lmx_a_access_" + strconv.FormatInt(n, 10),
			"refresh_token":      "lmx_a_refresh_" + strconv.FormatInt(n, 10),
			"expires_in":         expiresIn,
			"refresh_expires_in": 7776000,
			"token_id":           "tok-1",
		})
	}))
	h.server = hubtransporttest.NewServer(t, mux)
	t.Cleanup(h.server.Close)
	return h
}

// call runs one CloseChannel through the interceptor.
func (h *repairHub) call(c *Client) error {
	_, err := c.ChannelService().CloseChannel(context.Background(),
		connect.NewRequest(&leapmuxv1.CloseChannelRequest{ChannelId: "ch-1"}))
	return err
}

// TestAuthInterceptor_RepairsBothRefusalsInOneCall is why the repairs are a
// loop over a table rather than two hand-written sequences.
//
// One call can need BOTH. Another process rotates the credential, so the hub
// answers Unauthenticated; the replay then reaches the hub with a live
// credential that proved no factor, which is the marked refusal. The
// hand-written elevation branch returned its replay DIRECTLY, so the second
// refusal was reported raw and the user never saw a prompt.
func TestAuthInterceptor_RepairsBothRefusalsInOneCall(t *testing.T) {
	hub := newRepairHub(t, 3600,
		connect.NewError(connect.CodeUnauthenticated, errors.New("token expired")),
		elevationRequired(),
	)
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	require.NoError(t, hub.call(c), "a call that needs both repairs must still land")

	assert.Len(t, hub.rpc.presented(), 3, "one attempt for each refusal, and one that succeeds")
	starts, _ := hub.elevation.counts()
	assert.Equal(t, 1, starts, "the marked refusal must open the step-up, whatever ran before it")
}

// TestAuthInterceptor_RunsEachRepairAtMostOnce keeps the loop from asking a
// user to verify the same credential for ever. A second refusal of the same
// kind is the truth: the factor was proven and the action is still refused.
func TestAuthInterceptor_RunsEachRepairAtMostOnce(t *testing.T) {
	hub := newRepairHub(t, 3600, elevationRequired(), elevationRequired(), elevationRequired())
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	err = hub.call(c)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err), "the hub's own answer must survive")
	assert.Len(t, hub.rpc.presented(), 2, "one attempt plus one replay, and no more")
	starts, _ := hub.elevation.counts()
	assert.Equal(t, 1, starts, "one ceremony for one call")
}

// TestAuthInterceptor_ReplaysWithATokenRenewedAfterThePrompt is the reason
// the freshness check runs before EVERY attempt.
//
// The step-up blocks on a person for up to ten minutes and the access token
// lives for one hour, so the token a call started with can be dead by the
// time the person finishes. Stamping the replay from the header alone made
// the command fail as unauthenticated, and told a user who just finished
// verifying that they were not signed in.
func TestAuthInterceptor_ReplaysWithATokenRenewedAfterThePrompt(t *testing.T) {
	// One second of life per rotation, so the token the first attempt
	// presents is inside the renewal window again by the replay.
	hub := newRepairHub(t, 1, elevationRequired())
	seedCredentials(t, hub.server.URL, time.Now().Add(refreshSkew/2))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	require.NoError(t, hub.call(c))

	presented := hub.rpc.presented()
	require.Len(t, presented, 2)
	assert.Equal(t, "Bearer lmx_a_access_1", presented[0])
	assert.Equal(t, "Bearer lmx_a_access_2", presented[1],
		"the replay must carry a token renewed AFTER the prompt, not the one from before it")
}

// TestAuthInterceptor_ReportsTheHubsRefusalWhenNobodyCanVerify is the
// headless case at the call site. The hub's own refusal states what the
// command needs; reporting the ceremony's failure instead would state the
// remedy as the problem.
func TestAuthInterceptor_ReportsTheHubsRefusalWhenNobodyCanVerify(t *testing.T) {
	hub := newRepairHub(t, 3600, elevationRequired())
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	out, errOut := captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = false

	err = hub.call(c)
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Contains(t, err.Error(), "verify your identity", "the hub's message is what the user must read")
	assert.Len(t, hub.rpc.presented(), 1, "no replay, because nothing repaired the refusal")
	starts, _ := hub.elevation.counts()
	assert.Zero(t, starts, "a ceremony nobody can finish must never be opened")
	assert.Empty(t, out.String())
	assert.Empty(t, errOut.String())
}

// TestAuthInterceptor_LeavesAnUnrepairableRefusalAlone: a code with no
// repair in the table is reported as the hub sent it, with no replay.
func TestAuthInterceptor_LeavesAnUnrepairableRefusalAlone(t *testing.T) {
	hub := newRepairHub(t, 3600,
		connect.NewError(connect.CodePermissionDenied, errors.New("administrator privileges are required")))
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	err = hub.call(c)
	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	assert.Len(t, hub.rpc.presented(), 1)
	assert.Zero(t, hub.rotations.Load(), "an unrepairable refusal must not rotate the credential")
}

// TestAuthInterceptor_LeavesAnUnmarkedFailedPreconditionAlone: the marker,
// never the code. A FailedPrecondition also means "this account has no
// password", and stopping a script to print a URL for one of those would be
// worse than reporting it.
func TestAuthInterceptor_LeavesAnUnmarkedFailedPreconditionAlone(t *testing.T) {
	hub := newRepairHub(t, 3600,
		connect.NewError(connect.CodeFailedPrecondition, errors.New("this account has no password")))
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	require.Error(t, hub.call(c))
	assert.Len(t, hub.rpc.presented(), 1)
	starts, _ := hub.elevation.counts()
	assert.Zero(t, starts)
}

// TestAuthInterceptor_RepairsAnExpiredTokenAfterTheStepUp is the OTHER
// order, and the one the fixed sequence could never reach.
//
// The step-up blocks on a person for up to ten minutes, and another process
// on the same credential file rotates inside that window -- so the replay
// that follows the ceremony reaches the hub with a token the hub retired,
// and the hub answers Unauthenticated. The hand-written elevation branch
// RETURNED that replay directly, so the user who just finished verifying
// read "unauthenticated" and the call died. The loop repairs it and lands
// the call.
func TestAuthInterceptor_RepairsAnExpiredTokenAfterTheStepUp(t *testing.T) {
	hub := newRepairHub(t, 3600,
		elevationRequired(),
		connect.NewError(connect.CodeUnauthenticated, errors.New("token expired")),
	)
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	out, errOut := captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	require.NoError(t, hub.call(c), "a call refused in this order must still land")

	assert.Equal(t,
		[]string{"Bearer lmx_a_access_0", "Bearer lmx_a_access_0", "Bearer lmx_a_access_1"},
		hub.rpc.presented(),
		"the third attempt must carry the pair the rotation adopted")
	starts, _ := hub.elevation.counts()
	assert.Equal(t, 1, starts)
	assert.Equal(t, int64(1), hub.rotations.Load(), "the refusal after the ceremony must rotate the credential")

	// The JSON contract, under the interceptor that interrupts an ordinary
	// verb: the prose reaches Err alone, so the verb's own envelope is the
	// whole of Out and `... | jq` still parses it.
	assert.Empty(t, out.String())
	assert.Contains(t, errOut.String(), "verify your identity")
}

// TestAuthInterceptor_StopsWhenEveryRepairIsUsed is the loop's termination.
//
// Each repair runs at most once, so a hub that keeps refusing ends the loop
// rather than asking a person to verify the same credential for ever. Two
// repairs allow at most two replays, whatever order the refusals arrive in.
func TestAuthInterceptor_StopsWhenEveryRepairIsUsed(t *testing.T) {
	hub := newRepairHub(t, 3600,
		connect.NewError(connect.CodeUnauthenticated, errors.New("token expired")),
		elevationRequired(),
		connect.NewError(connect.CodeUnauthenticated, errors.New("token expired")),
	)
	seedCredentials(t, hub.server.URL, time.Now().Add(time.Hour))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)
	c.promptAllowed = true

	err = hub.call(c)
	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err),
		"the last refusal the hub sent is what the caller reads")
	assert.Len(t, hub.rpc.presented(), 3, "one attempt plus one replay for each repair, and no more")
	starts, _ := hub.elevation.counts()
	assert.Equal(t, 1, starts, "one ceremony for one call")
	assert.Equal(t, int64(1), hub.rotations.Load(), "one rotation for one call")
}

// --- the credential file the rotation could not write -----------------

// blockCredentialSaves makes the next SaveCredentials for hubURL fail,
// after LoadCredentials has already succeeded.
//
// chmod 0500 on the config directory is the Unix form: the existing
// file stays readable (owner execute on the directory is enough) and
// CreateTemp cannot create a sibling. Windows ignores a directory's
// read-only bit for child creates, so the same chmod lets the save
// succeed and swallows the rotation. A read-only credential FILE is
// the Windows form: ReadFile still works and the replace cannot.
func blockCredentialSaves(t *testing.T, hubURL string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		path, err := CredentialsPath(hubURL)
		require.NoError(t, err)
		require.NoError(t, os.Chmod(path, 0o400))
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		return
	}
	dir, err := ConfigDir()
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
}

// TestRefresh_AdoptsThePairItCouldNotSave is the failure that used to
// destroy a credential.
//
// The hub rotated both hashes before it answered, so the old access token is
// already dead. Discarding the fresh pair because the FILE could not be
// written left the process presenting a token the hub retired, and left
// the file holding the refresh secret the hub rotated away -- which the hub
// reads as a reuse past its grace window, and answers by REVOKING the row.
func TestRefresh_AdoptsThePairItCouldNotSave(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		ExpiresAt:    time.Now().Add(refreshSkew / 2),
	}))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	blockCredentialSaves(t, rs.server.URL)

	err = c.EnsureFreshBearer(context.Background())
	require.ErrorIs(t, err, ErrCredentialsNotSaved,
		"a rotation that could not be saved must be reported, not swallowed")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer(),
		"the pair the hub committed to is the only live one, so the process must adopt it")
	assert.Len(t, rs.calls(), 1)
}

// TestAuthInterceptor_ReportsALocalDiskFailureAsSuchNotAsASignIn wires
// bearerErrorCode into the call that reads it.
//
// The pre-call renewal reached the hub and could not write the pair to disk.
// Every other pre-call failure means "this call cannot authenticate", so the
// interceptor reported Unauthenticated for all of them -- which sent an
// operator with a full disk to `leapmux control auth login`, where the
// login writes to the same directory and fails the same way.
func TestAuthInterceptor_ReportsALocalDiskFailureAsSuchNotAsASignIn(t *testing.T) {
	hub := newRepairHub(t, 3600)
	dir := t.TempDir()
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", dir)
	require.NoError(t, SaveCredentials(hub.server.URL, CredentialFile{
		HubURL:       hub.server.URL,
		AccessToken:  "lmx_a_access_0",
		RefreshToken: "lmx_a_refresh_0",
		// Inside the skew, so the call renews before it dials.
		ExpiresAt: time.Now().Add(refreshSkew / 2),
	}))
	captureStreams(t)

	c, err := NewClient(hub.server.URL)
	require.NoError(t, err)

	blockCredentialSaves(t, hub.server.URL)

	err = hub.call(c)
	require.Error(t, err)
	assert.Equal(t, connect.CodeInternal, connect.CodeOf(err),
		"a disk that cannot be written is not a credential that cannot sign in")
	assert.ErrorIs(t, err, ErrCredentialsNotSaved, "the reason must reach the operator")
	assert.Empty(t, hub.rpc.presented(),
		"a rotation the file cannot record must stop the call, not ride it")
}

// TestBearerErrorCode_SeparatesTheFaultFromTheCredential: the code the
// caller reads must not send an operator to the login when the fault is a
// full disk -- and must not read a network failure as a revoked credential,
// which hubCheck would report as a signed-out state for a credential that
// was fine.
func TestBearerErrorCode_SeparatesTheFaultFromTheCredential(t *testing.T) {
	assert.Equal(t, connect.CodeInternal, bearerErrorCode(
		fmt.Errorf("%w: no space left on device", ErrCredentialsNotSaved)))
	assert.Equal(t, connect.CodeUnauthenticated, bearerErrorCode(ErrCredentialRejected))
	assert.Equal(t, connect.CodeUnavailable, bearerErrorCode(errors.New("dial tcp: refused")))
}

// --- the reactive repair ----------------------------------------------

// TestRepairAfterUnauthenticated_AdoptsInsteadOfRotating is what keeps two
// long-lived processes on one credential file from rotating on nearly every
// call, each one retiring the token the other just adopted.
//
// The 401 came from a token another process replaced, so the token on disk
// IS the repair. Rotating instead spends a single-use secret to learn what
// the file already said.
func TestRepairAfterUnauthenticated_AdoptsInsteadOfRotating(t *testing.T) {
	rs := newRefreshServer(t, func(w http.ResponseWriter, _ string) {
		t.Error("a rotation must not run when the file already holds a live token")
	})
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	// Another process rotated and wrote the pair it received.
	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_other",
		RefreshToken: "lmx_a_refresh_other",
		ExpiresAt:    time.Now().Add(time.Hour),
	}))

	require.NoError(t, c.repairAfterUnauthenticated(context.Background()))
	assert.Equal(t, "lmx_a_access_other", c.currentBearer())
	assert.Empty(t, rs.calls(), "the stored token was the repair; no rotation was needed")
}

// TestRepairAfterUnauthenticated_RotatesWhenTheStoredTokenIsTheRejectedOne
// is the other polarity: when the file holds the very token the hub just
// refused, only a rotation can produce a live one.
func TestRepairAfterUnauthenticated_RotatesWhenTheStoredTokenIsTheRejectedOne(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	require.NoError(t, c.repairAfterUnauthenticated(context.Background()))
	assert.Len(t, rs.calls(), 1)
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// TestRepairAfterUnauthenticated_RotatesWhenTheStoredTokenIsAlsoStale keeps
// the adopt path from presenting a token that dies in the next second: the
// bar is the same refreshSkew that decides to renew.
func TestRepairAfterUnauthenticated_RotatesWhenTheStoredTokenIsAlsoStale(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(time.Hour))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)

	require.NoError(t, SaveCredentials(rs.server.URL, CredentialFile{
		HubURL:       rs.server.URL,
		AccessToken:  "lmx_a_access_nearly_dead",
		RefreshToken: "lmx_a_refresh_other",
		ExpiresAt:    time.Now().Add(refreshSkew / 2),
	}))

	require.NoError(t, c.repairAfterUnauthenticated(context.Background()))
	assert.Len(t, rs.calls(), 1, "a stored token this client would renew at once is not worth adopting")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// TestRefresh_ReusesTheClientsOwnTransport pins where the rotation's HTTP
// client comes from.
//
// Building one per rotation allocated an http.Transport for a `unix:` or
// `npipe:` hub whose IdleConnTimeout is zero and that nothing closes, so a
// long-running `control events` leaked one idle connection and its read
// goroutine every hour. Swapping the client's transport is what makes the
// choice observable.
func TestRefresh_ReusesTheClientsOwnTransport(t *testing.T) {
	var counter atomic.Int64
	rs := newRefreshServer(t, rotatingResponder(&counter))
	seedCredentials(t, rs.server.URL, time.Now().Add(refreshSkew/2))

	c, err := NewClient(rs.server.URL)
	require.NoError(t, err)
	counting := &countingTransport{next: c.HTTPClient.Transport}
	c.HTTPClient = &http.Client{Transport: counting, Timeout: c.HTTPClient.Timeout}

	require.NoError(t, c.EnsureFreshBearer(context.Background()))
	assert.Equal(t, int64(1), counting.calls.Load(),
		"the rotation must ride the transport the client already holds")
	assert.Equal(t, "lmx_a_access_1", c.currentBearer())
}

// countingTransport counts the requests that pass through it.
type countingTransport struct {
	next  http.RoundTripper
	calls atomic.Int64
}

func (t *countingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	next := t.next
	if next == nil {
		next = http.DefaultTransport
	}
	return next.RoundTrip(req)
}
