package service

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
)

// Redirect-URI handling for the authorization server.
//
// The rule is EXACT STRING MATCHING against the app's registered list, with
// one exception, and the exception is one RFC 8252 section 7.3 requires of any
// conformant server: a registered LOOPBACK redirect matches on scheme, host
// and path with the PORT free, because a native app binds an ephemeral port
// it cannot know at registration.
//
// That exception is a property of the URI, not of the app. The control CLI's
// ephemeral port comes from the same rule any third-party native app gets,
// which is what stops "is this the CLI?" from being a question this code has
// to answer.

// redirectURISeparator delimits the stored list.
//
// A newline, because a URI cannot contain a raw one: RFC 3986 admits no
// control character unescaped, so the delimiter is unambiguous by the grammar
// of the values rather than by a convention a writer must remember.
const redirectURISeparator = "\n"

// maxRedirectURIs caps how many addresses one app may register.
//
// Not arbitrary: every authorization compares the presented URI against the
// whole list, and the consent page renders the matched one. A list nobody can
// read is a list whose owner cannot audit what their app accepts.
const maxRedirectURIs = 16

// ParseRedirectURIs splits a stored list. An empty list is legitimate: the
// service-account registration runs no flow and registers no address.
func ParseRedirectURIs(stored string) []string {
	out := make([]string, 0, 4)
	for _, line := range strings.Split(stored, redirectURISeparator) {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// JoinRedirectURIs renders a list for storage.
func JoinRedirectURIs(uris []string) string {
	return strings.Join(uris, redirectURISeparator)
}

// ValidateRedirectURI checks one address a registration offers.
//
// It refuses at REGISTRATION rather than only at authorization, because a
// registration is the durable artefact: an address nobody can reach is an app
// that fails on its first login with nothing for its owner to look at.
//
// The rules, in order of what they protect:
//
//   - Absolute, with a scheme and a host. A relative URI would resolve against
//     the hub's own origin at redirect time, so an app could aim the code at a
//     hub path.
//   - No fragment. RFC 6749 section 3.1.2 forbids one, and the authorization
//     response appends its own query -- a stored fragment would swallow it.
//   - A loopback address must use http or https and nothing else. The value
//     reaches a Location header, so a HOST test alone accepts
//     "javascript://127.0.0.1/%0aalert(1)": the host is loopback and the
//     scheme executes.
//   - Anything else must be https, or a private-use scheme with no host. A
//     plain-http redirect to a remote host puts the authorization code on the
//     wire in clear.
//   - No wildcard character anywhere. Matching is exact, so a `*` in a stored
//     URI can only be an author who expected it to mean something.
//   - An IPv6 loopback literal is refused with its own message. The consent
//     page's form-action policy cannot state an IPv6 host (see
//     redirectFormActionSource), and "a host the redirect accepts and the
//     policy omits is a login that hangs with nothing in any log" -- so the
//     honest place to refuse it is here, at registration, where the registrant
//     can act on the message instead of waiting on a code nobody delivers.
func ValidateRedirectURI(raw string) error {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return fmt.Errorf("redirect URI must not be empty or padded with spaces")
	}
	if strings.ContainsAny(raw, "*\n\r\t") {
		return fmt.Errorf("redirect URI %q contains a character that is never matched literally", raw)
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("redirect URI %q is not a URI: %w", raw, err)
	}
	if u.Scheme == "" {
		return fmt.Errorf("redirect URI %q must be absolute", raw)
	}
	if u.Fragment != "" || strings.Contains(raw, "#") {
		return fmt.Errorf("redirect URI %q must not carry a fragment", raw)
	}
	scheme := strings.ToLower(u.Scheme)
	host := u.Hostname()

	// A PRIVATE-USE scheme (RFC 8252 section 7.1): "com.example.app:/cb". It
	// carries no host, which is what distinguishes it from a web address whose
	// author forgot one.
	if host == "" {
		if scheme == "http" || scheme == "https" {
			return fmt.Errorf("redirect URI %q must name a host", raw)
		}
		if !strings.Contains(scheme, ".") {
			return fmt.Errorf(
				"redirect URI %q uses a private-use scheme with no dot; RFC 8252 section 7.1 requires a reverse-domain scheme such as com.example.app", raw)
		}
		return nil
	}

	if httpsec.CSPCannotStateHost(host) {
		return fmt.Errorf(
			"redirect URI %q uses an IPv6 address; the consent page's content security policy cannot state one, so the login would hang. Use 127.0.0.1 or localhost", raw)
	}
	if httpsec.IsLoopbackHost(host) {
		if !httpsec.IsLoopbackRedirectScheme(scheme) {
			return fmt.Errorf("loopback redirect URI %q must use http or https", raw)
		}
		return nil
	}
	if scheme != "https" {
		return fmt.Errorf("redirect URI %q must use https; only a loopback address may use http", raw)
	}
	return nil
}

// ValidateRedirectURIs checks a whole registered list and caps its size.
func ValidateRedirectURIs(uris []string) error {
	if len(uris) > maxRedirectURIs {
		return fmt.Errorf("an app may register at most %d redirect URIs, got %d", maxRedirectURIs, len(uris))
	}
	seen := make(map[string]bool, len(uris))
	for _, uri := range uris {
		if err := ValidateRedirectURI(uri); err != nil {
			return err
		}
		if seen[uri] {
			return fmt.Errorf("redirect URI %q is registered twice", uri)
		}
		seen[uri] = true
	}
	return nil
}

// MatchRedirectURI reports which registered address a presented redirect_uri
// matches, and whether it matched at all.
//
// It returns the REGISTERED form rather than a bare bool, because two callers
// need it: the consent page renders a label derived from the registration, and
// the token leg compares what the authorization stored.
//
// The comparison is exact, EXCEPT for a registered loopback address, where the
// port is free (RFC 8252 section 7.3). Everything else about a loopback match
// is still exact: the scheme, the host and the path must agree, so a
// registration of "http://127.0.0.1/callback" does not admit
// "http://127.0.0.1:5555/evil".
//
// A presented address is never normalized before the comparison. Normalizing
// is where an exact-match rule quietly stops being exact: "%2e%2e" and a
// trailing slash both decode to something a naive comparison then accepts.
func MatchRedirectURI(registered []string, presented string) (string, bool) {
	if presented == "" {
		return "", false
	}
	for _, candidate := range registered {
		if candidate == presented {
			return candidate, true
		}
		if loopbackPortInsensitiveMatch(candidate, presented) {
			return candidate, true
		}
	}
	return "", false
}

// loopbackPortInsensitiveMatch applies the RFC 8252 section 7.3 exception.
//
// BOTH sides must parse, both must be loopback, and every part except the port
// must agree exactly -- scheme, host, path, and the query, which a redirect URI
// may legitimately carry and which the authorization response appends to.
//
// The registered side is checked first and returns false for anything that is
// not loopback, so this can only ever WIDEN a loopback registration. A remote
// https registration is unaffected by it.
func loopbackPortInsensitiveMatch(registered, presented string) bool {
	reg, err := url.Parse(registered)
	if err != nil {
		return false
	}
	if !httpsec.IsLoopbackHost(reg.Hostname()) {
		return false
	}
	pres, err := url.Parse(presented)
	if err != nil {
		return false
	}
	return strings.EqualFold(reg.Scheme, pres.Scheme) &&
		httpsec.NormalizeHost(reg.Hostname()) == httpsec.NormalizeHost(pres.Hostname()) &&
		reg.Path == pres.Path &&
		reg.RawQuery == pres.RawQuery &&
		pres.Fragment == "" &&
		httpsec.IsLoopbackHost(pres.Hostname())
}

// redirectFormActionSource is the `form-action` source that admits ONE
// redirect target, for the per-request CSP the consent pages carry.
//
// The global policy states `form-action 'self'` alone. A browser matches
// form-action against EVERY hop of a submission's redirect chain, so a consent
// POST that redirects to https://app.example.com/callback is blocked on
// Chromium and WebKit with no server-side error at all. The page therefore
// carries its own policy naming exactly the origin THIS grant redirects to --
// which is narrower than the global relaxation it replaces, because it admits
// one origin for one request rather than a wildcard set for every page.
//
// A loopback target keeps a wildcard PORT, because the CLI binds an ephemeral
// one and CSP has no way to say "any port of this host" other than `:*`.
//
// An IPv6 literal cannot be stated at all: CSP's host-source grammar has no
// production for one, and Chromium ignores the whole entry and logs a console
// error. ValidateRedirectURI refuses such a target at registration for exactly
// this reason, so a stored redirect URI never reaches this function with one;
// the "" answer stays as the fail-closed backstop for a row that predates the
// rule or was written by hand.
func redirectFormActionSource(redirectURI string) string {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return ""
	}
	host := u.Hostname()
	if host == "" || strings.Contains(host, ":") {
		return ""
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		// A private-use scheme is a client-side handoff the browser resolves
		// itself; CSP cannot express it, and the redirect never leaves as an
		// http hop.
		return ""
	}
	if httpsec.IsLoopbackHost(host) {
		return scheme + "://" + host + ":*"
	}
	if port := u.Port(); port != "" {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}
