package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/util/id"
)

const (
	oauthStateExpiry         = 5 * time.Minute
	pendingOAuthSignupExpiry = 5 * time.Minute
	defaultTokenExpiry       = 1 * time.Hour
)

// OAuthHandler handles OAuth login/callback HTTP endpoints.
type OAuthHandler struct {
	sqlDB    *sql.DB
	queries  *gendb.Queries
	cfg      *config.Config
	keystore *keystore.Keystore

	// providers caches built Provider instances by provider ID.
	// Provider config (issuer, client_id, scopes) is immutable after creation,
	// so entries stay valid for the lifetime of the process.
	providers   map[string]huboauth.Provider
	providersMu sync.RWMutex
}

// NewOAuthHandler creates a new OAuth HTTP handler.
func NewOAuthHandler(sqlDB *sql.DB, q *gendb.Queries, cfg *config.Config, ks *keystore.Keystore) *OAuthHandler {
	return &OAuthHandler{sqlDB: sqlDB, queries: q, cfg: cfg, keystore: ks, providers: make(map[string]huboauth.Provider)}
}

// RegisterRoutes registers OAuth HTTP routes on the given mux.
func (h *OAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/oauth/", h.handleOAuth)
}

func (h *OAuthHandler) handleOAuth(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/auth/oauth/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 {
		http.Error(w, "invalid OAuth path", http.StatusBadRequest)
		return
	}

	providerID := parts[0]
	action := parts[1]

	switch action {
	case "login":
		h.handleLogin(w, r, providerID)
	case "callback":
		h.handleCallback(w, r, providerID)
	default:
		http.Error(w, "unknown OAuth action", http.StatusBadRequest)
	}
}

// loadEnabledProvider fetches the provider from DB, checks it's enabled, and
// builds the cached Provider instance. Returns an HTTP error on the
// ResponseWriter and false if the provider cannot be loaded.
func (h *OAuthHandler) loadEnabledProvider(w http.ResponseWriter, ctx context.Context, providerID string) (huboauth.Provider, bool) {
	dbProvider, err := h.queries.GetOAuthProviderByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return nil, false
	}
	if dbProvider.Enabled != 1 {
		http.Error(w, "provider disabled", http.StatusForbidden)
		return nil, false
	}
	provider, err := h.buildProvider(ctx, &dbProvider)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false
	}
	return provider, true
}

