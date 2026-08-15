package captcha

import (
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

// providerAliases maps every selectable provider to its human-facing
// name — the admin CLI's --provider flag, the `captcha show` output, the
// metrics label, and the keystore AAD all use these, never the CAPS
// proto names (the AgentProvider/agentlabels pattern).
var providerAliases = map[Provider]string{
	ProviderAltcha:      "altcha",
	ProviderRecaptchaV3: "recaptcha_v3",
	ProviderTurnstile:   "turnstile",
}

// ProviderAlias returns the provider's human-facing alias. An enum value
// outside the known set (possible only via direct SQL on an open proto3
// enum) degrades to its number, which decrypts against no real secret and
// never reaches the metric labels.
func ProviderAlias(p Provider) string {
	if alias, ok := providerAliases[p]; ok {
		return alias
	}
	return fmt.Sprintf("captcha_provider(%d)", int32(p))
}

// SupportedProviders lists every selectable provider's alias, sorted. The
// admin CLI's --provider help and validation errors derive from here so
// they cannot drift from what the manager dispatches on.
func SupportedProviders() []string {
	out := make([]string, 0, len(providerAliases))
	for _, alias := range providerAliases {
		out = append(out, alias)
	}
	sort.Strings(out)
	return out
}

// ParseProvider validates a provider alias from user input (the admin
// CLI's --provider flag) and returns its enum value. Accepts the alias
// and, for reCAPTCHA, the kebab-case spelling; never the CAPS proto name.
func ParseProvider(name string) (Provider, error) {
	switch name {
	case "altcha":
		return ProviderAltcha, nil
	case "recaptcha_v3", "recaptcha-v3":
		return ProviderRecaptchaV3, nil
	case "turnstile":
		return ProviderTurnstile, nil
	}
	return leapmuxv1.CaptchaProvider_CAPTCHA_PROVIDER_UNSPECIFIED,
		fmt.Errorf("unsupported captcha provider %q (supported: %s)", name, strings.Join(SupportedProviders(), ", "))
}
