package captcha

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

func TestIsSecureContextURL(t *testing.T) {
	secure := []string{
		"https://example.com",
		"https://example.com:443/path",
		"http://localhost",
		"http://localhost:8080",
		"http://127.0.0.1",
		"http://127.0.0.1:3000/login",
		"http://[::1]/",
		"http://[::1]:8080",
		"http://app.localhost",
		"http://foo.bar.localhost:5173",
		"HTTPS://Example.COM",
		"HTTP://LOCALHOST",
	}
	for _, raw := range secure {
		assert.True(t, isSecureContextURL(raw), "secure: %q", raw)
	}

	insecure := []string{
		"http://192.168.1.5",
		"http://192.168.1.5:8080",
		"http://example.com",
		"http://10.0.0.1",
		"http://myhost.local",
		"file:///tmp/index.html",
		"ftp://localhost",
		"",
		"not a url",
		"http://",
	}
	for _, raw := range insecure {
		assert.False(t, isSecureContextURL(raw), "insecure: %q", raw)
	}
}

// TestIsSecureContextHost pins the set W3C Secure Contexts calls
// potentially trustworthy, which is WIDER than httpsec.LoopbackHosts.
func TestIsSecureContextHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"localhost", "LOCALHOST", "localhost.", "app.localhost", "app.localhost.",
		"127.0.0.1", "127.0.0.2", "127.255.255.254", "::1", "[::1]",
		"::ffff:127.0.0.1",
	} {
		assert.Truef(t, isSecureContextHost(host), "%q is potentially trustworthy", host)
	}
	for _, host := range []string{
		"", "192.168.1.5", "10.0.0.1", "example.com", "notlocalhost",
		"localhost.evil.test", "fe80::1", "0.0.0.0",
	} {
		assert.Falsef(t, isSecureContextHost(host), "%q is not potentially trustworthy", host)
	}
}

func TestProviderRequiresSecureContext(t *testing.T) {
	t.Parallel()

	assert.True(t, providerRequiresSecureContext(ProviderAltcha))
	assert.False(t, providerRequiresSecureContext(ProviderTurnstile))
	assert.False(t, providerRequiresSecureContext(ProviderRecaptchaV3))
}

// TestPublishedAtLoopback pins the "is there an audience" half of the gate.
//
// It reads the WIDER loopback set on purpose, so 127.0.0.2 counts although
// httpsec.LoopbackHosts refuses it for a CLI redirect: the question here is
// what a browser can reach, not what a redirect may accept.
func TestPublishedAtLoopback(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"http://localhost", "http://localhost:4327", "https://localhost:4327",
		"http://127.0.0.1:4327", "http://127.0.0.2:4327", "http://[::1]:4327",
		"http://app.localhost:5173", "HTTP://LOCALHOST:4327",
	} {
		assert.Truef(t, publishedAtLoopback(raw), "%q publishes this machine only", raw)
	}
	for _, raw := range []string{
		"https://hub.example.com", "http://192.168.1.5:8080", "http://10.0.0.1",
		"", "not a url", "http://",
	} {
		assert.Falsef(t, publishedAtLoopback(raw), "%q is not a loopback publication", raw)
	}
}

// TestAltchaCanProtectReadsOnlyHubConfig pins the two questions the gate
// asks, and that neither of them is a request.
func TestAltchaCanProtectReadsOnlyHubConfig(t *testing.T) {
	ctx := context.Background()

	// A real settings store per case: the gate reads a Snapshot, and
	// building one any other way would test a fake.
	snap := func(t *testing.T, publicURL string, secureCookies bool) *settings.Snapshot {
		t.Helper()
		e := newTestManagerPublishedAt(t, publicURL, false)
		if secureCookies {
			require.NoError(t, settings.KeySecureCookies.Set(ctx, e.set, true))
		}
		return e.set.Snapshot(ctx)
	}

	t.Run("public_url outranks TLS, in both directions", func(t *testing.T) {
		assert.True(t, altchaCanProtect(snap(t, "https://hub.example.com", false)),
			"a TLS-terminating proxy in front of a plain-HTTP LAN bind")
		assert.False(t, altchaCanProtect(snap(t, "http://192.168.1.5:8080", true)),
			"a published plain-HTTP LAN URL cannot run the widget, whatever secure_cookies says")
	})

	t.Run("a loopback publication has no audience to protect", func(t *testing.T) {
		// The widget RUNS at either address. Neither one reaches past the
		// operator's own machine, so requiring a proof of work there buys
		// nothing and only makes the first-run form harder.
		assert.False(t, altchaCanProtect(snap(t, "http://localhost:8080", false)))
		assert.False(t, altchaCanProtect(snap(t, "https://127.0.0.1:8443", true)))
	})

	t.Run("with nothing published, TLS settles it", func(t *testing.T) {
		assert.True(t, altchaCanProtect(snap(t, "", true)),
			"a hub with a certificate is a hub somebody reaches")
		// The case that made ALTCHA required on a page that could never
		// solve it. A hub that publishes nothing and terminates no TLS
		// serves plain HTTP, so the only browser with a secure context is
		// one at loopback -- and that browser is the operator's own.
		assert.False(t, altchaCanProtect(snap(t, "", false)))
	})
}

func TestApplySecureContextGate(t *testing.T) {
	t.Parallel()

	altchaOn := Config{Enabled: true, Provider: ProviderAltcha}
	altchaOff := Config{Enabled: false, Provider: ProviderAltcha}
	turnstileOn := Config{Enabled: true, Provider: ProviderTurnstile}
	recaptchaOn := Config{Enabled: true, Provider: ProviderRecaptchaV3}

	t.Run("altcha stands down when it cannot run or has nobody to protect", func(t *testing.T) {
		got := applySecureContextGate(altchaOn, false)
		assert.False(t, got.Enabled)
		assert.Equal(t, ProviderAltcha, got.Provider, "the gate flips Enabled only")
	})

	t.Run("altcha stays enabled on a published secure-context hub", func(t *testing.T) {
		assert.True(t, applySecureContextGate(altchaOn, true).Enabled)
	})

	t.Run("the gate never restricts an external provider", func(t *testing.T) {
		assert.True(t, applySecureContextGate(turnstileOn, false).Enabled)
		assert.True(t, applySecureContextGate(recaptchaOn, false).Enabled)
	})

	t.Run("a disabled config stays disabled", func(t *testing.T) {
		assert.False(t, applySecureContextGate(altchaOff, true).Enabled)
		assert.False(t, applySecureContextGate(altchaOff, false).Enabled)
	})
}
