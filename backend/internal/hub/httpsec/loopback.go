package httpsec

import (
	"net"
	"slices"
	"strings"
)

// LoopbackHosts are the hosts a CLI login may redirect to.
//
// ONE list, because three places need the same answer and a comment saying
// they agree is not a mechanism:
//
//   - `isLoopbackURL` (hub/service) accepts exactly these as a `redirect_uri`.
//   - The CSP's `form-action` must admit the same set, or the browser blocks
//     the consent form's redirect hop and `leapmux control auth login` waits
//     until it times out. A browser matches `form-action` against EVERY hop of
//     a submission's redirect chain, so `'self'` alone is not enough.
//   - The CSP test asserted the set with a literal of its own.
//
// All three carried a comment claiming they matched, and no test connected
// them. A set that widens in one place and not the others is either a hole (the
// policy admits a host the redirect refuses) or an outage (the redirect offers
// a host the policy blocks), and neither shows up until a CLI login hangs.
//
// It lives here because `hub/frontend` and `hub/service` are siblings and this
// package is the leaf both already reach for.
var LoopbackHosts = []string{"127.0.0.1", "localhost", "::1"}

// LoopbackSchemes are the schemes a CLI login may redirect to.
//
// It lives beside LoopbackHosts for the same reason, and the reason is the
// same failure: the accepted set is spelled in two places. A CLI serves its
// callback over http, or over https with a local certificate, and nothing
// else -- the value reaches a Location header, so a hostname test alone
// accepts "javascript://127.0.0.1/%0aalert(1)", where the host IS loopback
// and the scheme executes.
//
// The CSP's `form-action` must admit the same schemes for the same hosts.
// A browser matches form-action against every hop of a submission's
// redirect chain, so a scheme the redirect accepts and the policy omits is
// a CLI login that hangs with nothing in any log.
var LoopbackSchemes = []string{"http", "https"}

// IsLoopbackRedirectScheme reports whether scheme is one a CLI callback can
// use. The comparison folds case, because a URL scheme is case-insensitive
// and "JavaScript" must not slip past a lower-case literal.
func IsLoopbackRedirectScheme(scheme string) bool {
	return slices.Contains(LoopbackSchemes, strings.ToLower(strings.TrimSpace(scheme)))
}

// NormalizeHost folds a host to the form the lists here are written in: no
// surrounding space, lower case, and no brackets. A URL authority carries
// an IPv6 literal in brackets (`[::1]`), and a bind address carries the
// same, so every caller that compares a host against a literal must strip
// them first.
func NormalizeHost(host string) string {
	return strings.Trim(strings.ToLower(strings.TrimSpace(host)), "[]")
}

// SplitBindHostPort splits a bind address into its host and its port, where
// the port keeps its leading colon so `host + port` rebuilds the input.
//
// net.SplitHostPort is not usable here. It rejects an address with no port
// ("hub.example.com"), and every caller must still report that host: they
// ask what the address specifies, not whether the hub can bind it. So the
// split is the last colon that is not inside an IPv6 literal's brackets.
//
// It lives here, beside the host predicates, because two packages need the
// same answer: settings.BaseURL and the passkey relying-party resolution.
// Both grew their own copy of this exact parse in one change, and a third
// caller spelled the wildcard set again -- which is the drift LoopbackHosts
// already exists to stop.
func SplitBindHostPort(listen string) (host, port string) {
	l := strings.TrimSpace(listen)
	if i := strings.LastIndex(l, ":"); i >= 0 && !strings.Contains(l[i+1:], "]") {
		return l[:i], l[i:]
	}
	return l, ""
}

// IsWildcardHost reports whether host is the "any address" bind, which
// answers on every address the machine holds and therefore specifies none.
//
// net.IP.IsUnspecified answers the whole CLASS, which a literal pair cannot:
// `0.0.0.0` and `::` are the two spellings anybody writes, but `[::0]`,
// `0:0:0:0:0:0:0:0` and `[0000:0000:0000:0000:0000:0000:0000:0000]` are the
// same bind and a list of literals reads every one of them as a real host.
// An empty host is NOT a wildcard here: the callers give it separate
// meanings, so each states its own rule for it.
func IsWildcardHost(host string) bool {
	ip := net.ParseIP(NormalizeHost(host))
	return ip != nil && ip.IsUnspecified()
}

// IsLoopbackHost reports whether host is one of LoopbackHosts, after
// NormalizeHost folds it. It is the membership test that keeps the list
// above the single source of truth: a caller that compares against its own
// copy of the three literals is the drift this package exists to remove.
//
// It answers the CLI-redirect question only. A caller that needs a
// different question -- "is a plain-HTTP page on this host a secure
// context", which grants the whole 127.0.0.0/8 block and every
// *.localhost name -- must state its own extra rules, because a browser
// answers those two questions differently.
func IsLoopbackHost(host string) bool {
	return slices.Contains(LoopbackHosts, NormalizeHost(host))
}
