package captcha

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// secureContextRaw answers isSecureContextURL for a raw configured string,
// so the table below keeps covering the values parseBrowserURL refuses.
// altchaCanProtect composes the two the same way.
func secureContextRaw(raw string) bool {
	u, ok := parseBrowserURL(raw)
	return ok && isSecureContextURL(u)
}

// loopbackRaw answers publishedAtLoopback for a raw configured string. An
// unparseable value publishes nothing, so it is not a loopback publication.
func loopbackRaw(raw string) bool {
	u, ok := parseBrowserURL(raw)
	return ok && publishedAtLoopback(u)
}

// TestParseBrowserURL pins the one parse both predicates share: it must
// refuse every value that carries no host, so neither predicate repeats the
// check and neither reads an empty host as a name.
func TestParseBrowserURL(t *testing.T) {
	t.Parallel()

	for _, raw := range []string{
		"https://example.com", "http://localhost:8080", "  https://hub.example.com  ",
		"http://[::1]:8080", "ftp://localhost",
	} {
		u, ok := parseBrowserURL(raw)
		if assert.Truef(t, ok, "%q parses", raw) {
			assert.NotEmptyf(t, u.Host, "%q carries a host", raw)
		}
	}
	for _, raw := range []string{
		"", "   ", "not a url", "http://", "/relative/path", "https://",
	} {
		_, ok := parseBrowserURL(raw)
		assert.Falsef(t, ok, "%q carries no host", raw)
	}
}

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
		assert.True(t, secureContextRaw(raw), "secure: %q", raw)
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
		assert.False(t, secureContextRaw(raw), "insecure: %q", raw)
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
		assert.Truef(t, loopbackRaw(raw), "%q publishes this machine only", raw)
	}
	for _, raw := range []string{
		"https://hub.example.com", "http://192.168.1.5:8080", "http://10.0.0.1",
		"", "not a url", "http://",
	} {
		assert.Falsef(t, loopbackRaw(raw), "%q is not a loopback publication", raw)
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
		// operator's own machine, so requiring a proof of work there gains
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

// standDownWarning is the substring the operator-facing warn line carries.
// The test matches the message rather than the whole line, so the field
// order the handler chooses cannot break it.
const standDownWarning = "captcha is enabled in the settings but verifies nothing"

// TestSecureContextStandDownIsReportedOnce pins the one thing an operator
// can act on.
//
// A hub behind a TLS-terminating reverse proxy whose operator set NEITHER
// public_url NOR secure_cookies runs on both defaults, keeps
// captcha.enabled true in the database, and verifies nothing but the
// honeypot. Nothing about the stored settings looks wrong there, so the
// hub must say so itself -- once, because the gate runs on every protected
// submission.
func TestSecureContextStandDownIsReportedOnce(t *testing.T) {
	// CaptureDefaultLogger swaps a process-global logger, so this test and
	// its subtests must stay sequential.
	ctx := context.Background()

	t.Run("both defaults warn once, and specify the remedy", func(t *testing.T) {
		e := newTestManagerPublishedAt(t, "", false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		logs := testutil.CaptureDefaultLogger(t)

		require.False(t, e.m.Describe(ctx).Enabled, "precondition: the gate stands ALTCHA down")
		for range 5 {
			assert.False(t, e.m.Describe(ctx).Enabled)
			require.NoError(t, e.m.Verify(ctx, "login", ""))
		}

		out := logs.String()
		assert.Equal(t, 1, strings.Count(out, standDownWarning),
			"the stand-down reports on transition, never once per request")
		assert.Contains(t, out, "level=WARN")
		assert.Contains(t, out, settings.KeyPublicURL.Name(),
			"the line must specify the setting that repairs it")
	})

	t.Run("a hub that enforces reports nothing", func(t *testing.T) {
		e := newTestManagerPublishedAt(t, "https://hub.example.com", false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		logs := testutil.CaptureDefaultLogger(t)

		require.True(t, e.m.Describe(ctx).Enabled)
		assert.NotContains(t, logs.String(), standDownWarning)
		assert.NotContains(t, logs.String(), "captcha enforcement restored",
			"a hub that never stood down has nothing to restore")
	})

	t.Run("the notice follows the setting in both directions", func(t *testing.T) {
		e := newTestManagerPublishedAt(t, "", false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		logs := testutil.CaptureDefaultLogger(t)

		require.False(t, e.m.Describe(ctx).Enabled)
		require.Equal(t, 1, strings.Count(logs.String(), standDownWarning))

		// The operator applies the remedy.
		require.NoError(t, settings.KeyPublicURL.Set(ctx, e.set, "https://hub.example.com"))
		require.True(t, e.m.Describe(ctx).Enabled)
		assert.Contains(t, logs.String(), "captcha enforcement restored")

		// And takes it away again: a SECOND stand-down reports again,
		// because the state changed again.
		logs.Reset()
		require.NoError(t, settings.KeyPublicURL.Set(ctx, e.set, "http://192.168.1.5:8080"))
		require.False(t, e.m.Describe(ctx).Enabled)
		assert.Equal(t, 1, strings.Count(logs.String(), standDownWarning))
	})

	t.Run("a stored disabled captcha is not a stand-down", func(t *testing.T) {
		// The hub took nothing away: the operator turned captcha off. The
		// notice exists for the gap between the stored value and the
		// effective one, and there is no gap here.
		e := newTestManagerPublishedAt(t, "", false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		require.NoError(t, CaptchaEnabledKey.Set(ctx, e.set, false))
		logs := testutil.CaptureDefaultLogger(t)

		require.False(t, e.m.Describe(ctx).Enabled)
		assert.NotContains(t, logs.String(), standDownWarning)
	})

	t.Run("solo mode reports nothing", func(t *testing.T) {
		// Solo runs no captcha at all, so there is no stored value for the
		// effective one to differ from.
		e := newTestManagerPublishedAt(t, "", true)
		logs := testutil.CaptureDefaultLogger(t)

		require.False(t, e.m.Describe(ctx).Enabled)
		assert.NotContains(t, logs.String(), standDownWarning)
	})

	t.Run("an external provider never stands down", func(t *testing.T) {
		stub := newSiteverifyStub(t)
		stub.body = `{"success":true,"action":"login"}`
		e := newTestManagerPublishedAt(t, "", false, WithTurnstileEndpoint(stub.server.URL))
		activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")
		logs := testutil.CaptureDefaultLogger(t)

		require.True(t, e.m.Describe(ctx).Enabled)
		assert.NotContains(t, logs.String(), standDownWarning)
	})
}

// TestEnabledEffective pins the value the administration surface reports
// beside the stored one.
//
// An operator who reads "enabled: true" for a control that verifies nothing
// has no way to find the problem: the stored row is right, the gate is
// silent to the caller, and both defaults produce the stand-down.
func TestEnabledEffective(t *testing.T) {
	ctx := context.Background()

	snap := func(t *testing.T, publicURL string) *settings.Snapshot {
		t.Helper()
		e := newTestManagerPublishedAt(t, publicURL, false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		return e.set.Snapshot(ctx)
	}

	t.Run("a stood-down hub reports false", func(t *testing.T) {
		value, override := EnabledEffective(snap(t, ""))
		require.True(t, override, "the stored true and the enforced false must not read alike")
		assert.Equal(t, false, value)
	})

	t.Run("an enforcing hub overrides nothing", func(t *testing.T) {
		value, override := EnabledEffective(snap(t, "https://hub.example.com"))
		assert.False(t, override, "the stored value already is the effective one")
		assert.Nil(t, value)
	})

	t.Run("a stored disabled captcha overrides nothing", func(t *testing.T) {
		e := newTestManagerPublishedAt(t, "", false)
		applyTestAltchaSettings(t, e, cheapAltchaSettings)
		require.NoError(t, CaptchaEnabledKey.Set(ctx, e.set, false))

		value, override := EnabledEffective(e.set.Snapshot(ctx))
		assert.False(t, override, "there is no gap between the stored value and the effective one")
		assert.Nil(t, value)
	})

	t.Run("an external provider overrides nothing", func(t *testing.T) {
		stub := newSiteverifyStub(t)
		stub.body = `{"success":true,"action":"login"}`
		e := newTestManagerPublishedAt(t, "", false, WithTurnstileEndpoint(stub.server.URL))
		activateExternal(t, e, ProviderTurnstile, `{"site_key":"1x00000000000000000000AA"}`, "secret-key")

		_, override := EnabledEffective(e.set.Snapshot(ctx))
		assert.False(t, override, "the gate never restricts an external provider")
	})

	t.Run("the rule and the manager agree", func(t *testing.T) {
		// Two readers of one rule. The RPC surface calls EnabledEffective and
		// the request path calls Describe; a hub that reported one answer and
		// enforced the other would be the defect this rule exists to remove.
		for _, publicURL := range []string{"", "https://hub.example.com", "http://192.168.1.5:8080", "http://localhost:4327"} {
			e := newTestManagerPublishedAt(t, publicURL, false)
			applyTestAltchaSettings(t, e, cheapAltchaSettings)

			value, override := EnabledEffective(e.set.Snapshot(ctx))
			enforced := e.m.Describe(ctx).Enabled
			if override {
				assert.Equalf(t, enforced, value, "public_url %q", publicURL)
				continue
			}
			assert.Equalf(t, CaptchaEnabledKey.Of(e.set.Snapshot(ctx)), enforced, "public_url %q", publicURL)
		}
	})
}
