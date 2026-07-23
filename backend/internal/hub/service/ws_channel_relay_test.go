package service

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/userid"

	"github.com/coder/websocket"
	"github.com/leapmux/leapmux/channelwire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/channelmgr"
	"github.com/leapmux/leapmux/internal/hub/store"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/util/id"
)

func newTestAuthContexts(t *testing.T) *auth.AuthContextRegistry {
	t.Helper()
	_, registry := auth.NewInterceptor(nil, nil, false, false)
	t.Cleanup(registry.Stop)
	return registry
}

func TestWebSocketHandlersRequireAuthContextRegistry(t *testing.T) {
	assert.Panics(t, func() { NewChannelRelayHandler(nil, newTestRegistry(), nil, nil, nil, false) })
	assert.Panics(t, func() { NewOrgEventsHandler(nil, nil, nil, nil, false) })
}

// TestChannelRelayHandlerRequiresWorkerRegistry pins the OTHER dependency the
// relay constructor refuses. A nil *workermgr.Manager is not catchable by the
// close dispatcher that receives it -- narrowed to a one-method interface, a
// nil pointer becomes a NON-nil interface value -- so without this guard the
// first channel teardown panics on a nil receiver, on the caller's goroutine,
// long after startup. Several tests in this file used to pass nil here.
func TestChannelRelayHandlerRequiresWorkerRegistry(t *testing.T) {
	assert.Panics(t, func() { NewChannelRelayHandler(nil, nil, nil, newTestAuthContexts(t), nil, false) })
}

// newTestRegistry is a real registry with no user-directed reach, for tests
// that exercise the relay's auth paths rather than worker delivery.
func newTestRegistry() *workermgr.Manager {
	return workermgr.New(workermgr.DenyAllReach())
}

// TestWSReadLimit_AcceptsLargeChunk verifies that a WebSocket connection with
// SetReadLimit(WSReadLimit) accepts messages up to the maximum chunk size
// (maxChunkCiphertext + framing overhead), which would fail with the library's
// default 32KB limit.
func TestWSReadLimit_AcceptsLargeChunk(t *testing.T) {
	// Build a ChannelMessage whose wire format (4-byte length prefix +
	// protobuf) exceeds the default 32KB WebSocket read limit.
	// Use 64KB of ciphertext — the maximum single Noise transport message.
	ciphertext := []byte(strings.Repeat("X", 65535))
	msg := &leapmuxv1.ChannelMessage{
		ChannelId:     "test-channel",
		CorrelationId: 1,
		Ciphertext:    ciphertext,
	}

	data, err := proto.Marshal(msg)
	require.NoError(t, err)

	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)

	// Sanity: the frame must exceed the default 32KB read limit.
	require.Greater(t, len(frame), 32768, "test frame must exceed default WS read limit")

	// Start a test server that reads one message using the relay's read limit.
	received := make(chan *leapmuxv1.ChannelMessage, 1)
	readErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			readErr <- err
			return
		}
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

		ws.SetReadLimit(channelwire.WSReadLimit)

		got, rerr := channelwire.ReadChannelMessage(r.Context(), ws)
		if rerr != nil {
			readErr <- rerr
			return
		}
		received <- got
	}))
	defer srv.Close()

	// Connect and send the large message.
	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

	// Client also needs a higher write limit for large messages.
	err = ws.Write(ctx, websocket.MessageBinary, frame)
	require.NoError(t, err)

	select {
	case got := <-received:
		assert.Equal(t, msg.GetChannelId(), got.GetChannelId())
		assert.Equal(t, msg.GetCorrelationId(), got.GetCorrelationId())
		assert.Equal(t, len(msg.GetCiphertext()), len(got.GetCiphertext()))
	case rerr := <-readErr:
		t.Fatalf("server read failed: %v", rerr)
	}
}

