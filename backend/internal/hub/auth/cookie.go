package auth

import (
	"crypto/sha256"
	"encoding/hex"
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
	// OAuthNonceCookieName is the OAuth flow-binding cookie name used without TLS.
	OAuthNonceCookieName = "leapmux-oauth"
	// SecureOAuthNonceCookieName is the OAuth flow-binding cookie name used
	// with TLS (__Host- prefix).
	SecureOAuthNonceCookieName = "__Host-leapmux-oauth"
	// OAuthSignupNonceCookieName is the pending-signup binding cookie name
	// used without TLS.
	OAuthSignupNonceCookieName = "leapmux-oauth-signup"
	// SecureOAuthSignupNonceCookieName is the pending-signup binding cookie
	// name used with TLS (__Host- prefix).
	SecureOAuthSignupNonceCookieName = "__Host-leapmux-oauth-signup"
)

// cookieName returns the appropriate cookie name based on the secure flag.
func cookieName(secure bool) string {
	if secure {
		return SecureCookieName
	}
	return CookieName
}

// newCookie builds one of this package's cookies with the attributes every
// one of them must carry.
//
// Path "/" and an absent Domain are not defaults here, they are
// requirements: a browser refuses a __Host- cookie that omits either, and
// every name this package builds takes that prefix under secure_cookies.
// One constructor makes that true for every cookie the package builds,
// rather than true in four literals that a fifth can forget.
//
// newCookie clamps maxAge rather than trusting it. int(ttl.Seconds()) truncates
// toward zero, and Go writes NO Max-Age attribute for 0, so a sub-second
// positive TTL would produce a cookie that lives for the whole browsing
// session and a sub-second negative one would fail to clear.
func newCookie(name, value string, maxAge int, secure bool) *http.Cookie {
	return &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// cookieMaxAge converts a TTL to the whole seconds a cookie can express,
// never to 0. See newCookie for why 0 is the one value that must not reach
// a cookie.
func cookieMaxAge(ttl time.Duration) int {
	if seconds := int(ttl.Seconds()); seconds != 0 {
		return seconds
	}
	if ttl > 0 {
		return 1
	}
	return -1
}

// flowCookieName returns the per-flow cookie name for one family.
//
// flowCookieName HASHES the flow id into the name rather than embedding it.
// A cookie name
// must be an RFC 7230 token, and Go answers an invalid name by returning an
// empty Set-Cookie line: http.SetCookie then writes no header at all, with
// no error and no log, and every login afterwards fails with the refusal
// that accuses the user's browser. A hex digest is always a token and
// always 32 characters, so no change to how the hub mints a state or a
// token can produce a name a browser never receives.
//
// The flow id is part of the name so two logins started in the same browser
// do not share one cookie. With a single shared name the second login
// overwrote the first one's nonce, and the older tab's callback then failed
// with "a different browser started this sign-in" -- a refusal aimed at an
// attacker,
// shown to a user who did nothing wrong.
//
// A per-flow name costs nothing in security. A callback for flow S reads
// only the cookie for S, and the victim's browser holds one solely because
// that browser started the flow. An attacker's state identifies a cookie
// that the victim never received, so the lookup misses and the callback
// refuses, exactly as with one shared name.
func flowCookieName(family, flowID string) string {
	sum := sha256.Sum256([]byte(flowID))
	return family + "-" + hex.EncodeToString(sum[:16])
}

// flowCookieFamily is one per-flow binding cookie: its two spellings, and
// the name, build and clear methods that follow from them.
//
// TWO families exist -- the OAuth flow's nonce and the pending signup's --
// and they differed only in which flow id they hash. Each carried its own
// name function, its own Build and its own Clear, so the rules that make a
// binding cookie work (hash the flow id into the name, write both spellings
// when clearing, give each spelling the Secure flag its own name requires)
// appeared twice and could drift. A third binding cookie now inherits
// them instead of copying them.
//
// The READERS stay separate below, deliberately: one holds an
// *http.Request and one a raw header, and the two cookie parsers behind
// them do not treat a malformed header alike.
type flowCookieFamily struct {
	secureName string
	plainName  string
}

var (
	// oauthNonceFamily binds an OAuth flow to the browser that started it.
	oauthNonceFamily = flowCookieFamily{SecureOAuthNonceCookieName, OAuthNonceCookieName}
	// oauthSignupNonceFamily carries that binding across the hand-off to
	// the pending signup.
	oauthSignupNonceFamily = flowCookieFamily{SecureOAuthSignupNonceCookieName, OAuthSignupNonceCookieName}
)

// name returns this family's cookie name for one flow. See flowCookieName
// for why the flow id is hashed rather than embedded.
func (f flowCookieFamily) name(flowID string, secure bool) string {
	if secure {
		return flowCookieName(f.secureName, flowID)
	}
	return flowCookieName(f.plainName, flowID)
}

// build creates this family's cookie for one flow.
func (f flowCookieFamily) build(flowID, nonce string, ttl time.Duration, secure bool) *http.Cookie {
	return newCookie(f.name(flowID, secure), nonce, cookieMaxAge(ttl), secure)
}

// clear returns the cookies that clear one flow, in BOTH spellings, because
// the reader accepts both. Clearing only the current one would leave a live
// nonce in the browser after an operator changed secure_cookies mid-flow.
//
// Each clear carries the Secure flag its own name requires, not the hub's
// current setting: a __Host- cookie is invalid without Secure, and a browser
// on plain HTTP drops a Secure cookie, so the unprefixed clear must not
// carry the flag or it would never arrive.
func (f flowCookieFamily) clear(flowID string) []*http.Cookie {
	return []*http.Cookie{
		newCookie(f.name(flowID, true), "", -1, true),
		newCookie(f.name(flowID, false), "", -1, false),
	}
}

// BuildOAuthNonceCookie creates the short-lived cookie that binds an OAuth
// flow to the browser that started it. The callback compares its value
// against the hash stored on the oauth_states row, so an attacker cannot
// redeem a (code, state) pair they captured in a victim's browser.
//
// SameSite must be Lax, not Strict. The callback arrives as a cross-site
// top-level navigation from the identity provider, and the browser
// withholds a Strict cookie on exactly that hop -- which would refuse every
// legitimate login rather than only the forged ones.
func BuildOAuthNonceCookie(state, nonce string, ttl time.Duration, secure bool) *http.Cookie {
	return oauthNonceFamily.build(state, nonce, ttl, secure)
}

// ClearOAuthNonceCookie creates the cookies that clear one flow's OAuth
// nonce. The callback clears it once the flow can no longer be completed:
// the nonce is single-use, and one left in the browser would outlive the
// state row it belongs to.
//
// It returns BOTH spellings, because the reader accepts both. Clearing only
// the current one would leave a live nonce in the browser after an operator
// changed secure_cookies mid-flow.
func ClearOAuthNonceCookie(state string) []*http.Cookie {
	return oauthNonceFamily.clear(state)
}

// OAuthNonceFromRequest reads one flow's OAuth nonce cookie, or "" when
// this browser holds none for that flow.
//
// A hub that runs without secure_cookies also accepts the __Host- spelling,
// and the asymmetry is the point. secure_cookies is read once when the
// login stage writes the cookie and again when the callback reads it, so an
// operator who turns it OFF inside the five-minute window would otherwise
// turn every in-flight login into "a different browser started this
// sign-in" -- a
// security accusation for an operator action. Reading the secure spelling
// costs nothing: only an https origin can set a __Host- cookie at all, so
// a plain-HTTP attacker cannot plant one. This reader refuses the reverse
// fallback on purpose, because any plain-HTTP page on the registrable domain CAN
// plant the unprefixed name, which is exactly what __Host- prevents.
func OAuthNonceFromRequest(r *http.Request, state string, secure bool) string {
	if v := cookieValue(r, oauthNonceFamily.name(state, true)); v != "" {
		return v
	}
	if secure {
		return ""
	}
	return cookieValue(r, oauthNonceFamily.name(state, false))
}

// BuildOAuthSignupNonceCookie creates the cookie that carries the browser
// binding ACROSS the hand-off from the OAuth callback to the pending
// signup.
//
// Without it the binding stops at oauth_states. The callback hands the
// browser a signup token in a URL, and CompleteOAuthSignup would then
// create the account and set a session for whoever presents that token --
// so an attacker who completes their OWN callback can deliver the token to
// a victim, whose browser is signed into an account the attacker's identity
// owns and can return to at any time. The token specifies a flow; this cookie
// specifies the browser.
func BuildOAuthSignupNonceCookie(token, nonce string, ttl time.Duration, secure bool) *http.Cookie {
	return oauthSignupNonceFamily.build(token, nonce, ttl, secure)
}

// ClearOAuthSignupNonceCookie creates the cookies that clear one pending
// signup's nonce, in both spellings; see ClearOAuthNonceCookie.
func ClearOAuthSignupNonceCookie(token string) []*http.Cookie {
	return oauthSignupNonceFamily.clear(token)
}

// OAuthSignupNonceFromHeader reads one pending signup's nonce from a raw
// Cookie header value. CompleteOAuthSignup is a Connect procedure, so it
// holds the header rather than a parsed http.Request.
//
// It accepts the secure spelling on a hub without secure_cookies for the
// same reason OAuthNonceFromRequest does; read that comment.
func OAuthSignupNonceFromHeader(cookieHeader, token string, secure bool) string {
	if v := cookieValueFromHeader(cookieHeader, oauthSignupNonceFamily.name(token, true)); v != "" {
		return v
	}
	if secure {
		return ""
	}
	return cookieValueFromHeader(cookieHeader, oauthSignupNonceFamily.name(token, false))
}

// cookieValue returns one cookie's value, or "" when the request carries
// no cookie of that name.
func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

// cookieValueFromHeader returns one cookie's value out of a raw Cookie
// header, or "" when the header carries no cookie of that name.
func cookieValueFromHeader(cookieHeader, name string) string {
	if cookieHeader == "" {
		return ""
	}
	cookies, err := http.ParseCookie(cookieHeader)
	if err != nil {
		return ""
	}
	for _, c := range cookies {
		if c.Name == name {
			return c.Value
		}
	}
	return ""
}

// BuildSessionCookie creates an HttpOnly session cookie.
func BuildSessionCookie(sessionID string, expiresAt time.Time, secure bool) *http.Cookie {
	c := newCookie(cookieName(secure), sessionID, cookieMaxAge(time.Until(expiresAt)), secure)
	// The one attribute only this cookie carries. Expires is the fallback
	// that keeps a browser which ignores Max-Age on the same deadline.
	c.Expires = expiresAt
	return c
}

// sessionRefresh carries a session slide from the auth interceptor to the
// response, so the cookie's own Expires attribute tracks the slid DB expiry.
// The zero value means that nothing slid on this request and this code
// writes no cookie.
type sessionRefresh struct {
	sessionID string
	expiresAt time.Time
}

// applyTo writes the refreshed session cookie into h, unless the handler already
// wrote a session cookie of its own.
//
// Leaving a handler's own cookie alone is what makes the refresh safe to
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
// overwrite this one. A guard that left every Set-Cookie alone would stop
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
	return newCookie(cookieName(secure), "", -1, secure)
}

// SessionIDFromCookieHeader reads the session id with the asymmetric fallback
// BOTH auth ladders use: the __Host- spelling first, and the unprefixed one
// only on a hub whose secure_cookies setting is off (so it never writes the
// prefix). See AuthenticateHTTP for why the fallback direction is safe.
//
// ONE helper, because the Connect interceptor, AuthenticateHTTP and Logout all
// answer the same question for the same browser, and a caller that spelled the
// ladder by hand could read a cookie the ladder does not -- or miss one it
// does, which is how a sign-out once cleared no row at all.
func SessionIDFromCookieHeader(cookieHeader string, secureCookies bool) string {
	token := cookieValueFromHeader(cookieHeader, cookieName(true))
	if token == "" && !secureCookies {
		token = cookieValueFromHeader(cookieHeader, cookieName(false))
	}
	return token
}
