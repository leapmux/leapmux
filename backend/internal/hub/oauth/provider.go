package oauth

import (
	"context"

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

// Provider defines the interface for OAuth/OIDC identity providers.
type Provider interface {
	// AuthURL returns the authorization URL to redirect the user to.
	// codeVerifier is the PKCE verifier; providers that support PKCE
	// derive the S256 challenge from it automatically.
	AuthURL(state, codeVerifier string, opts AuthURLOptions) string

	// Exchange trades an authorization code for user claims.
	Exchange(ctx context.Context, code, codeVerifier string) (*UserClaims, error)
}

// Provider type constants. oauth_providers.provider_type stores these ordinals.
const (
	ProviderTypeGitHub = leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_GITHUB
	ProviderTypeOIDC   = leapmuxv1.IdentityProviderType_IDENTITY_PROVIDER_TYPE_OIDC
)

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