func TestChannelRelay_NoCookie_Returns401(t *testing.T) {
	handler := NewChannelRelayHandler(nil, newTestRegistry(), nil, newTestAuthContexts(t), nil, false)

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, "unauthorized\n", rec.Body.String())
}

type httpAuthFailureStore struct{ store.Store }

func (httpAuthFailureStore) Sessions() store.SessionStore {
	return httpAuthFailureSessions{}
}

type httpAuthFailureSessions struct{ store.SessionStore }

func (httpAuthFailureSessions) ValidateWithUser(context.Context, string) (*store.SessionWithUser, error) {
	return nil, errors.New("sensitive database failure")
}

func TestWebSocketHandlers_InternalAuthFailureReturnsGeneric500(t *testing.T) {
	handlers := map[string]http.Handler{
		"channel relay": NewChannelRelayHandler(httpAuthFailureStore{}, newTestRegistry(), nil, newTestAuthContexts(t), nil, false),
		"org events":    NewOrgEventsHandler(httpAuthFailureStore{}, nil, newTestAuthContexts(t), nil, false),
	}
	for name, handler := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/ws", nil)
			req.AddCookie(auth.BuildSessionCookie("session", time.Now().Add(time.Hour), false))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusInternalServerError, rec.Code)
			assert.Equal(t, "internal server error\n", rec.Body.String())
		})
	}
}

func TestChannelRelay_SubprotocolToken_NotAccepted(t *testing.T) {
	handler := NewChannelRelayHandler(nil, newTestRegistry(), nil, newTestAuthContexts(t), nil, false)

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "channel-relay, auth.token.some-token")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Should still fail — subprotocol auth is no longer accepted.
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

// TestWSReadLimit_DefaultRejectsLargeChunk verifies that without SetReadLimit,
// the default 32KB limit causes a read failure for large messages. This
// confirms the fix is actually necessary.
func TestWSReadLimit_DefaultRejectsLargeChunk(t *testing.T) {
	ciphertext := []byte(strings.Repeat("X", 65535))
	msg := &leapmuxv1.ChannelMessage{
		ChannelId:     "test-channel",
		CorrelationId: 1,
		Ciphertext:    ciphertext,
	}

	data, err := proto.Marshal(msg)
	require.NoError(t, err)

	frame := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(data)))
	copy(frame[4:], data)

	// Server does NOT call SetReadLimit — uses library default (32KB).
	readErr := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			readErr <- err
			return
		}
		defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

		// No SetReadLimit — default applies.
		_, rerr := channelwire.ReadChannelMessage(r.Context(), ws)
		readErr <- rerr
	}))
	defer srv.Close()

	ctx := context.Background()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	ws, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(t, err)
	defer func() { _ = ws.Close(websocket.StatusNormalClosure, "") }()

	err = ws.Write(ctx, websocket.MessageBinary, frame)
	require.NoError(t, err)

	rerr := <-readErr
	require.Error(t, rerr, "default read limit should reject large messages")
}

// --- Bearer authentication on the WebSocket upgrade ---
//
// The relay accepts three auth modes: solo, cookie, Bearer. The cookie
// path is exercised indirectly by every workspace test in this package
// already; these tests pin down the Bearer path that the leapmux remote
// CLI relies on. Solo mode is covered by the smoke test below to keep
// the matrix complete in one place.

// newBearerRelay returns a ChannelRelayHandler with a real
// TokenValidator wired in, plus the store handle the test uses to
// seed api_token rows directly. The relay's ServeHTTP exits early
// before BindUser when authentication fails, so the workermgr/
// channelmgr arguments stay nil — they're never reached on the
// rejection paths.
func newBearerRelay(t *testing.T) (*ChannelRelayHandler, store.Store, *auth.TokenValidator) {
	t.Helper()
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	h := NewChannelRelayHandler(st, newTestRegistry(), nil, newTestAuthContexts(t), nil, false).WithTokenValidator(tv)
	return h, st, tv
}

