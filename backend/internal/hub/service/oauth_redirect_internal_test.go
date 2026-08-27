package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// TestMatchRedirectURIIsExactExceptForALoopbackPort pins the whole matching
// rule.
//
// EXACT string matching is the default, and it is what stops a registration
// from being widened by anything a caller writes. The one exception is what RFC
// 8252 section 7.3 REQUIRES of a conformant server: a registered loopback
// address matches with the port free, because a native app binds an ephemeral
// one it cannot know at registration.
//
// That exception is a property of the URI and not of the app. The control CLI's
// ephemeral port comes from the same rule any third-party native app gets,
// which is what stops "is this the CLI?" from being a question this code has to
// answer.
func TestMatchRedirectURIIsExactExceptForALoopbackPort(t *testing.T) {
	t.Parallel()

	registered := []string{
		"http://127.0.0.1/callback",
		"https://app.example.com/oauth/callback",
		"com.example.app:/cb",
	}

	t.Run("a loopback registration matches any port", func(t *testing.T) {
		for _, presented := range []string{
			"http://127.0.0.1/callback",
			"http://127.0.0.1:1/callback",
			"http://127.0.0.1:54321/callback",
			"http://127.0.0.1:65535/callback",
		} {
			matched, ok := MatchRedirectURI(registered, presented)
			assert.Truef(t, ok, "%q must match the loopback registration", presented)
			assert.Equal(t, "http://127.0.0.1/callback", matched)
		}
	})

	t.Run("everything except the port stays exact", func(t *testing.T) {
		for _, presented := range []string{
			// A different PATH is a different address.
			"http://127.0.0.1:54321/evil",
			"http://127.0.0.1:54321/callback/",
			// A different HOST, even another loopback spelling: the
			// registration named one, and matching another would let a
			// registration for 127.0.0.1 accept a page served on localhost.
			"http://localhost:54321/callback",
			// A different SCHEME.
			"https://127.0.0.1:54321/callback",
			// A query the registration does not carry.
			"http://127.0.0.1:54321/callback?next=evil",
			// A fragment, which RFC 6749 section 3.1.2 forbids outright.
			"http://127.0.0.1:54321/callback#x",
			// The classic prefix confusions.
			"http://127.0.0.1.evil.test/callback",
			"http://evil.test/callback",
		} {
			_, ok := MatchRedirectURI(registered, presented)
			assert.Falsef(t, ok, "%q must not match", presented)
		}
	})

	t.Run("a remote registration is exact, port included", func(t *testing.T) {
		matched, ok := MatchRedirectURI(registered, "https://app.example.com/oauth/callback")
		require.True(t, ok)
		assert.Equal(t, "https://app.example.com/oauth/callback", matched)

		// The loopback port exception must NOT leak to a remote host: a
		// registration for app.example.com may not accept app.example.com:8443.
		_, ok = MatchRedirectURI(registered, "https://app.example.com:8443/oauth/callback")
		assert.False(t, ok, "the port exception is for loopback alone")
	})

	t.Run("a private-use scheme is exact", func(t *testing.T) {
		matched, ok := MatchRedirectURI(registered, "com.example.app:/cb")
		require.True(t, ok)
		assert.Equal(t, "com.example.app:/cb", matched)
		_, ok = MatchRedirectURI(registered, "com.example.app:/other")
		assert.False(t, ok)
	})

	t.Run("an empty presented value matches nothing", func(t *testing.T) {
		_, ok := MatchRedirectURI(registered, "")
		assert.False(t, ok)
		_, ok = MatchRedirectURI(nil, "http://127.0.0.1:1/callback")
		assert.False(t, ok, "an app with no registered address matches nothing at all")
	})
}

// TestMatchRedirectURICoversEveryLoopbackHost ties the port exception to the
// shared host list, so a host httpsec accepts and this refuses cannot exist.
func TestMatchRedirectURICoversEveryLoopbackHost(t *testing.T) {
	t.Parallel()

	for _, host := range httpsec.LoopbackHosts {
		inURL := host
		if host == "::1" {
			inURL = "[::1]"
		}
		registered := []string{"http://" + inURL + "/callback"}
		_, ok := MatchRedirectURI(registered, "http://"+inURL+":54321/callback")
		assert.Truef(t, ok, "%q is a loopback host, so its port must be free", host)
	}
}

