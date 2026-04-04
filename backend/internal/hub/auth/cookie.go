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
