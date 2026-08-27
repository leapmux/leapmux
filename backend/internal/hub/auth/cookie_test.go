package auth_test

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func TestBuildSessionCookie_Insecure(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	c := auth.BuildSessionCookie("sess-123", expires, false)

	assert.Equal(t, auth.CookieName, c.Name)
	assert.Equal(t, "sess-123", c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
	assert.False(t, c.Secure)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
}

func TestBuildSessionCookie_Secure(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	c := auth.BuildSessionCookie("sess-456", expires, true)

	assert.Equal(t, auth.SecureCookieName, c.Name)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
}

func TestClearSessionCookie(t *testing.T) {
	c := auth.ClearSessionCookie(false)

	assert.Equal(t, auth.CookieName, c.Name)
	assert.Empty(t, c.Value)
	assert.True(t, c.MaxAge < 0)
	assert.True(t, c.HttpOnly)
}

func TestSessionIDFromHeader_ValidCookie(t *testing.T) {
	header := auth.CookieName + "=my-session-id"
	got := auth.SessionIDFromHeader(header, false)
	assert.Equal(t, "my-session-id", got)
}

func TestSessionIDFromHeader_MultipleCookies(t *testing.T) {
	header := "other=value; " + auth.CookieName + "=correct-id; foo=bar"
	got := auth.SessionIDFromHeader(header, false)
	assert.Equal(t, "correct-id", got)
}

func TestSessionIDFromHeader_NoCookie(t *testing.T) {
	got := auth.SessionIDFromHeader("other=value; foo=bar", false)
	assert.Empty(t, got)
}

func TestSessionIDFromHeader_EmptyHeader(t *testing.T) {
	got := auth.SessionIDFromHeader("", false)
	assert.Empty(t, got)
}

func TestSessionIDFromHeader_SecureCookie(t *testing.T) {
	header := auth.SecureCookieName + "=secure-id"
	got := auth.SessionIDFromHeader(header, true)
	assert.Equal(t, "secure-id", got)
}

// The OAuth flow-binding cookie. Every handler test runs with secure
// cookies off, so the __Host- half of these helpers has no coverage
// there — and that is the half production uses.

func TestBuildOAuthNonceCookie_Insecure(t *testing.T) {
	c := auth.BuildOAuthNonceCookie("state-abc", "nonce-xyz", 5*time.Minute, false)

	// flowCookieName HASHES the state into the name, never concatenates it
	// raw. A cookie
	// name must be an RFC 7230 token, and Go answers an invalid name by
	// writing no Set-Cookie header at all -- silently, so every later login
	// would fail with a refusal that accuses the user's browser.
	assert.True(t, strings.HasPrefix(c.Name, auth.OAuthNonceCookieName+"-"))
	assert.NotContains(t, c.Name, "state-abc", "the raw flow id must not reach the name")
	assert.Regexp(t, `^`+auth.OAuthNonceCookieName+`-[0-9a-f]{32}$`, c.Name)
	assert.Equal(t, "nonce-xyz", c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly, "script must not read the nonce")
	assert.False(t, c.Secure)
	// Lax, never Strict: the callback arrives as a cross-site top-level
	// navigation from the identity provider, and the browser withholds a
	// Strict cookie on exactly that hop.
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Equal(t, int((5 * time.Minute).Seconds()), c.MaxAge)
}

func TestBuildOAuthNonceCookie_Secure(t *testing.T) {
	c := auth.BuildOAuthNonceCookie("state-abc", "nonce-xyz", 5*time.Minute, true)

	assert.Regexp(t, `^`+auth.SecureOAuthNonceCookieName+`-[0-9a-f]{32}$`, c.Name)
	assert.True(t, strings.HasPrefix(c.Name, "__Host-"),
		"the __Host- prefix is what stops a subdomain from shadowing this cookie")
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
	// __Host- is only honoured with Path=/ and no Domain. The browser drops
	// a cookie that breaks either rule, and the callback then refuses every
	// legitimate login.
	assert.Equal(t, "/", c.Path)
	assert.Empty(t, c.Domain)
}

// TestOAuthNonceCookieNamesAreFlowScoped pins the property that keeps two
// sign-ins in one browser from evicting each other.
func TestOAuthNonceCookieNamesAreFlowScoped(t *testing.T) {
	first := auth.BuildOAuthNonceCookie("state-1", "n1", time.Minute, false)
	second := auth.BuildOAuthNonceCookie("state-2", "n2", time.Minute, false)
	assert.NotEqual(t, first.Name, second.Name)

	// And the secure and insecure names never collide, so a hub that flips
	// secure_cookies mid-flow cannot read the wrong one.
	assert.NotEqual(t,
		auth.BuildOAuthNonceCookie("state-1", "n1", time.Minute, false).Name,
		auth.BuildOAuthNonceCookie("state-1", "n1", time.Minute, true).Name)

	// The two families never collide either: a pending-signup cookie must
	// not be mistaken for a flow cookie, and the unprefixed flow name is a
	// prefix of the unprefixed signup family name.
	assert.NotEqual(t,
		auth.BuildOAuthNonceCookie("tok", "n", time.Minute, false).Name,
		auth.BuildOAuthSignupNonceCookie("tok", "n", time.Minute, false).Name)
}

// TestCookieMaxAgeNeverWritesZero pins the clamp. int(ttl.Seconds())
// truncates toward zero, and Go omits Max-Age entirely for 0, so a
// sub-second TTL would produce a cookie that lives for the whole browsing
// session instead of half a second.
func TestCookieMaxAgeNeverWritesZero(t *testing.T) {
	c := auth.BuildOAuthNonceCookie("s", "n", 500*time.Millisecond, false)
	assert.Positive(t, c.MaxAge, "a positive sub-second TTL must still expire")

	c = auth.BuildOAuthNonceCookie("s", "n", -500*time.Millisecond, false)
	assert.Negative(t, c.MaxAge, "a negative TTL must clear, never persist")

	c = auth.BuildOAuthNonceCookie("s", "n", 0, false)
	assert.Negative(t, c.MaxAge, "a zero TTL must clear, never persist")
}

// TestOAuthNonceFromRequestAcceptsTheSecureSpelling pins the asymmetric
// fallback. The hub reads secure_cookies when the login leg writes the cookie
// and again when the callback reads it, so an operator who turns it OFF inside
// the window would otherwise turn every in-flight login into a security
// accusation. Only an https origin can set a __Host- cookie, so reading it
// on a plain-HTTP hub grants an attacker nothing. The reader refuses the
// reverse, because any plain-HTTP page on the domain can plant the unprefixed
// name.
func TestOAuthNonceFromRequestAcceptsTheSecureSpelling(t *testing.T) {
	secureSet := auth.BuildOAuthNonceCookie("state-abc", "nonce-xyz", time.Minute, true)
	r := &http.Request{Header: http.Header{}}
	r.AddCookie(secureSet)

	assert.Equal(t, "nonce-xyz", auth.OAuthNonceFromRequest(r, "state-abc", true))
	assert.Equal(t, "nonce-xyz", auth.OAuthNonceFromRequest(r, "state-abc", false),
		"a hub that turned secure_cookies off must still read the cookie it wrote with it on")

	insecureSet := auth.BuildOAuthNonceCookie("state-abc", "planted", time.Minute, false)
	r2 := &http.Request{Header: http.Header{}}
	r2.AddCookie(insecureSet)
	assert.Empty(t, auth.OAuthNonceFromRequest(r2, "state-abc", true),
		"a secure-cookie hub must never accept the unprefixed name, which any plain-HTTP page can plant")
}

// TestClearOAuthNonceCookie pins that the callback clears BOTH spellings.
// The reader accepts both, so clearing only the hub's current one would
// leave a live nonce in the browser after an operator changed
// secure_cookies inside the flow's window.
func TestClearOAuthNonceCookie(t *testing.T) {
	cleared := auth.ClearOAuthNonceCookie("state-abc")
	require.Len(t, cleared, 2)

	names := map[string]*http.Cookie{}
	for _, c := range cleared {
		assert.Empty(t, c.Value)
		assert.Negative(t, c.MaxAge, "a negative MaxAge is what deletes it")
		assert.Equal(t, "/", c.Path)
		names[c.Name] = c
	}
	// Each clear must carry the SAME name as the cookie it deletes; a
	// browser keys cookies by name, so a mismatch leaves the nonce live.
	for _, secure := range []bool{false, true} {
		name := auth.BuildOAuthNonceCookie("state-abc", "n", time.Minute, secure).Name
		c, ok := names[name]
		require.Truef(t, ok, "no clear for the %v-cookie spelling", secure)
		// The __Host- clear needs Secure to be valid at all, and the
		// unprefixed clear must NOT carry it, or a browser on plain HTTP
		// drops the clear and the nonce survives.
		assert.Equal(t, secure, c.Secure)
	}
}

func TestOAuthNonceFromRequest(t *testing.T) {
	for _, secure := range []bool{false, true} {
		set := auth.BuildOAuthNonceCookie("state-abc", "nonce-xyz", time.Minute, secure)
		r := &http.Request{Header: http.Header{}}
		r.AddCookie(set)

		assert.Equal(t, "nonce-xyz", auth.OAuthNonceFromRequest(r, "state-abc", secure))

		// A different flow's state reads nothing, which is the whole
		// binding: an attacker's state identifies a cookie that this
		// browser never received.
		assert.Empty(t, auth.OAuthNonceFromRequest(r, "state-other", secure))
	}

	// The one direction that must NOT read across the two spellings: a
	// secure-cookie hub refuses the unprefixed name, which any plain-HTTP
	// page on the domain can plant. TestOAuthNonceFromRequestAcceptsThe-
	// SecureSpelling covers the direction that is safe to allow.
	insecure := &http.Request{Header: http.Header{}}
	insecure.AddCookie(auth.BuildOAuthNonceCookie("state-abc", "planted", time.Minute, false))
	assert.Empty(t, auth.OAuthNonceFromRequest(insecure, "state-abc", true))

	// No cookie at all.
	assert.Empty(t, auth.OAuthNonceFromRequest(&http.Request{Header: http.Header{}}, "state-abc", false))
}
