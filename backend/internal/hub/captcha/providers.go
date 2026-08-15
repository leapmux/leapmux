package captcha

import (
	"context"
	"fmt"
	"sort"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// Provider identifies the captcha provider. It is the CaptchaProvider
// proto enum verbatim (the same type the wire field and the database
// column carry), following the AgentProvider precedent: enum numbers are
// persisted as plain INTEGERs and cross every boundary as the generated
// type.
type Provider = leapmuxv1.CaptchaProvider

const (
	// ProviderAltcha is the built-in, self-hosted proof-of-work provider:
	// no third party, no verification egress, challenges issued by the hub
	// itself via GetAltchaChallenge.
	ProviderAltcha Provider = leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_ALTCHA
	// ProviderRecaptchaV3 is Google reCAPTCHA v3: invisible and
	// score-based. Verification calls Google's siteverify endpoint with
	// the row's secret and accepts tokens whose action matches and whose
	// score clears the configured minimum.
	ProviderRecaptchaV3 Provider = leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_RECAPTCHA_V3
	// ProviderTurnstile is Cloudflare Turnstile: a visible-or-invisible
	// checkbox challenge rendered from Cloudflare's script. Verification
	// calls Cloudflare's siteverify endpoint and accepts tokens whose
	// action matches.
	ProviderTurnstile Provider = leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_TURNSTILE
)

// providerSpec is the per-provider decision surface the shared captcha
// code dispatches on, following the agent Provider pattern (see
// backend/internal/worker/agent/provider.go): every provider-specific
// decision lives in the provider's spec, registered once in
// providerSpecs, and shared code looks the spec up instead of switching
// on the enum. Adding a provider means implementing this interface and
// adding one registry entry — the alias, defaults, settings parsing,
// validation, site-key accessor, and verification all follow.
type providerSpec interface {
	// alias is the provider's human-facing name — the admin CLI's
	// --provider flag, the `captcha show` output, the metrics label, and
	// the keystore AAD all use it, never the CAPS proto names
	// (the AgentProvider/agentlabels pattern).
	alias() string
	// defaults returns the provider's configuration with no stored row
	// behind it (Enabled matches a freshly provisioned row).
	defaults() Config
	// applySettings parses a stored row's settings JSON onto cfg's
	// provider-specific settings pointer.
	applySettings(cfg *Config, raw string)
	// validate checks that cfg's settings pointer for this provider is
	// present and passes the provider's own validation.
	validate(cfg Config) error
	// siteKey returns the public site key external frontends load their
	// widget with; "" when the provider has none (altcha's equivalent
	// input, the challenge, arrives per submission via GetAltchaChallenge).
	siteKey(cfg Config) string
	// altchaAlgorithm reports the active ALTCHA algorithm name; other
	// providers keep the base's "" (GetSystemInfo reports it
	// informationally).
	altchaAlgorithm(cfg Config) string
	// verify checks one token minted under the given action.
	verify(m *Manager, ctx context.Context, res *resolvedConfig, action, payload string) error
}

// baseProviderSpec carries the interface's empty defaults so a spec
// implements only what it has: providers without a public site key or an
// ALTCHA algorithm inherit the "" answers (the noopProvider pattern).
type baseProviderSpec struct{}

func (baseProviderSpec) siteKey(Config) string         { return "" }
func (baseProviderSpec) altchaAlgorithm(Config) string { return "" }

type altchaSpec struct{ baseProviderSpec }

func (altchaSpec) alias() string { return "altcha" }

func (altchaSpec) defaults() Config { return DefaultConfig() }

func (altchaSpec) applySettings(cfg *Config, raw string) {
	s := parseAltchaSettings(raw)
	cfg.Altcha = &s
}

func (altchaSpec) validate(cfg Config) error {
	if cfg.Altcha == nil {
		return fmt.Errorf("altcha settings missing")
	}
	return cfg.Altcha.Validate()
}

func (altchaSpec) altchaAlgorithm(cfg Config) string {
	if cfg.Altcha != nil {
		return cfg.Altcha.Algorithm
	}
	return ""
}

func (altchaSpec) verify(m *Manager, ctx context.Context, res *resolvedConfig, action, payload string) error {
	return m.verifyAltcha(ctx, res, payload)
}

type recaptchaSpec struct{ baseProviderSpec }

func (recaptchaSpec) alias() string { return "recaptcha_v3" }

func (recaptchaSpec) defaults() Config {
	s := DefaultRecaptchaV3Settings()
	return Config{Provider: ProviderRecaptchaV3, Enabled: true, RecaptchaV3: &s}
}

func (recaptchaSpec) applySettings(cfg *Config, raw string) {
	s := parseRecaptchaV3Settings(raw)
	cfg.RecaptchaV3 = &s
}

func (recaptchaSpec) validate(cfg Config) error {
	if cfg.RecaptchaV3 == nil {
		return fmt.Errorf("recaptcha_v3 settings missing")
	}
	return cfg.RecaptchaV3.Validate()
}

func (recaptchaSpec) siteKey(cfg Config) string {
	if cfg.RecaptchaV3 != nil {
		return cfg.RecaptchaV3.SiteKey
	}
	return ""
}

func (recaptchaSpec) verify(m *Manager, ctx context.Context, res *resolvedConfig, action, payload string) error {
	return m.verifyRecaptcha(ctx, string(res.secret), payload, action, res.cfg.RecaptchaV3.MinScore)
}

type turnstileSpec struct{ baseProviderSpec }

func (turnstileSpec) alias() string { return "turnstile" }

func (turnstileSpec) defaults() Config {
	s := DefaultTurnstileSettings()
	return Config{Provider: ProviderTurnstile, Enabled: true, Turnstile: &s}
}

func (turnstileSpec) applySettings(cfg *Config, raw string) {
	s := parseTurnstileSettings(raw)
	cfg.Turnstile = &s
}

func (turnstileSpec) validate(cfg Config) error {
	if cfg.Turnstile == nil {
		return fmt.Errorf("turnstile settings missing")
	}
	return cfg.Turnstile.Validate()
}

func (turnstileSpec) siteKey(cfg Config) string {
	if cfg.Turnstile != nil {
		return cfg.Turnstile.SiteKey
	}
	return ""
}

func (turnstileSpec) verify(m *Manager, ctx context.Context, res *resolvedConfig, action, payload string) error {
	return m.verifyTurnstile(ctx, string(res.secret), payload, action)
}

// providerSpecs holds one spec per selectable provider. The set is closed
// and package-local, so unlike the agent registry (which accepts
// cross-package registrations through RegisterProvider) a map written
// once at package init is enough.
var providerSpecs = map[Provider]providerSpec{
	ProviderAltcha:      altchaSpec{},
	ProviderRecaptchaV3: recaptchaSpec{},
	ProviderTurnstile:   turnstileSpec{},
}

// specFor returns the provider's spec. ok is false only for an enum value
// outside the known set (possible via direct SQL on an open proto3 enum);
// every caller fails closed on that path.
func specFor(p Provider) (providerSpec, bool) {
	spec, ok := providerSpecs[p]
	return spec, ok
}

// ProviderAlias returns the provider's human-facing alias. An enum value
// outside the known set degrades to its number, which decrypts against no
// real secret and never reaches the metric labels.
func ProviderAlias(p Provider) string {
	if spec, ok := specFor(p); ok {
		return spec.alias()
	}
	return fmt.Sprintf("captcha_provider(%d)", int32(p))
}

// SupportedProviders lists every selectable provider's alias, sorted. The
// admin CLI's --provider help and validation errors derive from here so
// they cannot drift from what the registry dispatches on.
func SupportedProviders() []string {
	out := make([]string, 0, len(providerSpecs))
	for _, spec := range providerSpecs {
		out = append(out, spec.alias())
	}
	sort.Strings(out)
	return out
}

// ParseProvider validates a provider alias from user input (the admin
// CLI's --provider flag) and returns its enum value. Accepts the alias
// and, for reCAPTCHA, the kebab-case spelling; never the CAPS proto name.
func ParseProvider(name string) (Provider, error) {
	for p, spec := range providerSpecs {
		if name == spec.alias() {
			return p, nil
		}
	}
	if name == "recaptcha-v3" {
		return ProviderRecaptchaV3, nil
	}
	return leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_UNSPECIFIED,
		fmt.Errorf("unsupported captcha provider %q (supported: %s)", name, strings.Join(SupportedProviders(), ", "))
}
