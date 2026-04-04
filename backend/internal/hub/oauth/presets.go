package oauth

// Preset holds default values for a known OAuth provider type.
type Preset struct {
	Name         string
	ProviderType string // "oidc" or "github"
	IssuerURL    string // empty for GitHub
	Scopes       string
}

// Presets maps provider type aliases to their default configurations.
var Presets = map[string]Preset{
	"github": {
		Name:         "GitHub",
		ProviderType: ProviderTypeGitHub,
		IssuerURL:    "",
		Scopes:       "read:user user:email",
	},
	"google": {
		Name:         "Google",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "https://accounts.google.com",
		Scopes:       "openid profile email",
	},
	"apple": {
		Name:         "Apple",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "https://appleid.apple.com",
		Scopes:       "openid name email",
	},
	"oidc": {
		Name:         "",
		ProviderType: ProviderTypeOIDC,
		IssuerURL:    "",
		Scopes:       "openid profile email",
	},
}
