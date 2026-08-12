package auth

import (
	"net/http"
	"time"
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

// applyTo writes the refreshed session cookie into h, unless the handler wrote a
// Set-Cookie of its own.
//
// Skipping a handler-written cookie is what makes the refresh safe to run on
// every response. A browser applies several Set-Cookie headers for one name in
// order and keeps the last, so a refresh appended after Logout's clearing cookie
// leaves a live session cookie in the browser -- the user stays signed in. The
// handlers that write this cookie (login, sign-up, OAuth signup completion, the
// OAuth callback, logout) each name the session that the response established,
// and that is always newer than the slide the interceptor observed on the way
// in.
func (r sessionRefresh) applyTo(h http.Header, secure bool) {
	if r.sessionID == "" || len(h.Values("Set-Cookie")) > 0 {
		return
	}
	h.Add("Set-Cookie", BuildSessionCookie(r.sessionID, r.expiresAt, secure).String())
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
