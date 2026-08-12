package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"
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
		store:           sessionValidationOverrideStore{sessions: sessions},
		state:           &authState{},
		sessionDuration: sessionDuration,
	}
}

func cookieRequest() connect.AnyRequest {
	req := connect.NewRequest(&struct{}{})
	req.Header().Set("Cookie", CookieName+"=session")
	return req
}

// A streaming handler must carry the refreshed cookie too. Its response header
// is written with the FIRST message, so the interceptor sets the cookie before
// the handler runs -- a long-lived stream would otherwise return hours after
// the header went out, and the refresh would never reach the browser.
func TestWrapStreamingHandler_SlideRefreshesCookie(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	conn := &fakeStreamingConn{requestHeader: http.Header{"Cookie": []string{CookieName + "=session"}}}

	var headerAtHandler http.Header
	before := time.Now()
	err := a.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		// Snapshot inside the handler: a cookie written after next() returns is
		// too late for a stream that already sent its header.
		headerAtHandler = conn.ResponseHeader().Clone()
		return nil
	})(context.Background(), conn)
	require.NoError(t, err)

	parsed, parseErr := http.ParseSetCookie(headerAtHandler.Get("Set-Cookie"))
	require.NoError(t, parseErr, "the stream's response header must carry the refreshed cookie")
	assert.Equal(t, "session", parsed.Value)
	assert.True(t, parsed.Expires.After(before.Add(36*time.Hour)),
		"the refreshed cookie must outlive the configured duration from this request")
}

// A refused stream must not hand back a cookie: the interceptor never reaches
// the refresh, so the browser keeps whatever deadline it had.
func TestWrapStreamingHandler_UnauthenticatedWritesNoCookie(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	conn := &fakeStreamingConn{requestHeader: http.Header{}}

	err := a.WrapStreamingHandler(func(context.Context, connect.StreamingHandlerConn) error {
		return errors.New("the handler must not run")
	})(context.Background(), conn)

	require.Error(t, err)
	assert.Equal(t, connect.CodeUnauthenticated, connect.CodeOf(err))
	assert.Empty(t, conn.ResponseHeader().Values("Set-Cookie"))
}

// A handler that fails answers with a connect.Error and no response to hang a
// header on. The slide already reached the store, so the refresh is dropped
// rather than forced onto a nil response -- and the handler's error reaches the
// caller unchanged.
func TestWrapUnary_HandlerErrorDropsRefresh(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)
	handlerErr := connect.NewError(connect.CodeInternal, errors.New("boom"))

	resp, err := a.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, handlerErr
	})(context.Background(), cookieRequest())

	require.ErrorIs(t, err, handlerErr)
	assert.Nil(t, resp)
}

// A handler that returns neither a response nor an error is a contract
// violation somewhere below, but the interceptor must not turn it into a nil
// dereference on the way out.
func TestWrapUnary_NilResponseWithoutErrorDoesNotPanic(t *testing.T) {
	a := slidingInterceptor(t, 36*time.Hour)

	resp, err := a.WrapUnary(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		return nil, nil
	})(context.Background(), cookieRequest())

	require.NoError(t, err)
	assert.Nil(t, resp)
}

// fakeStreamingConn is the smallest connect.StreamingHandlerConn that carries a
// request header in and a response header out, which is all the auth
// interceptor touches.
type fakeStreamingConn struct {
	requestHeader   http.Header
	responseHeader  http.Header
	responseTrailer http.Header
}

func (c *fakeStreamingConn) Spec() connect.Spec { return connect.Spec{Procedure: "/private"} }
func (c *fakeStreamingConn) Peer() connect.Peer { return connect.Peer{} }
func (c *fakeStreamingConn) Receive(any) error  { return nil }
func (c *fakeStreamingConn) Send(any) error     { return nil }

func (c *fakeStreamingConn) RequestHeader() http.Header {
	if c.requestHeader == nil {
		c.requestHeader = http.Header{}
	}
	return c.requestHeader
}

func (c *fakeStreamingConn) ResponseHeader() http.Header {
	if c.responseHeader == nil {
		c.responseHeader = http.Header{}
	}
	return c.responseHeader
}

func (c *fakeStreamingConn) ResponseTrailer() http.Header {
	if c.responseTrailer == nil {
		c.responseTrailer = http.Header{}
	}
	return c.responseTrailer
}
