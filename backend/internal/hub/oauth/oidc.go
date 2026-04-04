package oauth

import (
	"context"
	"fmt"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCProvider implements the Provider interface for generic OpenID Connect providers.
type OIDCProvider struct {
	oauth2Config *oauth2.Config
	verifier     *gooidc.IDTokenVerifier
	provider     *gooidc.Provider
}

// NewOIDCProvider creates an OIDC provider by discovering the issuer's configuration.
func NewOIDCProvider(ctx context.Context, issuerURL, clientID, clientSecret, redirectURL string, scopes []string) (*OIDCProvider, error) {
	provider, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", issuerURL, err)
	}

	if len(scopes) == 0 {
		scopes = []string{gooidc.ScopeOpenID, "profile", "email"}
	}

	oauth2Config := &oauth2.Config{
		ClientID:     clientID,
		ClientSecret: clientSecret,
		RedirectURL:  redirectURL,
		Endpoint:     provider.Endpoint(),
		Scopes:       scopes,
	}

	verifier := provider.Verifier(&gooidc.Config{ClientID: clientID})

	return &OIDCProvider{
		oauth2Config: oauth2Config,
		verifier:     verifier,
		provider:     provider,
	}, nil
}

func (p *OIDCProvider) AuthURL(state, codeVerifier string) string {
	opts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(codeVerifier),
	}
	return p.oauth2Config.AuthCodeURL(state, opts...)
}

func (p *OIDCProvider) Exchange(ctx context.Context, code, codeVerifier string) (*TokenSet, *UserClaims, error) {
	opts := []oauth2.AuthCodeOption{
		oauth2.VerifierOption(codeVerifier),
	}
	token, err := p.oauth2Config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, nil, fmt.Errorf("oidc exchange: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, nil, fmt.Errorf("oidc exchange: no id_token in response")
	}

	idToken, err := p.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, nil, fmt.Errorf("oidc verify id_token: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("oidc parse claims: %w", err)
	}

	userClaims := &UserClaims{
		Subject: idToken.Subject,
		Email:   claims.Email,
		Name:    claims.Name,
	}

	return TokenSetFromOAuth2Token(token), userClaims, nil
}

func (p *OIDCProvider) Refresh(ctx context.Context, refreshToken string) (*TokenSet, error) {
	return refreshWithConfig(ctx, p.oauth2Config, refreshToken, "oidc")
}

// ValidateIssuer checks that the OIDC issuer URL is reachable and returns a valid discovery document.
func ValidateIssuer(ctx context.Context, issuerURL string) error {
	_, err := gooidc.NewProvider(ctx, issuerURL)
	if err != nil {
		return fmt.Errorf("oidc discovery failed for %s: %w", issuerURL, err)
	}
	return nil
}