func mintAdminAPIToken(t *testing.T, st store.Store, tv *auth.TokenValidator) string {
	t.Helper()
	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(u.ID), ClientType: "cli", ClientName: "test",
		SecretHash: tv.HashSecret(secret), Scope: "remote:*",
	}))
	return auth.FormatBearer(auth.BearerKindAPI, tokenID, secret)
}

// Calling /ws/channel without WebSocket-upgrade headers makes the
// upgrade fail at the http layer. We assert just the auth gate by
// hitting the handler with an httptest.NewRecorder, which lets us
// observe the auth error before the upgrade attempt.
func TestChannelRelay_Bearer_RejectsUnknownToken(t *testing.T) {
	h, _, _ := newBearerRelay(t)
	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	req.Header.Set("Authorization", "Bearer lmx_unknown_id_unknown_secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelRelay_Bearer_RejectsRevokedToken(t *testing.T) {
	h, st, tv := newBearerRelay(t)
	bearer := mintAdminAPIToken(t, st, tv)
	// Pull the token id out of "lmx_<kind><id>_<secret>" — strip
	// the prefix, drop the kind char, then take everything before
	// the next underscore.
	rest := strings.TrimPrefix(bearer, "lmx_")
	rest = rest[1:] // skip kind char
	tokenID := rest[:strings.Index(rest, "_")]
	_, err := st.APITokens().Revoke(context.Background(), tokenID)
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelRelay_Bearer_RejectsExpiredToken(t *testing.T) {
	h, st, tv := newBearerRelay(t)
	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	past := time.Now().Add(-time.Minute)
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: userid.MustNew(u.ID), ClientType: "cli", ClientName: "test",
		SecretHash: tv.HashSecret(secret), ExpiresAt: &past, Scope: "remote:*",
	}))

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	req.Header.Set("Authorization", "Bearer "+auth.FormatBearer(auth.BearerKindAPI, tokenID, secret))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelRelay_Bearer_RejectsMalformedBearer(t *testing.T) {
	h, _, _ := newBearerRelay(t)
	for _, header := range []string{
		"Bearer lmx_no_underscore_in_secret",
		"Bearer lmx__missing_id",
		"Bearer not-a-bearer",
		"Token lmx_some_thing", // wrong scheme
	} {
		req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "header=%q should be rejected", header)
	}
}

func TestChannelRelay_Bearer_RejectsWhenValidatorNotWired(t *testing.T) {
	// Without WithTokenValidator, the relay must reject lmx_* bearers
	// rather than fall through to the cookie path or panic. This
	// matches the deployed shape where bearer support is a deliberate
	// opt-in for the multi-user-hub.
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	h := NewChannelRelayHandler(st, newTestRegistry(), nil, newTestAuthContexts(t), nil, false)

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	req.Header.Set("Authorization", "Bearer lmx_anything_anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelRelay_Bearer_AcceptsValidToken(t *testing.T) {
	// Spin up a real httptest.Server with the relay so the WebSocket
	// upgrade can complete and BindUser actually runs. Wire real
	// (empty) channelmgr/workermgr instances so we never trip a nil
	// pointer panic on the success path.
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	cm := channelmgr.New()
	wm := workermgr.New(workermgr.DenyAllReach())
	h := NewChannelRelayHandler(st, wm, cm, newTestAuthContexts(t), nil, false).WithTokenValidator(tv)

	bearer := mintAdminAPIToken(t, st, tv)

	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/channel"
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+bearer)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err, "valid bearer must complete the WebSocket upgrade")
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
}

