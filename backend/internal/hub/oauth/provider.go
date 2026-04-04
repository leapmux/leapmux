package oauth

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/oauth2"
)

// TokenSet holds the tokens returned by an OAuth provider after exchange or refresh.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresAt    time.Time // absolute expiry time for the access token
}

// String returns a redacted representation to prevent token leakage in logs.
func (t TokenSet) String() string { return "[REDACTED TokenSet]" }

// GoString returns a redacted representation for %#v formatting.
func (t TokenSet) GoString() string { return "[REDACTED TokenSet]" }

// UserClaims holds the user identity claims from an OAuth provider.
type UserClaims struct {
	Subject     string // unique identifier (sub claim for OIDC, user ID for GitHub)
	Email       string
	Name        string
	DisplayName string
}

// Provider defines the interface for OAuth/OIDC identity providers.
type Provider interface {
	// AuthURL returns the authorization URL to redirect the user to.
	// codeVerifier is the PKCE verifier; providers that support PKCE
	// derive the S256 challenge from it automatically.
	AuthURL(state, codeVerifier string) string

	// Exchange trades an authorization code for tokens and user claims.
	Exchange(ctx context.Context, code, codeVerifier string) (*TokenSet, *UserClaims, error)

	// Refresh exchanges a refresh token for new tokens.
	Refresh(ctx context.Context, refreshToken string) (*TokenSet, error)
}

// Provider type constants.
const (
	ProviderTypeGitHub = "github"
	ProviderTypeOIDC   = "oidc"
)

// refreshWithConfig is a shared implementation of Refresh for providers backed
// by an oauth2.Config.
func refreshWithConfig(ctx context.Context, cfg *oauth2.Config, refreshToken, label string) (*TokenSet, error) {
	tokenSource := cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("%s refresh: %w", label, err)
	}

	return TokenSetFromOAuth2Token(token), nil
}

// TokenSetFromOAuth2Token converts an oauth2.Token to a TokenSet.
func TokenSetFromOAuth2Token(token *oauth2.Token) *TokenSet {
	ts := &TokenSet{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresAt:    token.Expiry.UTC(),
	}
	return ts
}
