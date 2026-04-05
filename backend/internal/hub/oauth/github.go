package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/github"
)

var githubHTTPClient = &http.Client{Timeout: 10 * time.Second}

const defaultGitHubBaseURL = "https://api.github.com"

// GitHubProvider implements the Provider interface for GitHub OAuth.
type GitHubProvider struct {
	oauth2Config *oauth2.Config
	baseURL      string // overridable for testing; defaults to GitHub API
}

// NewGitHubProvider creates a GitHub OAuth provider.
func NewGitHubProvider(clientID, clientSecret, redirectURL string, scopes []string) *GitHubProvider {
	if len(scopes) == 0 {
		scopes = []string{"read:user", "user:email"}
	}

	return &GitHubProvider{
		baseURL: defaultGitHubBaseURL,
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
	return p.exchangeWithBaseURL(ctx, code, p.baseURL)
}

func (p *GitHubProvider) exchangeWithBaseURL(ctx context.Context, code string, baseURL string) (*TokenSet, *UserClaims, error) {
	token, err := p.oauth2Config.Exchange(ctx, code)
	if err != nil {
		return nil, nil, fmt.Errorf("github exchange: %w", err)
	}

	claims, err := fetchGitHubUser(ctx, token.AccessToken, baseURL)
	if err != nil {
		return nil, nil, err
	}

	return TokenSetFromOAuth2Token(token), claims, nil
}

func (p *GitHubProvider) Refresh(ctx context.Context, refreshToken string) (*TokenSet, error) {
	return refreshWithConfig(ctx, p.oauth2Config, refreshToken, "github")
}

// fetchGitHubUser retrieves user info from the GitHub API (or a custom base URL for testing).
// It fetches /user for identity and /user/emails for the verified primary email.
func fetchGitHubUser(ctx context.Context, accessToken string, baseURL string) (*UserClaims, error) {
	ghUser, err := fetchGitHubJSON[struct {
		ID    int    `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}](ctx, accessToken, baseURL+"/user")
	if err != nil {
		return nil, err
	}

	// Fetch the verified primary email from /user/emails.
	email := fetchGitHubVerifiedEmail(ctx, accessToken, baseURL)

	return &UserClaims{
		Subject:     strconv.Itoa(ghUser.ID),
		Email:       email,
		Name:        ghUser.Login,
		DisplayName: ghUser.Name,
	}, nil
}

// fetchGitHubVerifiedEmail returns the primary verified email from /user/emails,
// or empty string if none is found.
func fetchGitHubVerifiedEmail(ctx context.Context, accessToken, baseURL string) string {
	type ghEmail struct {
		Email    string `json:"email"`
		Primary  bool   `json:"primary"`
		Verified bool   `json:"verified"`
	}

	emails, err := fetchGitHubJSON[[]ghEmail](ctx, accessToken, baseURL+"/user/emails")
	if err != nil {
		return ""
	}

	for _, e := range *emails {
		if e.Primary && e.Verified {
			return e.Email
		}
	}
	return ""
}

// fetchGitHubJSON performs an authenticated GET request to the GitHub API and
// decodes the JSON response into T.
func fetchGitHubJSON[T any](ctx context.Context, accessToken, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("github request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := githubHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("github API %s returned %d: %s", url, resp.StatusCode, string(body))
	}

	var result T
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("github decode %s: %w", url, err)
	}
	return &result, nil
}