func TestChannelRelay_BearerRevocationClosesLiveConnection(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	hubtestutil.CreateTestAdmin(t, st)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)
	_, cache := auth.NewInterceptorWithTokens(st, nil, tv, false, false)
	t.Cleanup(cache.Stop)

	cm := channelmgr.New()
	wm := workermgr.New(workermgr.DenyAllReach())
	handler := NewChannelRelayHandler(st, wm, cm, cache, nil, false).
		WithTokenValidator(tv)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	bearer := mintAdminAPIToken(t, st, tv)
	kind, tokenID, _, err := auth.ParseBearer(bearer)
	require.NoError(t, err)
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+bearer)
	conn, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/channel", &websocket.DialOptions{
		HTTPHeader: hdr,
	})
	require.NoError(t, err)
	defer func() { _ = conn.CloseNow() }()

	cache.EvictBearer(auth.NewBearerRef(kind, tokenID))
	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, _, err = conn.Read(readCtx)
	require.Error(t, err, "revoking the authenticating bearer must close the upgraded relay")
	require.NotErrorIs(t, err, context.DeadlineExceeded,
		"the relay remained open after its authenticated lease was cancelled")
}

func TestChannelRelay_DelegationCannotAttachUnscopedChannel(t *testing.T) {
	st := hubtestutil.OpenTestStore(t)
	tv, err := auth.NewTokenValidator(st, []byte("0123456789abcdef0123456789abcdef"))
	require.NoError(t, err)

	orgID := id.Generate()
	require.NoError(t, st.Orgs().Create(context.Background(), store.CreateOrgParams{
		ID: orgID, Name: "relay-delegation-org",
	}))
	userID := id.Generate()
	require.NoError(t, st.Users().Create(context.Background(), store.CreateUserParams{
		ID: userID, OrgID: orgID, Username: "relay-user-" + id.Generate()[:6],
	}))
	workspaceID := id.Generate()
	require.NoError(t, st.Workspaces().Create(context.Background(), store.CreateWorkspaceParams{
		ID: workspaceID, OrgID: orgID, OwnerUserID: userid.MustNew(userID), Title: "relay-ws",
	}))
	workerID := id.Generate()
	require.NoError(t, st.Workers().Create(context.Background(), store.CreateWorkerParams{
		ID:              workerID,
		AuthToken:       id.Generate(),
		RegisteredBy:    userid.MustNew(userID),
		PublicKey:       []byte("test-x25519-key-32-bytes-padding"),
		MlkemPublicKey:  []byte("mlkem"),
		SlhdsaPublicKey: []byte("slhdsa"),
	}))

	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.DelegationTokens().Create(context.Background(), store.CreateDelegationTokenParams{
		ID:               tokenID,
		UserID:           userid.MustNew(userID),
		WorkerID:         workerID,
		WorkspaceID:      workspaceID,
		IssuedForTabID:   "tab-1",
		IssuedForTabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
		SecretHash:       tv.HashSecret(secret),
		ExpiresAt:        time.Now().Add(time.Hour),
	}))
	bearer := auth.FormatBearer(auth.BearerKindDelegation, tokenID, secret)

	cm := channelmgr.New()
	wm := workermgr.New(workermgr.DenyAllReach())
	unscopedChannelID := id.Generate()
	scopedChannelID := id.Generate()
	cm.RegisterWithAuthInfo(unscopedChannelID, workerID, userID, channelmgr.AuthInfo{}, nil)
	cm.RegisterWithAuthInfo(scopedChannelID, workerID, userID, channelmgr.AuthInfo{
		Credential: auth.DelegationCredential(tokenID, workspaceID, workerID),
	}, nil)

	workerMsgs := make(chan *leapmuxv1.ConnectResponse, 4)
	_, _ = wm.Register(&workermgr.Conn{
		WorkerID: workerID,
		SendFn: func(msg *leapmuxv1.ConnectResponse) error {
			workerMsgs <- msg
			return nil
		},
	})

	srv := httptest.NewServer(NewChannelRelayHandler(st, wm, cm, newTestAuthContexts(t), nil, false).WithTokenValidator(tv))
	t.Cleanup(srv.Close)

	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/channel"
	hdr := http.Header{}
	hdr.Set("Authorization", "Bearer "+bearer)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: hdr})
	require.NoError(t, err)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	require.NoError(t, channelwire.WriteChannelMessage(ctx, conn, &leapmuxv1.ChannelMessage{
		ChannelId:     unscopedChannelID,
		CorrelationId: 1,
		Ciphertext:    []byte("blocked"),
	}))
	select {
	case msg := <-workerMsgs:
		require.Failf(t, "delegation relay must not forward unscoped channel", "got %v", msg)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(t, channelwire.WriteChannelMessage(ctx, conn, &leapmuxv1.ChannelMessage{
		ChannelId:     scopedChannelID,
		CorrelationId: 2,
		Ciphertext:    []byte("allowed"),
	}))
	select {
	case msg := <-workerMsgs:
		got := msg.GetChannelMessage()
		require.NotNil(t, got)
		assert.Equal(t, scopedChannelID, got.GetChannelId())
		assert.Equal(t, uint64(2), got.GetCorrelationId())
	case <-time.After(time.Second):
		require.Fail(t, "expected scoped delegation channel message to reach worker")
	}
}

