package captcha

import (
	"net"
	"net/url"
	"strings"

	"github.com/leapmux/leapmux/internal/hub/httpsec"
	"github.com/leapmux/leapmux/internal/hub/settings"
)

// providerRequiresSecureContext reports whether the provider's browser
// widget needs a secure context (SubtleCrypto). Only ALTCHA does;
// Turnstile and reCAPTCHA v3 both work on plain HTTP pages.
func providerRequiresSecureContext(p Provider) bool {
	return p == ProviderAltcha
}

// parseBrowserURL parses a configured browser address for the two
// predicates below.
//
// Both of them read the scheme and the host only, so this refuses a value
// that carries no host: a relative path, a bare "http://", and anything
// url.ParseRequestURI rejects all answer false at the caller, and neither
// predicate has to repeat the check.
func parseBrowserURL(raw string) (*url.URL, bool) {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return nil, false
	}
	return u, true
}

// isSecureContextURL mirrors the browser's isSecureContext rules for
// http(s) page URLs: https is always secure; http is secure only on a
// loopback host. Anything else (LAN IPs, bare hostnames over http,
// non-http schemes) is not a secure context -- ALTCHA's SubtleCrypto digest
// is unavailable there.
func isSecureContextURL(u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		return isSecureContextHost(u.Hostname())
	default:
		return false
	}
}

// isSecureContextHost reports whether a plain-HTTP page on this host is
// still a secure context.
//
// This is a WIDER set than httpsec.LoopbackHosts, and the difference is
// deliberate. W3C Secure Contexts grants "Potentially Trustworthy" to the
// whole 127.0.0.0/8 block and to ::1/128, so `127.0.0.2` counts although a
// CLI redirect must not accept it. net.IP.IsLoopback answers exactly that
// range, and it folds the IPv4-mapped spelling (::ffff:127.0.0.1) as well,
// so no list of literals appears here at all.
//
// The same rules grant every *.localhost name, and a trailing root dot
// (`localhost.`) is the same name to a browser.
func isSecureContextHost(host string) bool {
	h := strings.TrimSuffix(httpsec.NormalizeHost(host), ".")
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback()
	}
	return h == "localhost" || strings.HasSuffix(h, ".localhost")
}

// altchaCanProtect reports whether ALTCHA is worth requiring on this hub.
//
// TWO questions, and the name states the conjunction because the gate's
// decision IS the conjunction:
//
//   - Can a browser RUN the widget? ALTCHA's proof of work needs
//     SubtleCrypto, which a page holds only in a secure context.
//   - Is there anything to PROTECT? ALTCHA counters automated sign-up and
//     sign-in abuse, which needs the hub to be reachable by somebody other
//     than the operator's own machine. A hub published at a loopback
//     address, or published nowhere at all, has no such audience.
//
// The answer comes from the hub's own configuration, and that is the point.
// The gate this replaced read the request's Origin header, so any caller
// could claim an insecure page and switch ALTCHA off for its own request --
// a client-chosen switch on a server-side security decision, in front of the
// only automation control Login, RequestAccountRecovery and the passkey Begin
// procedures have.
//
// A stand-down is therefore silent to the caller by design, and visible to
// the operator: Manager.noteStandDown reports it once, because an operator who
// stored captcha.enabled=true and gets no enforcement must be able to find
// out. See the two settings keys this reads for the remedy.
//
// There is no bind-address rung, and its absence is the rule rather than an
// omission. Without public_url and without TLS the hub serves plain HTTP, so
// a browser has a secure context ONLY at loopback -- and loopback is exactly
// the case with no audience. So no bind address can make ALTCHA both usable
// and useful, and the previous rung could only produce the wrong answer:
// assuming a wildcard bind was loopback made ALTCHA REQUIRED for a LAN page
// that could never solve it, and the remedy the form specified (set
// public_url) needed a credential the same block prevented.
func altchaCanProtect(s *settings.Snapshot) bool {
	raw := settings.KeyPublicURL.Of(s)
	if raw == "" {
		// No published address. TLS still settles it: a hub that terminates
		// TLS serves every page in a secure context, and a hub with a
		// certificate is a hub somebody reaches.
		return settings.KeySecureCookies.Of(s)
	}
	// ONE parse for both questions. They read the same address, and a second
	// url.ParseRequestURI of the same string can only agree with the first.
	u, ok := parseBrowserURL(raw)
	if !ok {
		return false
	}
	return isSecureContextURL(u) && !publishedAtLoopback(u)
}

// publishedAtLoopback reports whether public_url points at this machine.
//
// It reads the WIDER loopback set, the one a browser treats as potentially
// trustworthy, because the question is what a browser can reach: an operator
// who published http://127.0.0.2:4327 published no more of an audience than
// one who published localhost.
func publishedAtLoopback(u *url.URL) bool {
	return isSecureContextHost(u.Hostname())
}

// applySecureContextGate runtime-disables captcha when the selected provider
// needs a secure context this hub cannot serve, or serves to nobody. It does
// not write the settings row -- captcha.enabled keeps its stored value in
// the database; only the effective Enabled flag flips.
//
// applySecureContextGate never restricts an external provider: Turnstile and
// reCAPTCHA v3 work on plain HTTP pages, and neither needs an audience to be
// worth running.
func applySecureContextGate(cfg Config, altchaProtects bool) Config {
	if !cfg.Enabled || !providerRequiresSecureContext(cfg.Provider) {
		return cfg
	}
	if !altchaProtects {
		cfg.Enabled = false
	}
	return cfg
}

// EnabledEffective is the read-time rule for captcha.enabled: the value the
// hub ACTUALLY enforces, when the secure-context gate stands the stored one
// down. It reports (nil, false) when the two agree.
//
// It exists so an operator cannot read "enabled: true" for a control that
// verifies nothing. The stand-down is silent to the caller by design -- a
// bot must not learn which check is off -- and the stored settings row keeps
// its value, so the administration surface is the only place the gap can
// show. settings.WithEffective is the mechanism for exactly this shape, and
// the sibling rule on captcha.selected already reports the provider degrade
// through it.
//
// The signature matches settings.WithEffective, so the hub's wiring site
// registers this function itself and holds no captcha knowledge:
//
//	settings.WithEffective(captcha.CaptchaEnabledKey.Name(), captcha.EnabledEffective)
func EnabledEffective(s *settings.Snapshot) (any, bool) {
	cfg, _ := Effective(s)
	restricted := applySecureContextGate(cfg, altchaCanProtect(s))
	if restricted.Enabled == cfg.Enabled {
		return nil, false
	}
	return restricted.Enabled, true
}
