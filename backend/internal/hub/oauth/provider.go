package oauth

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// UserClaims holds the user identity claims from an OAuth provider.
// Email carries a value only when the provider verified it.
type UserClaims struct {
	Subject     string // unique identifier (sub claim for OIDC, user ID for GitHub)
	Email       string // set only when the provider confirms it is verified
	Name        string
	DisplayName string
}

// AuthURLOptions modifies one authorization request.
type AuthURLOptions struct {
	// ForceReauthentication asks the identity provider to make the user
	// interact again even when it holds its own live session.
	//
	// It exists for the step-up leg: an OAuth-only account has no password
	// and no passkey, so "prove who you are again" can only be asked of the
	// provider. Without it the provider silently reuses its session, returns
	// the user in a fraction of a second, and the click proves nothing at
	// all.
	//
	// It ASKS; it does not prove. ReauthMaxAge states three things once:
	// what the request can oblige a provider to do, what no provider reports
	// back reliably enough to check, and why the hub therefore reads none of
	// it.
	ForceReauthentication bool
}

// ExchangeOptions carries optional callback transport data to Provider.Exchange.
type ExchangeOptions struct {
	// FormValues holds the parsed POST form body for response_mode=form_post
	// callbacks (nil for GET query callbacks).
	FormValues url.Values
}

// Provider defines the interface for OAuth/OIDC identity providers.
type Provider interface {
	// AuthURL returns the authorization URL to redirect the user to.
	// codeVerifier is the PKCE verifier; providers that support PKCE
	// derive the S256 challenge from it automatically.
	AuthURL(state, codeVerifier string, opts AuthURLOptions) string

	// Exchange trades an authorization code for user claims.
	Exchange(ctx context.Context, code, codeVerifier string, opts ExchangeOptions) (*UserClaims, error)

	// UsesFormPost reports whether the authorization callback arrives as a
	// cross-site HTTP POST (response_mode=form_post), such as Sign in with
	// Apple.
	UsesFormPost() bool
}

// Provider type and response mode constants stored in oauth_providers.
const (
	ProviderTypeGitHub   = leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB
	ProviderTypeOIDC     = leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC
	ResponseModeQuery    = leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_QUERY
	ResponseModeFormPost = leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_FORM_POST
)

// ResponseModeWire spells an OAuthResponseMode for the admin RPC surface.
func ResponseModeWire(m leapmuxv1.OAuthResponseMode) string {
	switch m {
	case ResponseModeQuery:
		return "query"
	case ResponseModeFormPost:
		return "form_post"
	default:
		return ""
	}
}

// ParseResponseMode parses the admin RPC wire spelling of an OAuthResponseMode.
func ParseResponseMode(raw string) (leapmuxv1.OAuthResponseMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "query":
		return ResponseModeQuery, nil
	case "form_post":
		return ResponseModeFormPost, nil
	default:
		return leapmuxv1.OAuthResponseMode_O_AUTH_RESPONSE_MODE_UNSPECIFIED,
			fmt.Errorf("unknown response_mode %q (supported: query, form_post)", raw)
	}
}

// ProviderTypeWire spells a provider type for the ADMIN RPC surface, which
// carries it as a string because an operator types it and reads it back.
//
// The storage numbering and this vocabulary are separate on purpose: the column
// holds the ordinal, and only the two functions here know the words. An
// unrecognized type spells "", which the admin surface renders as no type
// rather than as a type it invented.
func ProviderTypeWire(t leapmuxv1.IdentityProviderType) string {
	switch t {
	case ProviderTypeGitHub:
		return "github"
	case ProviderTypeOIDC:
		return "oidc"
	default:
		return ""
	}
}
