package oauth

import (
	"context"
	"fmt"
	"strconv"
	"time"

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

// ReauthMaxAge is the max_age this provider sends on a re-authentication,
// in seconds on the wire.
//
// It covers the whole round trip a person makes at the provider -- reading
// the prompt, typing a password, answering a second factor -- and nothing
// more.
//
// This number obliges the PROVIDER, not the hub: a conforming OP must
// re-authenticate a session older than it, and that server-side obligation is
// the whole value.
//
// The hub does NOT read the auth_time that comes back, and that is a decision
// rather than an omission. This file is where the decision lives, because
// max_age is the request half of the same question.
//
// The one account shape the re-authentication leg serves holds no password
// and no passkey, so the provider IS its sign-in credential: a completed
// round trip already proves as much as signing in does, and asking for more
// proves nothing extra. A check would also be unenforceable. OpenID Connect
// makes prompt=login a SHOULD, its own RP-side rule for auth_time is a SHOULD
// whose stated remedy is to ask again rather than to refuse, and several
// widely deployed providers never fill the claim at all -- Google documents
// that it does not re-authenticate on request, and Microsoft Entra carries
// auth_time as an optional claim the app registration must ask for. Refusing
// them answered the only path such an account has with a 403 every time.
//
// So the request carries prompt=login and max_age, and the hub reads neither
// the claim nor anything derived from it. UserClaims carries no auth_time
// field for the same reason: a value nothing may act on is one a later reader
// builds a rule on.
const ReauthMaxAge = 5 * time.Minute

// AuthURL builds the OIDC authorization URL.
//
// ForceReauthentication emits BOTH prompt=login and max_age, and the second
// is the one a provider must obey. prompt=login alone is a SHOULD -- OpenID Connect
// Core 3.1.2.1 says the provider "SHOULD prompt the user for
// reauthentication", so one that ignores it answers exactly like one that
// honoured it. max_age states a number the same section obliges a conforming
// provider to enforce at its own end.
//
// A POSITIVE value, not zero. The spec calls max_age=0 "equivalent to
// prompt=login", so the zero adds nothing to the prompt beside it, while any
// positive value carries the same obligation -- and two deployed providers
// mishandle the zero: Microsoft Entra answers it with a token already outside
// its own window, and Okta Classic refuses it outright.
func (p *OIDCProvider) AuthURL(state, codeVerifier string, opts AuthURLOptions) string {
	codeOpts := []oauth2.AuthCodeOption{
		oauth2.S256ChallengeOption(codeVerifier),
	}
	if opts.ForceReauthentication {
		codeOpts = append(codeOpts,
			oauth2.SetAuthURLParam("prompt", "login"),
			oauth2.SetAuthURLParam("max_age", strconv.Itoa(int(ReauthMaxAge.Seconds()))),
		)
	}
	return p.oauth2Config.AuthCodeURL(state, codeOpts...)
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
		Email         string `json:"email"`
		EmailVerified *bool  `json:"email_verified,omitempty"`
		Name          string `json:"name"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, nil, fmt.Errorf("oidc parse claims: %w", err)
	}

	// INVARIANT: UserClaims.Email carries only an email the provider confirms
	// as verified. This check ensures email_verified is explicitly true;
	// missing or false values result in an empty email. The per-provider
	// trust_email setting controls whether the hub treats such verified emails
	// as pre-verified for account linking and signup — it does NOT bypass this
	// provider-level check. See also fetchGitHubVerifiedEmail in github.go.
	email := ""
	if claims.EmailVerified != nil && *claims.EmailVerified {
		email = claims.Email
	}

	userClaims := &UserClaims{
		Subject: idToken.Subject,
		Email:   email,
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
