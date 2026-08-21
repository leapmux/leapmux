package captcha

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
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

func TestClientPageURL(t *testing.T) {
	t.Run("prefers Origin", func(t *testing.T) {
		h := http.Header{}
		h.Set("Origin", "http://192.168.1.5:8080")
		h.Set("Referer", "https://ignored.example/path")
		assert.Equal(t, "http://192.168.1.5:8080", clientPageURL(h))
	})

	t.Run("falls back to Referer origin", func(t *testing.T) {
		h := http.Header{}
		h.Set("Referer", "http://192.168.1.5:8080/login?x=1")
		assert.Equal(t, "http://192.168.1.5:8080", clientPageURL(h))
	})

	t.Run("ignores null Origin and uses Referer", func(t *testing.T) {
		h := http.Header{}
		h.Set("Origin", "null")
		h.Set("Referer", "https://example.com/app")
		assert.Equal(t, "https://example.com", clientPageURL(h))
	})

	t.Run("empty when neither is usable", func(t *testing.T) {
		assert.Empty(t, clientPageURL(http.Header{}))
		h := http.Header{}
		h.Set("Origin", "not-a-url")
		h.Set("Referer", "/relative")
		assert.Empty(t, clientPageURL(h))
	})
}

func TestApplySecureContextGate(t *testing.T) {
	altchaOn := Config{Provider: ProviderAltcha, Enabled: true}
	turnstileOn := Config{Provider: ProviderTurnstile, Enabled: true}
	altchaOff := Config{Provider: ProviderAltcha, Enabled: false}

	t.Run("disables altcha on insecure HTTP", func(t *testing.T) {
		got := applySecureContextGate(altchaOn, "http://192.168.1.5:8080")
		assert.False(t, got.Enabled)
		assert.Equal(t, ProviderAltcha, got.Provider, "selection stays altcha for admin visibility")
	})

	t.Run("keeps altcha on https and localhost http", func(t *testing.T) {
		assert.True(t, applySecureContextGate(altchaOn, "https://example.com").Enabled)
		assert.True(t, applySecureContextGate(altchaOn, "http://localhost:8080").Enabled)
		assert.True(t, applySecureContextGate(altchaOn, "http://127.0.0.1").Enabled)
	})

	t.Run("never gates external providers", func(t *testing.T) {
		assert.True(t, applySecureContextGate(turnstileOn, "http://192.168.1.5").Enabled)
		recaptchaOn := Config{Provider: ProviderRecaptchaV3, Enabled: true}
		assert.True(t, applySecureContextGate(recaptchaOn, "http://192.168.1.5").Enabled)
	})

	t.Run("empty client URL leaves enablement alone", func(t *testing.T) {
		assert.True(t, applySecureContextGate(altchaOn, "").Enabled)
		assert.False(t, applySecureContextGate(altchaOff, "").Enabled)
		assert.False(t, applySecureContextGate(altchaOff, "http://192.168.1.5").Enabled)
	})
}

func TestClientPageURLContextRoundTrip(t *testing.T) {
	ctx := withClientPageURL(context.Background(), "http://192.168.1.5")
	assert.Equal(t, "http://192.168.1.5", clientPageURLFromCtx(ctx))
	assert.Empty(t, clientPageURLFromCtx(context.Background()))
}

func TestProviderRequiresSecureContext(t *testing.T) {
	assert.True(t, providerRequiresSecureContext(ProviderAltcha))
	assert.False(t, providerRequiresSecureContext(ProviderTurnstile))
	assert.False(t, providerRequiresSecureContext(ProviderRecaptchaV3))
}
