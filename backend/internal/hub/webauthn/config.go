package webauthn

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// RPConfig carries the relying-party parameters derived from hub settings.
type RPConfig struct {
	// RPID is the default RP ID: the host of the hub's base origin.
	RPID          string
	RPDisplayName string
	// RPOrigins lists every browser origin the hub accepts. Origins come
	// only from configured settings and the listen address — never from a
	// client Origin/Referer header.
	RPOrigins []string
}

// RPConfigFromSettings builds RP parameters from the public_url setting and
// the process listen address. It returns an error when the hub has no usable
// browser origin (desktop local-only mode, where listen is empty; or a hub
// published at a bare IP address): passkeys are then cleanly unavailable
// instead of silently broken.
func RPConfigFromSettings(s *settings.Snapshot, listen string) (RPConfig, error) {
	baseURL := settings.BaseURL(s, listen)
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return RPConfig{}, fmt.Errorf("passkeys need a hub URL; set public_url or enable the TCP listener")
	}
	// A REMOTE IP address has no usable RP ID. WebAuthn §5.1.3 requires a
	// valid domain string and excludes the address literal, so a ceremony
	// under an IP RP ID can never complete -- the browser refuses it, and
	// go-webauthn now refuses to build one. Loopback is the one case with an
	// answer, because "localhost" names the same host and IS a domain.
	if isIPHost(u.Hostname()) && !httpsec.IsLoopbackHost(u.Hostname()) {
		return RPConfig{}, fmt.Errorf(
			"passkeys need a hub URL with a domain name; %q is an IP address, which WebAuthn forbids as a relying-party ID",
			u.Hostname())
	}
	return RPConfig{
		RPID:          rpIDForHost(u.Hostname()),
		RPDisplayName: "LeapMux",
		RPOrigins:     allowedOrigins(u),
	}, nil
}

// RPIDForOrigin returns the RP ID for a browser origin the hub allows, and
// whether the origin is allowed at all. The match is on the full origin
// (scheme, host, port): a host-only match would admit a different port or
// scheme, the browser would accept the ceremony (the RP ID is still a
// suffix of the page host), and the finish-time origin check would then
// reject the assertion — a ceremony that cannot succeed, after interactive
// biometric work. An unallowed or unparseable origin reports allowed=false
// so Begin can refuse it with a clear error. An empty origin (a non-browser
// client without an Origin header) keeps the default RPID: there is no
// browser ceremony to mislead.
func (c RPConfig) RPIDForOrigin(origin string) (rpID string, allowed bool) {
	if origin == "" {
		return c.RPID, true
	}
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return "", false
	}
	candidate := u.Scheme + "://" + u.Host
	for _, a := range c.RPOrigins {
		if strings.EqualFold(a, candidate) {
			return rpIDForHost(u.Hostname()), true
		}
	}
	return "", false
}

// allowedOrigins returns the base origin plus the loopback name when a hub
// bound on this host also answers on loopback. Browsers reach the same local
// hub through several host spellings, and the browser — not the server —
// decides which one the ceremony runs on.
//
// An IP-LITERAL spelling is left out, and that is the whole of the loopback
// handling: only "localhost" survives. A browser requires the RP ID to equal
// the page's effective domain or be a registrable-domain suffix of it, and
// the only valid RP ID for loopback is "localhost", which is neither for
// "127.0.0.1". So a ceremony started on http://127.0.0.1 cannot complete
// under ANY RP ID — under "127.0.0.1" the RP ID is not a domain, and under
// "localhost" it does not match the page. Listing the origin would light up
// every passkey affordance (passkeysRunnableForOrigin reads this list) on a
// page where create() can only raise SecurityError. Leaving it out turns
// that into "passkeys unavailable here", which is what is true.
func allowedOrigins(u *url.URL) []string {
	var origins []string
	if !isIPHost(u.Hostname()) {
		origins = append(origins, u.Scheme+"://"+u.Host)
	}
	if !servesLoopback(u.Hostname()) {
		return origins
	}
	altURL := u.Scheme + "://localhost"
	if port := u.Port(); port != "" {
		altURL += ":" + port
	}
	if !originListed(origins, altURL) {
		origins = append(origins, altURL)
	}
	return origins
}

// isIPHost reports whether host is an address literal rather than a domain
// name, in every spelling this hub sees: "127.0.0.1", "::1" and "[::1]"
// (normalizeHost strips the brackets before the parse).
func isIPHost(host string) bool {
	return net.ParseIP(normalizeHost(host)) != nil
}

// servesLoopback reports whether a hub bound on this host also answers on
// the loopback spellings. Two binds do: a loopback bind, and a wildcard bind.
//
// An EMPTY host reports false. httpsec.IsWildcardHost states that an empty
// host is not a wildcard there, and that each caller must give it its own
// meaning; here it means "no host at all", which serves nothing. The case is
// unreachable in any event -- RPConfigFromSettings refuses a base URL with an
// empty hostname before allowedOrigins runs -- so the answer is the safe one
// rather than a rule the code depends on.
func servesLoopback(host string) bool {
	h := normalizeHost(host)
	// httpsec answers both halves, so this file spells neither the loopback
	// list nor the wildcard pair itself -- that list exists because several
	// packages need the same answer, and a comment claiming they agree is not
	// a mechanism. IsWildcardHost also covers the spellings a literal pair
	// misses ("[::0]", "0:0:0:0:0:0:0:0").
	return httpsec.IsLoopbackHost(h) || httpsec.IsWildcardHost(h)
}

// rpIDForHost returns the RP ID for a host the hub serves.
//
// Every loopback spelling collapses to "localhost". WebAuthn §5.1.3 requires
// the RP ID to be a valid domain string and names the address literal as the
// case it excludes, so "127.0.0.1" and "::1" are not RP IDs at all --
// go-webauthn rejects them where they are configured, and before it did, the
// browser rejected them at create(). "localhost" names the same host and is
// the single-label domain the spec keeps.
func rpIDForHost(host string) string {
	h := normalizeHost(host)
	if httpsec.IsLoopbackHost(h) {
		return "localhost"
	}
	return h
}

func normalizeHost(host string) string {
	return httpsec.NormalizeHost(host)
}

func originListed(origins []string, candidate string) bool {
	for _, o := range origins {
		if strings.EqualFold(o, candidate) {
			return true
		}
	}
	return false
}
