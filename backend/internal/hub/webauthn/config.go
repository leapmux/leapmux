package webauthn

import (
	"fmt"
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
// browser origin (desktop local-only mode, where listen is empty): passkeys
// are then cleanly unavailable instead of silently broken.
func RPConfigFromSettings(s *settings.Snapshot, listen string) (RPConfig, error) {
	baseURL := settings.BaseURL(s, listen)
	u, err := url.Parse(baseURL)
	if err != nil || u.Scheme == "" || u.Hostname() == "" {
		return RPConfig{}, fmt.Errorf("passkeys need a hub URL; set public_url or enable the TCP listener")
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
// reject the assertion — a guaranteed-dead ceremony after interactive
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

// allowedOrigins returns the base origin plus the loopback spellings when a
// hub bound on this host also answers on loopback. Browsers reach the same
// local hub through several host spellings, and the browser — not the
// server — decides which one the ceremony runs on.
func allowedOrigins(u *url.URL) []string {
	origin := u.Scheme + "://" + u.Host
	origins := []string{origin}
	if !servesLoopback(u.Hostname()) {
		return origins
	}
	port := u.Port()
	for _, alt := range []string{"localhost", "127.0.0.1", "[::1]"} {
		altURL := u.Scheme + "://" + alt
		if port != "" {
			altURL += ":" + port
		}
		if !originListed(origins, altURL) {
			origins = append(origins, altURL)
		}
	}
	return origins
}

// servesLoopback reports whether a hub bound on this host also answers on
// the loopback spellings: a loopback bind, a wildcard bind, or an empty host.
func servesLoopback(host string) bool {
	h := normalizeHost(host)
	// httpsec answers both halves, so this file spells neither the loopback
	// list nor the wildcard pair itself -- that list exists because several
	// packages need the same answer, and a comment claiming they agree is not
	// a mechanism. IsWildcardHost also covers the spellings a literal pair
	// misses ("[::0]", "0:0:0:0:0:0:0:0").
	return httpsec.IsLoopbackHost(h) || httpsec.IsWildcardHost(h)
}

func rpIDForHost(host string) string {
	return normalizeHost(host)
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
