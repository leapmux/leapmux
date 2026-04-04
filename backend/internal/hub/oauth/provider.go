package oauth

import "context"

// TokenSet holds the tokens returned by an OAuth provider after exchange or refresh.
type TokenSet struct {
	AccessToken  string
	RefreshToken string
	TokenType    string
	ExpiresIn    int // seconds until access token expires
}

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
	AuthURL(state, codeChallenge string) string

	// Exchange trades an authorization code for tokens and user claims.
	Exchange(ctx context.Context, code, codeVerifier string) (*TokenSet, *UserClaims, error)

	// Refresh exchanges a refresh token for new tokens.
	Refresh(ctx context.Context, refreshToken string) (*TokenSet, error)
}
