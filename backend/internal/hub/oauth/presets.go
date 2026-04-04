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
		ProviderType: "github",
		IssuerURL:    "",
		Scopes:       "read:user user:email",
	},
	"google": {
		Name:         "Google",
		ProviderType: "oidc",
		IssuerURL:    "https://accounts.google.com",
		Scopes:       "openid profile email",
	},
	"apple": {
		Name:         "Apple",
		ProviderType: "oidc",
		IssuerURL:    "https://appleid.apple.com",
		Scopes:       "openid name email",
	},
	"oidc": {
		Name:         "",
		ProviderType: "oidc",
		IssuerURL:    "",
		Scopes:       "openid profile email",
	},
}
