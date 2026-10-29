package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// slidingInterceptor builds an interceptor over a session store whose Touch
// always reports one updated row, so every request slides and the refresh is
// observable without waiting out the throttle.
func slidingInterceptor(t *testing.T, sessionDuration time.Duration) *authInterceptor {
	t.Helper()
	sessions := &touchRecordingSessionStore{row: &store.SessionWithUser{
		UserID: "user", Username: "user",
		CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Minute),
	}}
	return &authInterceptor{
		store:  sessionValidationOverrideStore{sessions: sessions},
		state:  &authState{},
		policy: func(context.Context) Policy { return Policy{SessionDuration: sessionDuration} },
	}
}

// A store that cannot answer must not be asked again on the very next request.
// touchSession records the throttle BEFORE it runs the UPDATE, so a store that
// refuses every touch costs one attempt per throttle window; recording it after
// the error return would have made every authenticated request retry, turning a
// database hiccup into a write storm against the database that is already
// unwell.
func TestTouchSession_StoreErrorIsThrottledLikeAnySlide(t *testing.T) {
	sessions := &touchRecordingSessionStore{
		row: &store.SessionWithUser{
			UserID: "user", Username: "user",
			CreatedAt: time.Now().Add(-time.Hour), ExpiresAt: time.Now().Add(time.Minute),
		},
		touchErr: errors.New("database is down"),
	}
	a := &authInterceptor{
		store:  sessionValidationOverrideStore{sessions: sessions},
		state:  &authState{},
		policy: func(context.Context) Policy { return Policy{SessionDuration: 36 * time.Hour} },
	}

	first := a.touchSession(context.Background(), "session", nil, a.currentPolicy(context.Background()))
	second := a.touchSession(context.Background(), "session", nil, a.currentPolicy(context.Background()))

	assert.True(t, first.IsZero(), "a failed touch slid nothing")
	assert.True(t, second.IsZero())
	assert.Equal(t, 1, sessions.touchCalls,
		"the second request must wait out the throttle rather than retry the failing UPDATE")
}

// callWrap drives an interceptor the way the transport does. connect.Server
// .Call attaches the CallInfo to the context, and the unknown-handler slot
// stands in for a registered procedure so the test controls the handler body.
// A raw ServerFunc invocation gets no CallInfo, and wrap reads its headers from
// one.
func callWrap(t *testing.T, interceptor connect.ServerInterceptor, procedure string, info *connect.CallInfo, handler connect.ServerFunc) error {
	t.Helper()
	server := connect.NewServer(interceptor)
	server.SetUnknownHandler(handler)
	return server.Call(context.Background(), procedure, info, &fakeServerStream{})
}

// cookieCallInfo seeds one call's request headers with the session cookie the
// fakes validate.
func cookieCallInfo() *connect.CallInfo {
	info := &connect.CallInfo{}
	info.RequestHeader().Set("Cookie", CookieName+"=session")
	return info
}

// fakeServerStream is the smallest connect.ServerStream: the auth interceptor
// never touches the stream, and headers live on the call's CallInfo.
type fakeServerStream struct{}

func (fakeServerStream) Receive(any) error  { return nil }
func (fakeServerStream) SendHeaders() error { return nil }
func (fakeServerStream) Send(any) error     { return nil }

// A sliding request must hand the refreshed cookie to the handler BEFORE it
// runs. A stream's response header goes out with its first message, long before
// the call returns, so a cookie written after the handler returned would never
// reach a long-lived stream's browser.
func TestWrap_SlideRefreshesCookieBeforeTheHandlerRuns(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	info := cookieCallInfo()

	var cookieAtHandler string
	before := time.Now()
	err := callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		// Read inside the handler: a cookie written after the handler returns
		// is too late for a stream that already sent its header.
		cookieAtHandler = info.ResponseHeader().Get("Set-Cookie")
		return nil
	})
	require.NoError(t, err)

	parsed, parseErr := http.ParseSetCookie(cookieAtHandler)
	require.NoError(t, parseErr, "the response header must carry the refreshed cookie")
	assert.Equal(t, "session", parsed.Value)
	assert.True(t, parsed.Expires.After(before.Add(36*time.Hour)),
		"the refreshed cookie must outlive the configured duration from this request")
}

// A request the interceptor refuses BEFORE it authenticates anything must not
// hand back a cookie. There is no slide to report, a cookie on an
// unauthenticated answer would be a session the caller never presented, and a
// refused stream's browser keeps whatever deadline it had.
func TestWrap_UnauthenticatedWritesNoCookie(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	info := &connect.CallInfo{}

	err := callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		return errors.New("the handler must not run")
	})

	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, info.ResponseHeader().Values("Set-Cookie"))
}

// A handler that fails has no response to hang a header on, but the refresh is
// written to the call's response header before the handler runs, and the
// transport flushes that header when it writes an error status. The slide
// already reached the store, so the cookie travels with the rejection -- a
// client whose calls keep failing would otherwise slide the row on every
// request and still be signed out at the login deadline, because the throttle
// gives the cookie no other request to ride. The handler's error must reach the
// caller with its code and message intact.
func TestWrap_HandlerErrorStillCarriesRefresh(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	handlerErr := connect.NewError(connect.CodeInternal, "boom")

	before := time.Now()
	info := cookieCallInfo()
	err := callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		return handlerErr
	})

	require.ErrorIs(t, err, handlerErr, "the handler's error must reach the caller unchanged")
	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeInternal, connectErr.Code())
	parsed, parseErr := http.ParseSetCookie(info.ResponseHeader().Get("Set-Cookie"))
	require.NoError(t, parseErr, "a failed call must still carry the refreshed cookie")
	assert.Equal(t, "session", parsed.Value)
	assert.True(t, parsed.Expires.After(before.Add(36*time.Hour)))
}

// The same for a rejection the interceptor itself issues after the slide
// committed. An unverified user waiting to verify has every non-allowlisted
// procedure denied, and the slide runs before that gate -- so without this the
// row would keep moving while the browser's cookie stayed at the login
// deadline, for as long as the user kept being refused.
func TestWrap_EmailVerificationDenialCarriesRefresh(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	a.policy = func(context.Context) Policy {
		return Policy{SessionDuration: 36 * time.Hour, EmailVerificationRequired: true}
	}

	info := cookieCallInfo()
	err := callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		return errors.New("the handler must not run")
	})

	require.Error(t, err)
	assert.Equal(t, connect.CodePermissionDenied, connect.CodeOf(err))
	parsed, parseErr := http.ParseSetCookie(info.ResponseHeader().Get("Set-Cookie"))
	require.NoError(t, parseErr, "the denial must carry the cookie for the slide it already made")
	assert.Equal(t, "session", parsed.Value)
}

// A handler that returns no error and sends no message must complete cleanly.
// v2's handler returns only an error, so the v1 contract violation -- a nil
// response beside a nil error -- is unrepresentable; what remains is the
// interceptor's out-path obligation to assume nothing about what the handler
// sent.
func TestWrap_SuccessWithoutAMessageDoesNotPanic(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	info := cookieCallInfo()

	err := callWrap(t, a.wrap, "/private", info, func(context.Context, connect.Spec, connect.ServerStream) error {
		return nil
	})

	require.NoError(t, err)
}
