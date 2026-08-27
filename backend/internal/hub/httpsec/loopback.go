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
// a host the policy blocks), and neither appears until a CLI login hangs.
//
// It lives here because `hub/frontend` and `hub/service` are siblings and this
// package is the leaf that both already import.
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
// and "JavaScript" must not evade a lower-case literal.
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
// ask what the address SPECIFIES, not whether the hub can bind it.
//
// A WHOLE-ADDRESS IP test comes first, and it is what makes an unbracketed
// IPv6 literal split correctly. "::1" has no port, but the last colon in it
// looks exactly like a port separator, so the split alone returned the host
// ":" and the port ":1" -- and ":" is neither loopback nor a wildcard, so
// "::" and "::0" missed IsWildcardHost below although settings/keys.go
// promises every wildcard spelling resolves alike. An address that parses
// whole as an IP carries no port by construction, whatever its colons say.
//
// After that test the split is the last colon outside an IPv6 literal's
// brackets, so "[::1]:8080" and "0:0:0:0:0:0:0:0:4327" both keep their port.
//
// It lives HERE, beside the host predicates, because it applies one of them:
// NormalizeHost folds the brackets that the IP test must not see. The result
// then goes straight into IsWildcardHost or IsLoopbackHost, so the parse and
// the tests that read it share one file and one notion of a host. There is
// one production caller today -- settings.BaseURL. The passkey relying party
// reaches the same answer through that function rather than through this one.
func SplitBindHostPort(listen string) (host, port string) {
	l := strings.TrimSpace(listen)
	if net.ParseIP(NormalizeHost(l)) != nil {
		return l, ""
	}
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
