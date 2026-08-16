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

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// Config is the effective configuration of the selected captcha
// provider, built from the settings snapshot. Exactly one settings
// pointer is non-nil — the one matching Provider — which keeps
// provider-specific knobs in provider-specific types and the stored
// schema free of cross-provider columns.
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

// Validate checks that the settings pointer matching Provider is present
// and valid. Unknown providers are refused here, so a hand-edited
// selection can never select a provider the manager cannot dispatch on.
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

// Effective assembles the selected provider's effective configuration
// from a settings snapshot. The stored documents are validated at
// consumption: the write paths validate before storing (per-key rules
// plus the SelectedConfigured cross rule), so a value that fails here was
// written outside them (direct SQL), and the built-in ALTCHA defaults
// keep login working instead of issuing unsolvable challenges or calling
// a provider with missing keys. The fallback preserves the verification
// on/off switch — a deliberately disabled hub stays disabled through
// corruption; swapping the provider to defaults is about solvability,
// not about overriding the admin's on/off decision. The hub and the
// admin CLI share this one definition of "effective".
//
// Effective is pure assembly and does not log: the second return carries
// the fallback reason ("" when the stored state held), and the caller
// that resolves per request (Manager.resolve) reports it through its
// warn-on-transition, the one degrade-logging mechanism — a bare warn
// here fired on every login attempt.
func Effective(s *settings.Snapshot) (Config, string) {
	enabled := CaptchaEnabledKey.Of(s)
	alias := CaptchaSelectedKey.Of(s)
	provider, err := ParseProvider(alias)
	if err != nil {
		return fallbackConfig(enabled), fmt.Sprintf("captcha selection %q specifies an unsupported provider", alias)
	}
	cfg := Config{Provider: provider, Enabled: enabled}
	switch provider {
	case ProviderAltcha:
		row := AltchaKey.Of(s)
		cfg.Altcha = &row.AltchaSettings
	case ProviderRecaptchaV3:
		row := RecaptchaV3Key.Of(s)
		cfg.RecaptchaV3 = &RecaptchaV3Settings{SiteKey: row.SiteKey, MinScore: row.MinScore}
	case ProviderTurnstile:
		row := TurnstileKey.Of(s)
		cfg.Turnstile = &TurnstileSettings{SiteKey: row.SiteKey}
	}
	if err := cfg.Validate(); err != nil {
		return fallbackConfig(enabled), fmt.Sprintf("captcha settings for %s are invalid: %v", ProviderAlias(provider), err)
	}
	return cfg, ""
}

// fallbackConfig is the invalid-settings fallback: built-in altcha
// defaults with the hub's own verification on/off switch.
func fallbackConfig(enabled bool) Config {
	cfg := DefaultConfig()
	cfg.Enabled = enabled
	return cfg
}
