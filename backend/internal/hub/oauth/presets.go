package oauth

import "github.com/leapmux/leapmux/internal/util/ptrconv"

// Preset holds default values for a known OAuth provider type.
type Preset struct {
	Name         string
	ProviderType string // "oidc" or "github"
	IssuerURL    string // empty for GitHub
	Scopes       string
	TrustEmail   *bool // nil means the user must specify --trust-email explicitly
}

// Presets maps provider type aliases to their default configurations.
var Presets = map[string]Preset{
	"github": {
		Name:         "GitHub",
		ProviderType: ProviderTypeGitHub,
		IssuerURL:    "",
		Scopes:       "read:user user:email",
		TrustEmail:   ptrconv.Ptr(true),
	},
	"google": {
		Name:         "Google",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "https://accounts.google.com",
		Scopes:       "openid profile email",
		TrustEmail:   ptrconv.Ptr(true),
	},
	"apple": {
		Name:         "Apple",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "https://appleid.apple.com",
		Scopes:       "openid name email",
		TrustEmail:   ptrconv.Ptr(true),
	},
	"oidc": {
		Name:         "",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "",
		Scopes:       "openid profile email",
		TrustEmail:   nil, // must be specified explicitly
	},
}
