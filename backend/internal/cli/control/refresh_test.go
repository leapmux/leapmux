package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
)

// The CLI's access token lives for an hour and its refresh token for
// months, so without this the credential a login mints is usable for
// exactly one hour and then demands a browser again.

// refreshServer stands in for /auth/cli/refresh. It records every request
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
	mux.HandleFunc("/auth/cli/refresh", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		presented := r.FormValue("refresh_token")
		rs.mu.Lock()
		rs.presented = append(rs.presented, presented)
		rs.mu.Unlock()
		rs.respond(w, presented)
	})
	rs.server = httptest.NewServer(mux)
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
// the old secret is dead whatever happens next. Under the CALLER's context a
// Ctrl-C between the hub's commit and SaveCredentials left the file holding
// a secret the hub had already rotated away, and presenting it later reads
// as a reuse -- which the hub answers by REVOKING the row. A credential
// nothing was wrong with would then need a browser again.
func TestEnsureFreshBearer_CompletesARotationAfterTheCallerIsCancelled(t *testing.T) {
	var counter atomic.Int64
	release := make(chan struct{})
	arrived := make(chan struct{}, 1)
	rotate := rotatingResponder(&counter)
	rs := newRefreshServer(t, func(w http.ResponseWriter, presented string) {
		arrived <- struct{}{}
		<-release // Hold the hub's answer until the caller has gone away.
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

// TestRefresh_InvalidGrantDeletesTheCredential pins the permanent case. A
// revoked, reused, or lifetime-expired credential can never work again, so
// retrying it is pure noise; deleting it makes the next command answer
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
	assert.Equal(t, "lmx_a_refresh_0", stored.RefreshToken, "a blip must not discard the credential")
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

// TestRetryAfterUnauthenticated_RefusesWhenNothingIsRefreshable keeps the
// single reactive retry from doubling a genuinely unauthenticated caller's
// error: with no credential to renew there is nothing to retry with.
func TestRetryAfterUnauthenticated_RefusesWhenNothingIsRefreshable(t *testing.T) {
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
	assert.False(t, c.retryAfterUnauthenticated(context.Background()))
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
	mux.HandleFunc("/auth/cli/refresh", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		rotatingResponder(&counter)(w, r.FormValue("refresh_token"))
	})
	srv := httptest.NewServer(mux)
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
	mux.HandleFunc("/auth/cli/refresh", func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		rotatingResponder(&counter)(w, r.FormValue("refresh_token"))
	})
	srv := httptest.NewServer(mux)
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
