package captcha

import (
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/settings"
)

// The captcha domain's hub_settings keys. The old captcha_config table's
// per-provider rows (selected + enabled flags, settings JSON, encrypted
// secret) map onto five keys: the selection and the verification switch
// become scalars — so "exactly one selected row" is a type property, not
// an invariant three statements maintained — and each provider keeps one
// document holding its public settings and, in the encrypted half, its
// secret (the ALTCHA HMAC signing key or an external provider's siteverify
// API secret).
//
// Key-level validation accepts the unconfigured state (an external
// provider's row before the operator supplies its keys): the strict
// "selected provider must be complete" rule lives in SelectedConfigured
// (the write-path cross rule) with Effective's read-time fallback behind
// it.
var (
	CaptchaEnabledKey = settings.NewKey[bool]("captcha.enabled").
				WithDefault(true).
				WithDoc("whether captcha verification runs (the honeypot stays active either way)", "boolean")

	CaptchaSelectedKey = settings.NewKey[string]("captcha.selected").
				WithDefault(altchaSpec{}.alias()).
				WithValidate(validateSelectedProvider).
				WithDoc("the active captcha provider alias", "string")

	AltchaKey = settings.NewKey[AltchaRow]("captcha.altcha").
			WithDefault(defaultAltchaRow()).
			WithValidate(func(r AltchaRow) error { return r.AltchaSettings.Validate() }).
			SecretFields("hmac_key").
			WithDoc("built-in ALTCHA proof-of-work parameters; the signing key lives in the secret half",
			`{"algorithm", "cost", "memory_cost", "parallelism", "challenge_expiry_seconds"} + secret {"hmac_key"}`)

	RecaptchaV3Key = settings.NewKey[RecaptchaV3Row]("captcha.recaptcha_v3").
			WithDefault(defaultRecaptchaV3Row()).
			WithValidate(validateRecaptchaRow).
			SecretFields("secret_key").
			WithDoc("Google reCAPTCHA v3 site key and score threshold; the API secret lives in the secret half",
			`{"site_key", "min_score"} + secret {"secret_key"}`)

	TurnstileKey = settings.NewKey[TurnstileRow]("captcha.turnstile").
			SecretFields("secret_key").
			WithDoc("Cloudflare Turnstile site key; the API secret lives in the secret half",
			`{"site_key"} + secret {"secret_key"}`)
)

// AltchaRow is the captcha.altcha document: the proof-of-work parameters
// plus the HMAC signing key (encrypted half, base64 in JSON).
type AltchaRow struct {
	AltchaSettings
	HMACKey []byte `json:"hmac_key,omitempty"`
}

func defaultAltchaRow() AltchaRow {
	return AltchaRow{AltchaSettings: DefaultAltchaSettings()}
}

// RecaptchaV3Row is the captcha.recaptcha_v3 document.
type RecaptchaV3Row struct {
	SiteKey   string  `json:"site_key,omitempty"`
	MinScore  float64 `json:"min_score,omitempty"`
	SecretKey string  `json:"secret_key,omitempty"` // encrypted half
}

func defaultRecaptchaV3Row() RecaptchaV3Row {
	return RecaptchaV3Row{MinScore: defaultRecaptchaMinScore}
}

// validateRecaptchaRow checks only what must hold even unconfigured. The
// score threshold must sit in (0, 1]: zero is excluded because the
// provider's own validation rejects it (a threshold that accepts every
// score disables the check), and a value the consumer refuses must fail
// at write time, not silently round-trip to the 0.5 default.
func validateRecaptchaRow(r RecaptchaV3Row) error {
	if r.MinScore <= 0 || r.MinScore > 1 {
		return fmt.Errorf("recaptcha_v3 min_score must be greater than 0 and at most 1 (got %g)", r.MinScore)
	}
	return nil
}

// TurnstileRow is the captcha.turnstile document; Turnstile has no
// tunables beyond the key pair.
type TurnstileRow struct {
	SiteKey   string `json:"site_key,omitempty"`
	SecretKey string `json:"secret_key,omitempty"` // encrypted half
}

// validateSelectedProvider keeps the selection inside the registry the
// manager can dispatch on; a hand-edited alias degrades to the altcha
// default at read time rather than failing open on an unknown provider.
func validateSelectedProvider(alias string) error {
	if _, err := ParseProvider(alias); err != nil {
		return err
	}
	return nil
}

// SettingsDescriptors lists the captcha keys for settings-manager
// registration.
func SettingsDescriptors() []settings.Descriptor {
	return []settings.Descriptor{
		CaptchaEnabledKey,
		CaptchaSelectedKey,
		AltchaKey,
		RecaptchaV3Key,
		TurnstileKey,
	}
}

// DescriptorFor returns the settings key holding one provider's row. It
// is the one provider-to-key mapping: the admin CLI's captcha verbs, the
// cross-key rule, and the manager all ask here, so a provider added to
// the registry cannot end up addressed through a stale or divergent key.
func DescriptorFor(p Provider) settings.Descriptor {
	switch p {
	case ProviderRecaptchaV3:
		return RecaptchaV3Key
	case ProviderTurnstile:
		return TurnstileKey
	default:
		return AltchaKey
	}
}

// SelectedConfigured is the captcha domain's cross-key rule: selecting an
// external provider requires its row to be complete in the same state —
// site key and secret both present. The write path rejects the impossible
// combination wherever it is introduced (selecting an unconfigured
// provider, or clearing the keys of the selected one), instead of storing
// a selection that Effective would silently fall back from. ALTCHA needs
// nothing: its row self-provisions on first use.
func SelectedConfigured(s *settings.Snapshot) error {
	provider, err := ParseProvider(CaptchaSelectedKey.Of(s))
	if err != nil || provider == ProviderAltcha {
		// An unparseable selection degrades at read time (validateSelected
		// is the write-path guard for the alias itself); altcha has no
		// completeness requirement.
		return nil
	}
	switch provider {
	case ProviderRecaptchaV3:
		if row := RecaptchaV3Key.Of(s); row.SiteKey == "" || row.SecretKey == "" {
			return fmt.Errorf("captcha.selected=%s requires its site key and secret to be configured first", ProviderAlias(provider))
		}
	case ProviderTurnstile:
		if row := TurnstileKey.Of(s); row.SiteKey == "" || row.SecretKey == "" {
			return fmt.Errorf("captcha.selected=%s requires its site key and secret to be configured first", ProviderAlias(provider))
		}
	}
	return nil
}
