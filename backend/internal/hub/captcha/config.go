// Package captcha implements the hub's bot protection on the
// unauthenticated credential procedures (Login, SignUp,
// CompleteOAuthSignup): challenge issuance and token verification for a
// selectable set of providers (the built-in ALTCHA proof-of-work, Google
// reCAPTCHA v3, and Cloudflare Turnstile), plus the ConnectRPC
// interceptor that gates the procedures. The honeypot check runs
// regardless of provider and enablement.
package captcha

import (
	"fmt"
	"log/slog"

	"github.com/leapmux/leapmux/internal/hub/store"
)

// Config is the effective configuration of the selected captcha
// provider, built by overlaying that provider's stored settings JSON
// onto its defaults. Exactly one settings pointer is non-nil — the one
// matching Provider — which keeps provider-specific knobs in
// provider-specific types and the stored schema free of cross-provider
// columns.
type Config struct {
	Provider Provider `json:"provider"`
	Enabled  bool     `json:"enabled"`

	Altcha      *AltchaSettings      `json:"altcha,omitempty"`
	RecaptchaV3 *RecaptchaV3Settings `json:"recaptcha_v3,omitempty"`
	Turnstile   *TurnstileSettings   `json:"turnstile,omitempty"`
}

// DefaultConfig returns the safe out-of-the-box configuration: the
// built-in ALTCHA provider with its default parameters, enabled.
func DefaultConfig() Config {
	s := DefaultAltchaSettings()
	return Config{
		Provider: ProviderAltcha,
		Enabled:  true,
		Altcha:   &s,
	}
}

// Validate checks that the settings pointer matching Provider is present
// and valid. Unknown providers are refused here, so a hand-edited row
// can never select a provider the manager cannot dispatch on.
func (c Config) Validate() error {
	switch c.Provider {
	case ProviderAltcha:
		if c.Altcha == nil {
			return fmt.Errorf("altcha settings missing")
		}
		return c.Altcha.Validate()
	case ProviderRecaptchaV3:
		if c.RecaptchaV3 == nil {
			return fmt.Errorf("recaptcha_v3 settings missing")
		}
		return c.RecaptchaV3.Validate()
	case ProviderTurnstile:
		if c.Turnstile == nil {
			return fmt.Errorf("turnstile settings missing")
		}
		return c.Turnstile.Validate()
	default:
		return fmt.Errorf("unsupported captcha provider %q (supported: %v)", c.Provider, SupportedProviders())
	}
}

// SiteKey returns the public site key for external providers (what the
// frontend loads its widget with), and "" for altcha — whose equivalent
// input, the challenge, arrives per-submission via GetAltchaChallenge.
func (c Config) SiteKey() string {
	switch c.Provider {
	case ProviderRecaptchaV3:
		return c.RecaptchaV3.SiteKey
	case ProviderTurnstile:
		return c.Turnstile.SiteKey
	}
	return ""
}

// AltchaAlgorithm returns the active ALTCHA algorithm name, and "" when
// another provider is selected. GetSystemInfo reports it informationally.
func (c Config) AltchaAlgorithm() string {
	if c.Provider == ProviderAltcha && c.Altcha != nil {
		return c.Altcha.Algorithm
	}
	return ""
}

// Effective overlays a stored row onto that provider's defaults. A row
// is validated at consumption: the CLI validates before writing, so a
// row that fails here was written outside the CLI (direct SQL, a future
// migration), and the built-in ALTCHA defaults keep login working
// instead of issuing unsolvable challenges or calling a provider with
// missing keys. The hub and the admin CLI share this one definition of
// "effective".
func Effective(row *store.CaptchaConfig) Config {
	if row == nil {
		return DefaultConfig()
	}
	cfg := Config{Provider: row.Provider, Enabled: row.Enabled}
	switch cfg.Provider {
	case ProviderAltcha:
		s := parseAltchaSettings(row.Settings)
		cfg.Altcha = &s
	case ProviderRecaptchaV3:
		s := parseRecaptchaV3Settings(row.Settings)
		cfg.RecaptchaV3 = &s
	case ProviderTurnstile:
		s := parseTurnstileSettings(row.Settings)
		cfg.Turnstile = &s
	default:
		slog.Warn("captcha config row names an unsupported provider; using built-in defaults", "provider", row.Provider)
		return DefaultConfig()
	}
	if err := cfg.Validate(); err != nil {
		slog.Warn("captcha config row invalid; using built-in defaults", "provider", row.Provider, "error", err)
		return DefaultConfig()
	}
	return cfg
}
