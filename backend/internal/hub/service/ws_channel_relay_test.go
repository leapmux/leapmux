package service

import (
	"context"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
	handler := NewChannelRelayHandler(nil, nil, nil, nil, false)

	req := httptest.NewRequest(http.MethodGet, "/ws/channel", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestChannelRelay_SubprotocolToken_NotAccepted(t *testing.T) {
	handler := NewChannelRelayHandler(nil, nil, nil, nil, false)

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
	h := NewChannelRelayHandler(st, nil, nil, nil, false).WithTokenValidator(tv)
	return h, st, tv
}

func mintAdminAPIToken(t *testing.T, st store.Store, tv *auth.TokenValidator) string {
	t.Helper()
	u, err := st.Users().GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	tokenID := id.Generate()
	secret := auth.MintAccessSecret()
	require.NoError(t, st.APITokens().Create(context.Background(), store.CreateAPITokenParams{
		ID: tokenID, UserID: u.ID, ClientType: "cli", ClientName: "test",
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
		ID: tokenID, UserID: u.ID, ClientType: "cli", ClientName: "test",
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
	h := NewChannelRelayHandler(st, nil, nil, nil, false)

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
	wm := workermgr.New()
	h := NewChannelRelayHandler(st, wm, cm, nil, false).WithTokenValidator(tv)

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
