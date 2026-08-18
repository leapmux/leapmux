package captcha

import (
	"fmt"

	"github.com/leapmux/leapmux/internal/hub/settings"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
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
// captchaProviderEnumValues derives the captcha.selected enum from the
// provider registry itself (providerSpecs via SupportedProviders), the
// same catalogue validateSelectedProvider dispatches on, so the UI and
// the validator cannot drift.
var captchaProviderEnumValues = func() []settings.EnumValue {
	aliases := SupportedProviders()
	out := make([]settings.EnumValue, 0, len(aliases))
	for _, alias := range aliases {
		p, err := ParseProvider(alias)
		if err != nil {
			panic("captcha: supported provider alias failed to parse: " + err.Error())
		}
		var ev settings.EnumValue
		ev.Value = alias
		switch p {
		case ProviderAltcha:
			ev.Label = "ALTCHA"
			ev.Help = "Built-in proof-of-work challenges; no third party, no egress."
		case ProviderRecaptchaV3:
			ev.Label = "Google reCAPTCHA v3"
			ev.Help = "Invisible score-based verification via Google's siteverify."
		case ProviderTurnstile:
			ev.Label = "Cloudflare Turnstile"
			ev.Help = "Checkbox challenge rendered from Cloudflare's script."
		}
		out = append(out, ev)
	}
	return out
}()

// altchaAlgorithmEnumValues derives the algorithm enum from
// SupportedAltchaAlgorithms — the same deriveKeyFuncs catalogue
// AltchaSettings.Validate dispatches on, so the UI and the validator
// cannot drift.
var altchaAlgorithmEnumValues = func() []settings.EnumValue {
	out := make([]settings.EnumValue, 0)
	for _, name := range SupportedAltchaAlgorithms() {
		out = append(out, settings.EnumValue{Value: name, Label: name})
	}
	return out
}()

var (
	CaptchaEnabledKey = settings.NewKey[bool]("captcha.enabled").
				WithDefault(true).
				WithUI(settings.UIMeta{
			Category:     "captcha",
			Title:        "Bot protection enabled",
			Summary:      "whether captcha verification runs (the honeypot stays active either way)",
			HiddenInSolo: true,
			Fields:       []settings.Field{{Name: "", Label: "Bot protection enabled", Kind: settings.FieldBool}},
		})

	CaptchaSelectedKey = settings.NewKey[string]("captcha.selected").
				WithDefault(altchaSpec{}.alias()).
				WithValidate(validateSelectedProvider).
				WithUI(settings.UIMeta{
			Category:     "captcha",
			Title:        "Provider",
			Summary:      "the active captcha provider alias",
			HiddenInSolo: true,
			Fields: []settings.Field{{
				Name: "", Label: "Provider", Kind: settings.FieldEnum,
				EnumValues: captchaProviderEnumValues,
			}},
		})

	AltchaKey = settings.NewKey[AltchaRow]("captcha.altcha").
			WithDefault(defaultAltchaRow()).
			WithValidate(func(r AltchaRow) error { return r.AltchaSettings.Validate() }).
			WithNormalize(normalizeAltchaFamily).
			SecretFields("hmac_key").
			WithUI(settings.UIMeta{
			Category:     "captcha",
			Title:        "ALTCHA parameters",
			Summary:      "built-in ALTCHA proof-of-work parameters; the signing key lives in the secret half",
			HiddenInSolo: true,
			Fields: []settings.Field{
				{Name: "algorithm", Label: "Algorithm", Kind: settings.FieldEnum, EnumValues: altchaAlgorithmEnumValues},
				{Name: "cost", Label: "Cost", Kind: settings.FieldInt, Unit: "count",
					Min: ptrconv.Ptr[int64](MinAltchaCost), Max: ptrconv.Ptr[int64](MaxAltchaCost)},
				{Name: "memory_cost", Label: "Memory cost", Kind: settings.FieldInt, Unit: "count",
					Min:       ptrconv.Ptr[int64](MinAltchaMemoryCost),
					Max:       ptrconv.Ptr[int64](MaxAltchaMemoryCost),
					DependsOn: &settings.FieldCondition{Field: "algorithm", In: []string{"SCRYPT", "ARGON2ID"}}},
				{Name: "parallelism", Label: "Parallelism", Kind: settings.FieldInt, Unit: "count",
					Min:       ptrconv.Ptr[int64](MinAltchaParallelism),
					Max:       ptrconv.Ptr[int64](MaxAltchaParallelism),
					DependsOn: &settings.FieldCondition{Field: "algorithm", In: []string{"SCRYPT", "ARGON2ID"}}},
				{Name: "challenge_expiry_seconds", Label: "Challenge expiry", Kind: settings.FieldInt,
					Min: ptrconv.Ptr[int64](60), Max: ptrconv.Ptr[int64](86400), Unit: "seconds"},
				{Name: "hmac_key", Label: "Signing key", Kind: settings.FieldBytes, Secret: true},
			},
		})

	RecaptchaV3Key = settings.NewKey[RecaptchaV3Row]("captcha.recaptcha_v3").
			WithDefault(defaultRecaptchaV3Row()).
			WithValidate(validateRecaptchaRow).
			SecretFields("secret_key").
			WithUI(settings.UIMeta{
			Category:     "captcha",
			Title:        "Google reCAPTCHA v3",
			Summary:      "Google reCAPTCHA v3 site key and score threshold; the API secret lives in the secret half",
			HiddenInSolo: true,
			// NO DependsOn on the credential fields. Hiding them until the
			// provider is selected made the provider unconfigurable: the
			// cross-key rule (SelectedConfigured) refuses the selection
			// until the keys are stored, and the keys had no field on
			// screen until the selection went through. An operator
			// configures a provider first and selects it after, which is
			// the same order the CLI writes in.
			Fields: []settings.Field{
				{Name: "site_key", Label: "Site key", Kind: settings.FieldString},
				{Name: "min_score", Label: "Minimum score", Kind: settings.FieldFloat,
					MinF: ptrconv.Ptr(minRecaptchaScore), MaxF: ptrconv.Ptr(1.0), Unit: "score",
					Help: "Tokens score below this are rejected; must be greater than 0 and at most 1."},
				{Name: "secret_key", Label: "API secret", Kind: settings.FieldString, Secret: true},
			},
		})

	TurnstileKey = settings.NewKey[TurnstileRow]("captcha.turnstile").
			SecretFields("secret_key").
			WithUI(settings.UIMeta{
			Category:     "captcha",
			Title:        "Cloudflare Turnstile",
			Summary:      "Cloudflare Turnstile site key; the API secret lives in the secret half",
			HiddenInSolo: true,
			// NO DependsOn on the credential fields; see RecaptchaV3Key.
			Fields: []settings.Field{
				{Name: "site_key", Label: "Site key", Kind: settings.FieldString},
				{Name: "secret_key", Label: "API secret", Kind: settings.FieldString, Secret: true},
			},
		})
)

// normalizeAltchaFamily resets the family-specific parameters when a
// write changes the algorithm.
//
// Cost, memory_cost and parallelism carry a DIFFERENT unit per family
// (SCRYPT's r is a block-count multiplier, ARGON2ID's m is KiB, and
// PBKDF2/SHA use neither), so carrying the old family's numbers into the
// new family reinterprets them — and Validate then refuses the result.
// Without this, a client that writes one field at a time could never
// switch to SCRYPT or ARGON2ID at all: the preferences dialog sends
// exactly {"algorithm":"SCRYPT"}, which merged onto the PBKDF2 default
// (cost 10000) is not a power of two.
//
// A parameter the document specifies always wins, so `--algorithm ARGON2ID
// --cost 3` means what it says. An unsupported algorithm passes through
// untouched for Validate to refuse with its own message.
func normalizeAltchaFamily(prev, next AltchaRow, specified map[string]bool) AltchaRow {
	if !specified["algorithm"] || next.Algorithm == prev.Algorithm {
		return next
	}
	defaults, err := DefaultAltchaSettingsFor(next.Algorithm)
	if err != nil {
		return next
	}
	if !specified["cost"] {
		next.Cost = defaults.Cost
	}
	if !specified["memory_cost"] {
		next.MemoryCost = defaults.MemoryCost
	}
	if !specified["parallelism"] {
		next.Parallelism = defaults.Parallelism
	}
	return next
}

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

// minRecaptchaScore is the LOWEST threshold the declared bound may
// advertise. The validator refuses 0 outright, so the interval it enforces
// is half-open -- (0, 1] -- and Field.MinF can only express a closed one.
// The declared floor is therefore the smallest score control step above
// zero, not zero: advertising 0.0 offered the operator a value every write
// refused.
const minRecaptchaScore = 0.05

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
