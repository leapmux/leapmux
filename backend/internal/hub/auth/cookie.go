package auth

import (
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
)

const (
	// CookieName is the session cookie name used without TLS.
	CookieName = "leapmux-session"
	// SecureCookieName is the session cookie name used with TLS (__Host- prefix).
	SecureCookieName = "__Host-leapmux-session"
)

// cookieName returns the appropriate cookie name based on the secure flag.
func cookieName(secure bool) string {
	if secure {
		return SecureCookieName
	}
	return CookieName
}

// BuildSessionCookie creates an HttpOnly session cookie.
func BuildSessionCookie(sessionID string, expiresAt time.Time, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName(secure),
		Value:    sessionID,
		Path:     "/",
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// sessionRefresh carries a session slide from the auth interceptor to the
// response, so the cookie's own Expires attribute tracks the slid DB expiry.
// The zero value means that nothing slid on this request and no cookie is
// written.
type sessionRefresh struct {
	sessionID string
	expiresAt time.Time
}

// applyTo writes the refreshed session cookie into h, unless the handler already
// wrote a session cookie of its own.
//
// Standing back from a handler's own cookie is what makes the refresh safe to
// run on every response. A browser applies several Set-Cookie headers for one
// name in order and keeps the last, so a refresh appended after Logout's
// clearing cookie leaves a live session cookie in the browser -- the user stays
// signed in. The handlers that write this cookie (login, sign-up, OAuth signup
// completion, the OAuth callback, logout) each specify the session that the
// response established, and that is always newer than the slide the interceptor
// observed on the way in.
//
// The test is the cookie's NAME, not the presence of any cookie at all. A
// browser keeps the last cookie per name, so only a cookie of this name can
// overwrite this one. A guard that stood back from every Set-Cookie would stop
// refreshing the session on the first response that also carried an unrelated
// cookie, silently and with nothing to point at the cause.
func (r sessionRefresh) applyTo(h http.Header, secure bool) {
	if r.sessionID == "" {
		return
	}
	name := cookieName(secure)
	for _, line := range h.Values("Set-Cookie") {
		c, err := http.ParseSetCookie(line)
		// An unparseable cookie counts as a collision. The handler wrote
		// something this code cannot read, and losing one refresh costs a
		// throttle window, where overwriting a cookie meant for the browser
		// could sign a user in again after a logout.
		if err != nil || c.Name == name {
			return
		}
	}
	h.Add("Set-Cookie", BuildSessionCookie(r.sessionID, r.expiresAt, secure).String())
}

// applyToError writes the refreshed session cookie into err's metadata and
// returns the error to answer with.
//
// A ConnectRPC unary handler that fails has no response to carry a header, but
// connect-go merges a *connect.Error's metadata into the response header before
// it writes the status, so the cookie still reaches the browser.
//
// An error of any other type passes through untouched, and loses this one
// refresh. Wrapping it to give it metadata would decide its status code here,
// and connect.CodeOf answers CodeUnknown for a context.Canceled that connect-go
// itself reports as CodeCanceled -- so the wrapper would change what the client
// sees for every cancelled request that happened to slide. The cookie is worth
// less than the code: the row already slid, and the next slide re-issues it.
//
// A stream cannot use this: connect-go puts a stream error's metadata in the
// body trailer, where a browser ignores Set-Cookie. WrapStreamingHandler writes
// the cookie to the response header instead.
func (r sessionRefresh) applyToError(err error, secure bool) error {
	if err == nil || r.sessionID == "" {
		return err
	}
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return err
	}
	r.applyTo(connectErr.Meta(), secure)
	return err
}

// ClearSessionCookie creates a cookie that clears the session.
func ClearSessionCookie(secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     cookieName(secure),
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// SessionIDFromRequest extracts the session ID from a parsed http.Request's cookies.
func SessionIDFromRequest(r *http.Request, secure bool) string {
	c, err := r.Cookie(cookieName(secure))
	if err != nil {
		return ""
	}
	return c.Value
}

// SessionIDFromHeader extracts the session ID from a raw Cookie header value.
// This is used in ConnectRPC interceptors where we only have the header string.
func SessionIDFromHeader(cookieHeader string, secure bool) string {
	if cookieHeader == "" {
		return ""
	}
	target := cookieName(secure)
	cookies, err := http.ParseCookie(cookieHeader)
	if err != nil {
		return ""
	}
	for _, c := range cookies {
		if c.Name == target {
			return c.Value
		}
	}
	return ""
}
