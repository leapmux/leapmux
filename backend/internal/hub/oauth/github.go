package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

// GitHubProvider implements the Provider interface for GitHub OAuth.
type GitHubProvider struct {
	oauth2Config *oauth2.Config
}

// NewGitHubProvider creates a GitHub OAuth provider.
func NewGitHubProvider(clientID, clientSecret, redirectURL string, scopes []string) *GitHubProvider {
	if len(scopes) == 0 {
		scopes = []string{"read:user", "user:email"}
	}

	return &GitHubProvider{
		oauth2Config: &oauth2.Config{
			ClientID:     clientID,
			ClientSecret: clientSecret,
			RedirectURL:  redirectURL,
			Endpoint:     github.Endpoint,
			Scopes:       scopes,
		},
	}
}

func (p *GitHubProvider) AuthURL(state, codeChallenge string) string {
	// GitHub does not support PKCE, but we include the state for CSRF protection.
	return p.oauth2Config.AuthCodeURL(state)
}

func (p *GitHubProvider) Exchange(ctx context.Context, code, _ string) (*TokenSet, *UserClaims, error) {
	token, err := p.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("github exchange: %w", err)
	}

	// Fetch user info from GitHub API.
	claims, err := fetchGitHubUser(ctx, token.AccessToken)
	if err != nil {
		return nil, nil, err
	}

	tokenSet := &TokenSet{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
	}

	return tokenSet, claims, nil
}

func (p *GitHubProvider) Refresh(ctx context.Context, refreshToken string) (*TokenSet, error) {
	tokenSource := p.oauth2Config.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
	token, err := tokenSource.Token()
	if err != nil {
		return nil, fmt.Errorf("github refresh: %w", err)
	}

	return &TokenSet{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
	}, nil
}

// fetchGitHubUser retrieves user info from the GitHub API.
func fetchGitHubUser(ctx context.Context, accessToken string) (*UserClaims, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return nil, fmt.Errorf("github user request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github user fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("github user API returned %d: %s", resp.StatusCode, string(body))
	}

	var ghUser struct {
		ID    int    `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&ghUser); err != nil {
		return nil, fmt.Errorf("github user decode: %w", err)
	}

	return &UserClaims{
		Subject:     strconv.Itoa(ghUser.ID),
		Email:       ghUser.Email,
		Name:        ghUser.Login,
		DisplayName: ghUser.Name,
	}, nil
}