// relayFrontendMessageToWorker owns the four teardown/forward decisions the read
// loop reacts to: a channel with no worker is a no-op; an offline worker or a
// broken worker stream is terminal (the read loop then closes the channel); a
// live worker receives the ciphertext wrapped verbatim.
func TestRelayFrontendMessageToWorker(t *testing.T) {
	msg := func(channelID string) *leapmuxv1.ChannelMessage {
		return &leapmuxv1.ChannelMessage{ChannelId: channelID, CorrelationId: 1, Ciphertext: []byte("ct")}
	}

	t.Run("empty worker id is a no-op", func(t *testing.T) {
		h := &ChannelRelayHandler{workerMgr: workermgr.New(workermgr.DenyAllReach()), channelMgr: channelmgr.New()}
		err := h.relayFrontendMessageToWorker(channelmgr.ChannelInfo{ChannelID: "ch"}, msg("ch"))
		require.NoError(t, err)
	})

	t.Run("offline worker is terminal", func(t *testing.T) {
		h := &ChannelRelayHandler{workerMgr: workermgr.New(workermgr.DenyAllReach()), channelMgr: channelmgr.New()}
		err := h.relayFrontendMessageToWorker(
			channelmgr.ChannelInfo{ChannelID: "ch", WorkerID: "gone"}, msg("ch"))
		require.ErrorIs(t, err, errTerminalChannelRelay)
	})

	t.Run("live worker receives the wrapped ciphertext", func(t *testing.T) {
		wm := workermgr.New(workermgr.DenyAllReach())
		var got []*leapmuxv1.ConnectResponse
		_, _ = wm.Register(&workermgr.Conn{WorkerID: "w1", SendFn: func(m *leapmuxv1.ConnectResponse) error {
			got = append(got, m)
			return nil
		}})
		h := &ChannelRelayHandler{workerMgr: wm, channelMgr: channelmgr.New()}
		err := h.relayFrontendMessageToWorker(
			channelmgr.ChannelInfo{ChannelID: "ch", WorkerID: "w1"}, msg("ch"))
		require.NoError(t, err)
		require.Len(t, got, 1)
		assert.Equal(t, "ch", got[0].GetChannelMessage().GetChannelId())
		assert.Equal(t, []byte("ct"), got[0].GetChannelMessage().GetCiphertext())
	})

	t.Run("broken worker stream is terminal", func(t *testing.T) {
		wm := workermgr.New(workermgr.DenyAllReach())
		_, _ = wm.Register(&workermgr.Conn{WorkerID: "w1", SendFn: func(*leapmuxv1.ConnectResponse) error {
			return errors.New("stream closed")
		}})
		h := &ChannelRelayHandler{workerMgr: wm, channelMgr: channelmgr.New()}
		err := h.relayFrontendMessageToWorker(
			channelmgr.ChannelInfo{ChannelID: "ch", WorkerID: "w1"}, msg("ch"))
		require.ErrorIs(t, err, errTerminalChannelRelay)
	})
}
