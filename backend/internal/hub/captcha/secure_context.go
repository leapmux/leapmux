package captcha

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// clientPageURLKey carries the browser page URL (Origin, else Referer
// origin) on the request context so Describe / resolve / Verify can apply
// the secure-context gate without changing every Manager method signature.
type clientPageURLKey struct{}

// withClientPageURL returns ctx carrying the client page URL. An empty
// url is still stored so a missing-vs-empty distinction is unnecessary
// downstream — the gate treats both as "unknown" and leaves the stored
// enablement alone.
func withClientPageURL(ctx context.Context, pageURL string) context.Context {
	return context.WithValue(ctx, clientPageURLKey{}, pageURL)
}

// clientPageURLFromCtx returns the page URL stashed by the captcha
// interceptor, or "" when the call did not pass through it (tests that
// invoke Manager methods directly, non-HTTP callers).
func clientPageURLFromCtx(ctx context.Context) string {
	v, _ := ctx.Value(clientPageURLKey{}).(string)
	return v
}

// providerRequiresSecureContext reports whether the provider's browser
// widget needs a secure context (SubtleCrypto). Only ALTCHA does;
// Turnstile and reCAPTCHA v3 both work on plain HTTP pages.
func providerRequiresSecureContext(p Provider) bool {
	return p == ProviderAltcha
}

// clientPageURL extracts the browser page URL from request headers.
// Prefer Origin (the precise page origin on Connect POSTs from the SPA);
// fall back to the origin of Referer when Origin is absent. Empty when
// neither yields a usable absolute URL — the secure-context gate then
// leaves stored enablement alone (fail closed for non-browser callers).
func clientPageURL(h http.Header) string {
	if origin := strings.TrimSpace(h.Get("Origin")); origin != "" && origin != "null" {
		if _, err := url.ParseRequestURI(origin); err == nil {
			return origin
		}
	}
	ref := strings.TrimSpace(h.Get("Referer"))
	if ref == "" {
		return ""
	}
	u, err := url.ParseRequestURI(ref)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// isSecureContextURL mirrors the browser's isSecureContext rules for
// http(s) page URLs: https is always secure; http is secure only for
// localhost, 127.0.0.1, ::1, and *.localhost. Anything else (LAN IPs,
// bare hostnames over http, non-http schemes) is not a secure context —
// ALTCHA's SubtleCrypto digest is unavailable there.
func isSecureContextURL(raw string) bool {
	u, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return true
	case "http":
		host := strings.ToLower(u.Hostname())
		return host == "localhost" || host == "127.0.0.1" || host == "::1" || strings.HasSuffix(host, ".localhost")
	default:
		return false
	}
}

// applySecureContextGate runtime-disables captcha when the selected
// provider needs a secure context and the client page URL is known to be
// insecure. The settings row is not written — captcha.enabled stays true
// in the DB; only the effective Enabled flag flips for this request.
//
// An empty clientPageURL leaves enablement alone (fail closed): non-browser
// callers and tests that omit Origin keep the stored policy. External
// providers are never gated — they work on plain HTTP.
func applySecureContextGate(cfg Config, clientPageURL string) Config {
	if !cfg.Enabled || !providerRequiresSecureContext(cfg.Provider) || clientPageURL == "" {
		return cfg
	}
	if !isSecureContextURL(clientPageURL) {
		cfg.Enabled = false
	}
	return cfg
}