// TestValidateRedirectURIRefusesTheDangerousShapes pins what a REGISTRATION may
// offer. Refusing here rather than only at authorization is what turns "my app
// fails on its first login" into a message at registration time.
func TestValidateRedirectURIRefusesTheDangerousShapes(t *testing.T) {
	t.Parallel()

	for _, ok := range []string{
		"http://127.0.0.1/callback",
		"http://127.0.0.1:54321/callback",
		"http://localhost/cb",
		"https://app.example.com/callback",
		"https://app.example.com:8443/callback",
		"com.example.app:/oauth",
	} {
		assert.NoErrorf(t, ValidateRedirectURI(ok), "%q must be registrable", ok)
	}

	// An IPv6 literal is refused with its own message, at registration rather
	// than at the consent hop: the per-request form-action policy cannot state
	// one, so a registered IPv6 redirect was a login that hung with nothing in
	// any log. See CSPCannotStateHost.
	for _, refused := range []string{"https://[::1]/cb", "http://[::1]:54321/callback"} {
		err := ValidateRedirectURI(refused)
		if assert.Errorf(t, err, "%q must be refused", refused) {
			assert.Contains(t, err.Error(), "IPv6")
		}
	}

	refused := map[string]string{
		"":                                   "empty",
		"  https://app.example.com/cb  ":     "padded with spaces",
		"/callback":                          "relative: it would resolve against the hub's own origin",
		"https://app.example.com/cb#frag":    "carries a fragment, which RFC 6749 section 3.1.2 forbids",
		"https://*.example.com/cb":           "a wildcard, which exact matching can never honour",
		"http://app.example.com/cb":          "plain http to a remote host puts the code on the wire in clear",
		"javascript://127.0.0.1/%0aalert(1)": "the host is loopback and the SCHEME executes",
		"ftp://127.0.0.1/cb":                 "a loopback address must use http or https",
		"https:///cb":                        "https with no host",
		"myapp:/cb":                          "a private-use scheme must be reverse-domain (RFC 8252 section 7.1)",
	}
	for uri, why := range refused {
		assert.Errorf(t, ValidateRedirectURI(uri), "%q must be refused: %s", uri, why)
	}
}

func TestValidateRedirectURIsCapsAndDeduplicates(t *testing.T) {
	t.Parallel()

	assert.NoError(t, ValidateRedirectURIs(nil), "an app that runs no redirecting flow registers none")

	dup := []string{"https://a.example.com/cb", "https://a.example.com/cb"}
	assert.Error(t, ValidateRedirectURIs(dup), "a duplicate is an author mistake, not a wider registration")

	tooMany := make([]string, 0, maxRedirectURIs+1)
	for i := range maxRedirectURIs + 1 {
		tooMany = append(tooMany, "https://a.example.com/cb"+string(rune('a'+i)))
	}
	assert.Error(t, ValidateRedirectURIs(tooMany))
}

// TestRedirectListRoundTrips pins the storage encoding. A URI cannot contain a
// raw newline, so the delimiter is unambiguous by the grammar of the values --
// and ValidateRedirectURI refuses one anyway, so the two agree.
func TestRedirectListRoundTrips(t *testing.T) {
	t.Parallel()

	uris := []string{"http://127.0.0.1/callback", "https://app.example.com/cb"}
	assert.Equal(t, uris, ParseRedirectURIs(JoinRedirectURIs(uris)))
	assert.Empty(t, ParseRedirectURIs(""))
	assert.Empty(t, ParseRedirectURIs("\n \n"))
}

// TestRedirectFormActionSource pins the per-request CSP source.
//
// The global policy is `form-action 'self'` alone. A browser matches
// form-action against EVERY hop of a submission's redirect chain, so the
// consent POST's redirect to the app needs its origin named -- and naming one
// origin per request is narrower than the wildcard loopback set the global
// policy used to carry on every page.
func TestRedirectFormActionSource(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://app.example.com", redirectFormActionSource("https://app.example.com/cb"))
	assert.Equal(t, "https://app.example.com:8443", redirectFormActionSource("https://app.example.com:8443/cb"))
	// A loopback target keeps a WILDCARD port: the client binds an ephemeral
	// one, and CSP cannot say "any port of this host" any other way.
	assert.Equal(t, "http://127.0.0.1:*", redirectFormActionSource("http://127.0.0.1:54321/cb"))
	assert.Equal(t, "http://localhost:*", redirectFormActionSource("http://localhost/cb"))

	// An IPv6 literal CANNOT be stated: CSP's host-source grammar has no
	// production for one, and Chromium ignores the whole entry and logs a
	// console error. Answering "" leaves the global `'self'`, which blocks the
	// hop -- an honest failure rather than a policy that silently ignores its
	// own source.
	assert.Equal(t, "", redirectFormActionSource("http://[::1]:54321/cb"))
	// A private-use scheme is a client-side handoff the browser resolves
	// itself; no http hop leaves, so there is nothing for CSP to admit.
	assert.Equal(t, "", redirectFormActionSource("com.example.app:/cb"))
	assert.Equal(t, "", redirectFormActionSource("::::"))
}