func (h *OAuthHandler) handleLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	provider, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	verifier := oauth2.GenerateVerifier()
	state := id.Generate()

	redirectURI := r.URL.Query().Get("redirect")

	if err := h.queries.CreateOAuthState(ctx, gendb.CreateOAuthStateParams{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: verifier,
		RedirectUri:  redirectURI,
		ExpiresAt:    time.Now().Add(oauthStateExpiry).UTC(),
	}); err != nil {
		slog.Error("oauth: create state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	authURL := provider.AuthURL(state, verifier)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (h *OAuthHandler) handleCallback(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		errMsg := r.URL.Query().Get("error_description")
		if errMsg == "" {
			errMsg = r.URL.Query().Get("error")
		}
		if errMsg == "" {
			errMsg = "missing code or state"
		}
		http.Error(w, "OAuth error: "+errMsg, http.StatusBadRequest)
		return
	}

	// Validate and consume state.
	oauthState, err := h.queries.GetOAuthState(ctx, state)
	if err != nil {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	_ = h.queries.DeleteOAuthState(ctx, state)

	if time.Now().UTC().After(oauthState.ExpiresAt) {
		http.Error(w, "state expired", http.StatusBadRequest)
		return
	}
	if oauthState.ProviderID != providerID {
		http.Error(w, "state/provider mismatch", http.StatusBadRequest)
		return
	}

	provider, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	// Exchange code for tokens.
	tokenSet, claims, err := provider.Exchange(ctx, code, oauthState.PkceVerifier)
	if err != nil {
		slog.Error("oauth: exchange", "error", err)
		http.Error(w, "OAuth exchange failed", http.StatusBadRequest)
		return
	}

	// Check if this OAuth identity is already linked to a user.
	link, err := h.queries.GetOAuthUserLink(ctx, gendb.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
	})
	if err == nil {
		// Existing link — direct login.
		user, userErr := h.queries.GetUserByID(ctx, link.UserID)
		if userErr != nil {
			http.Error(w, "linked user not found", http.StatusInternalServerError)
			return
		}

		h.loginOAuthUser(w, r, user.ID, providerID, tokenSet, oauthState.RedirectUri)
		return
	}
	if err != sql.ErrNoRows {
		slog.Error("oauth: query user link", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Auto-link by verified email: if the OAuth provider is trusted and returns
	// an email that matches an existing user with a verified email, link the
	// new provider identity to that account and log in directly.
	if h.cfg.OAuthTrustEmail && claims.Email != "" {
		existingUser, emailErr := h.queries.GetUserByEmail(ctx, claims.Email)
		if emailErr == nil && existingUser.EmailVerified == 1 {
			if err := h.queries.CreateOAuthUserLink(ctx, gendb.CreateOAuthUserLinkParams{
				UserID:          existingUser.ID,
				ProviderID:      providerID,
				ProviderSubject: claims.Subject,
			}); err != nil {
				slog.Error("oauth: create user link for auto-link", "error", err)
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}

			slog.Info("oauth: auto-linked provider to existing account by verified email",
				"user_id", existingUser.ID, "provider_id", providerID, "email", claims.Email)
			h.loginOAuthUser(w, r, existingUser.ID, providerID, tokenSet, oauthState.RedirectUri)
			return
		}
	}

	// New user — store pending signup for username selection.
	if !h.cfg.SignupEnabled {
		http.Error(w, "sign-up is disabled; no existing account linked to this identity", http.StatusForbidden)
		return
	}

	signupToken, err := h.storePendingSignup(ctx, providerID, claims, tokenSet, oauthState.RedirectUri)
	if err != nil {
		slog.Error("oauth: store pending signup", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, "/oauth/complete-signup?token="+signupToken, http.StatusFound)
}

// loginOAuthUser stores tokens, creates a session, and redirects the user.
// Used by both the existing-link and auto-link-by-email paths.
func (h *OAuthHandler) loginOAuthUser(w http.ResponseWriter, r *http.Request, userID, providerID string, tokenSet *huboauth.TokenSet, redirectURI string) {
	ctx := r.Context()

	if err := h.storeTokens(ctx, userID, providerID, tokenSet); err != nil {
		slog.Error("oauth: store tokens", "error", err)
	}

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, h.queries, userID, r.UserAgent(), r.RemoteAddr)
	if sessionErr != nil {
		slog.Error("oauth: create session", "error", sessionErr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, auth.BuildSessionCookie(sessionID, expiresAt, h.cfg.SecureCookies))

	redirectTo := "/"
	if redirectURI != "" {
		redirectTo = redirectURI
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func tokenExpiryTime(tokenSet *huboauth.TokenSet) time.Time {
	if tokenSet.ExpiresIn > 0 {
		return time.Now().Add(time.Duration(tokenSet.ExpiresIn) * time.Second).UTC()
	}
	return time.Now().Add(defaultTokenExpiry).UTC()
}

func (h *OAuthHandler) storePendingSignup(ctx context.Context, providerID string, claims *huboauth.UserClaims, tokenSet *huboauth.TokenSet, redirectURI string) (string, error) {
	token := id.Generate()

	// Use a temporary user/provider ID for AAD since the user doesn't exist yet.
	encAccessToken, err := h.keystore.Encrypt([]byte(tokenSet.AccessToken), keystore.AccessTokenAAD(token, providerID))
	if err != nil {
		return "", fmt.Errorf("encrypt access token: %w", err)
	}
	encRefreshToken, err := h.keystore.Encrypt([]byte(tokenSet.RefreshToken), keystore.RefreshTokenAAD(token, providerID))
	if err != nil {
		return "", fmt.Errorf("encrypt refresh token: %w", err)
	}

	tokenExpiresAt := tokenExpiryTime(tokenSet)

	displayName := claims.DisplayName
	if displayName == "" {
		displayName = claims.Name
	}

	if err := h.queries.CreatePendingOAuthSignup(ctx, gendb.CreatePendingOAuthSignupParams{
		Token:           token,
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
		Email:           claims.Email,
		DisplayName:     displayName,
		AccessToken:     encAccessToken,
		RefreshToken:    encRefreshToken,
		TokenType:       tokenSet.TokenType,
		TokenExpiresAt:  tokenExpiresAt,
		KeyVersion:      int64(h.keystore.ActiveVersion()),
		RedirectUri:     redirectURI,
		ExpiresAt:       time.Now().Add(pendingOAuthSignupExpiry).UTC(),
	}); err != nil {
		return "", fmt.Errorf("create pending signup: %w", err)
	}

	return token, nil
}

func (h *OAuthHandler) storeTokens(ctx context.Context, userID, providerID string, tokenSet *huboauth.TokenSet) error {
	encAccessToken, err := h.keystore.Encrypt([]byte(tokenSet.AccessToken), keystore.AccessTokenAAD(userID, providerID))
	if err != nil {
		return fmt.Errorf("encrypt access token: %w", err)
	}

	encRefreshToken, err := h.keystore.Encrypt([]byte(tokenSet.RefreshToken), keystore.RefreshTokenAAD(userID, providerID))
	if err != nil {
		return fmt.Errorf("encrypt refresh token: %w", err)
	}

	expiresAt := tokenExpiryTime(tokenSet)

	return h.queries.UpsertOAuthTokens(ctx, gendb.UpsertOAuthTokensParams{
		UserID:       userID,
		ProviderID:   providerID,
		AccessToken:  encAccessToken,
		RefreshToken: encRefreshToken,
		TokenType:    tokenSet.TokenType,
		ExpiresAt:    expiresAt,
		KeyVersion:   int64(h.keystore.ActiveVersion()),
	})
}

func (h *OAuthHandler) buildProvider(ctx context.Context, dbProvider *gendb.OauthProvider) (huboauth.Provider, error) {
	// Check cache first (provider config is immutable after creation).
	h.providersMu.RLock()
	cached, ok := h.providers[dbProvider.ID]
	h.providersMu.RUnlock()
	if ok {
		return cached, nil
	}

	clientSecret, err := h.keystore.Decrypt(dbProvider.ClientSecret, keystore.ProviderAAD(dbProvider.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt client secret: %w", err)
	}

	redirectURL := fmt.Sprintf("%s/auth/oauth/%s/callback", h.cfg.BaseURL(), dbProvider.ID)

	scopes := strings.Fields(dbProvider.Scopes)

	var provider huboauth.Provider
	switch dbProvider.ProviderType {
	case huboauth.ProviderTypeOIDC:
		provider, err = huboauth.NewOIDCProvider(ctx, dbProvider.IssuerUrl, dbProvider.ClientID, string(clientSecret), redirectURL, scopes)
		if err != nil {
			return nil, err
		}
	case huboauth.ProviderTypeGitHub:
		provider = huboauth.NewGitHubProvider(dbProvider.ClientID, string(clientSecret), redirectURL, scopes)
	default:
		return nil, fmt.Errorf("unknown provider type: %s", dbProvider.ProviderType)
	}

	h.providersMu.Lock()
	h.providers[dbProvider.ID] = provider
	h.providersMu.Unlock()

	return provider, nil
}
