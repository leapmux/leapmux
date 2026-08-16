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
// "selected provider must be complete" rule lives in Effective, which is
// where the old row validation lived too.
var (
	CaptchaEnabledKey = settings.NewKey[bool]("captcha.enabled").
				WithDefault(true)

	CaptchaSelectedKey = settings.NewKey[string]("captcha.selected").
				WithDefault(altchaSpec{}.alias()).
				WithValidate(validateSelectedProvider)

	AltchaKey = settings.NewKey[AltchaRow]("captcha.altcha").
			WithDefault(defaultAltchaRow()).
			WithValidate(func(r AltchaRow) error { return r.AltchaSettings.Validate() }).
			SecretFields("hmac_key")

	RecaptchaV3Key = settings.NewKey[RecaptchaV3Row]("captcha.recaptcha_v3").
			WithDefault(defaultRecaptchaV3Row()).
			WithValidate(validateRecaptchaRow).
			SecretFields("secret_key")

	TurnstileKey = settings.NewKey[TurnstileRow]("captcha.turnstile").
			SecretFields("secret_key")
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

// validateRecaptchaRow checks only what must hold even unconfigured: the
// score threshold, when set, must accept something.
func validateRecaptchaRow(r RecaptchaV3Row) error {
	if r.MinScore < 0 || r.MinScore > 1 {
		return fmt.Errorf("recaptcha_v3 min_score must be between 0 and 1 (got %g)", r.MinScore)
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

// providerDescriptor returns the settings key holding one provider's row.
func providerDescriptor(p Provider) settings.Descriptor {
	switch p {
	case ProviderRecaptchaV3:
		return RecaptchaV3Key
	case ProviderTurnstile:
		return TurnstileKey
	default:
		return AltchaKey
	}
}
