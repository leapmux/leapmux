// Package captcha implements the hub's bot protection on the
// unauthenticated credential procedures (Login, SignUp,
// CompleteOAuthSignup): challenge issuance and token verification for a
// selectable set of providers (the built-in ALTCHA proof-of-work, Google
// reCAPTCHA v3, and Cloudflare Turnstile), plus the ConnectRPC
// interceptor that controls access to the procedures. The honeypot check
// runs regardless of provider and enablement.
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

// DisabledConfig returns what a hub that does not run the captcha
// subsystem reports: the built-in defaults with verification off. Solo
// mode and the nil-Captcha service wiring both answer with it, so "no
// captcha" has one definition.
func DisabledConfig() Config {
	cfg := DefaultConfig()
	cfg.Enabled = false
	return cfg
}

// defaultConfigFor returns a provider's defaults with no stored row
// behind them — the base an admin CLI edit overlays when the provider has
// never been configured. Enabled matches a freshly provisioned row.
func defaultConfigFor(provider Provider) Config {
	if spec, ok := specFor(provider); ok {
		return spec.defaults()
	}
	return DefaultConfig()
}

// Validate checks that the settings pointer matching Provider is present
// and valid. Unknown providers are refused here, so a hand-edited row
// can never select a provider the manager cannot dispatch on.
func (c Config) Validate() error {
	spec, ok := specFor(c.Provider)
	if !ok {
		return fmt.Errorf("unsupported captcha provider %q (supported: %v)", c.Provider, SupportedProviders())
	}
	return spec.validate(c)
}

// SiteKey returns the public site key for external providers (what the
// frontend loads its widget with), and "" for altcha — whose equivalent
// input, the challenge, arrives per-submission via GetAltchaChallenge.
func (c Config) SiteKey() string {
	if spec, ok := specFor(c.Provider); ok {
		return spec.siteKey(c)
	}
	return ""
}

// AltchaAlgorithm returns the active ALTCHA algorithm name, and "" when
// another provider is selected. GetSystemInfo reports it informationally.
func (c Config) AltchaAlgorithm() string {
	if spec, ok := specFor(c.Provider); ok {
		return spec.altchaAlgorithm(c)
	}
	return ""
}

// Effective overlays a stored row onto that provider's defaults. A row
// is validated at consumption: the CLI validates before writing, so a
// row that fails here was written outside the CLI (direct SQL, a future
// migration), and the built-in ALTCHA defaults keep login working
// instead of issuing unsolvable challenges or calling a provider with
// missing keys. The fallback preserves the row's Enabled bit — a
// deliberately disabled hub stays disabled through corruption; swapping
// the provider to defaults is about solvability, not about overriding the
// admin's on/off decision. The hub and the admin CLI share this one
// definition of "effective".
func Effective(row *store.CaptchaConfig) Config {
	if row == nil {
		return DefaultConfig()
	}
	spec, ok := specFor(row.Provider)
	if !ok {
		slog.Warn("captcha config row specifies an unsupported provider; using built-in defaults", "provider", row.Provider)
		return fallbackConfig(row)
	}
	cfg := Config{Provider: row.Provider, Enabled: row.Enabled}
	spec.applySettings(&cfg, row.Settings)
	if err := cfg.Validate(); err != nil {
		slog.Warn("captcha config row invalid; using built-in defaults", "provider", row.Provider, "error", err)
		return fallbackConfig(row)
	}
	return cfg
}

// fallbackConfig is the invalid-row fallback: built-in altcha defaults
// with the row's own Enabled bit.
func fallbackConfig(row *store.CaptchaConfig) Config {
	cfg := DefaultConfig()
	cfg.Enabled = row.Enabled
	return cfg
}
