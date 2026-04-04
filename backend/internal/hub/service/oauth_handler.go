package service

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	gendb "github.com/leapmux/leapmux/internal/hub/generated/db"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/password"
	"github.com/leapmux/leapmux/internal/util/id"
)

// OAuthHandler handles OAuth login/callback HTTP endpoints.
type OAuthHandler struct {
	queries  *gendb.Queries
	cfg      *config.Config
	keystore *keystore.Keystore
}

// NewOAuthHandler creates a new OAuth HTTP handler.
func NewOAuthHandler(q *gendb.Queries, cfg *config.Config, ks *keystore.Keystore) *OAuthHandler {
	return &OAuthHandler{queries: q, cfg: cfg, keystore: ks}
}

// RegisterRoutes registers OAuth HTTP routes on the given mux.
func (h *OAuthHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/auth/oauth/", h.handleOAuth)
}

func (h *OAuthHandler) handleOAuth(w http.ResponseWriter, r *http.Request) {
	// Parse path: /auth/oauth/{provider_id}/{action}
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

func (h *OAuthHandler) handleLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	dbProvider, err := h.queries.GetOAuthProviderByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}
	if dbProvider.Enabled != 1 {
		http.Error(w, "provider disabled", http.StatusForbidden)
		return
	}

	provider, err := h.buildProvider(ctx, &dbProvider)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Generate PKCE and state.
	verifier, challenge, err := huboauth.GeneratePKCE()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	state := id.Generate()

	// Save redirect URI from query param.
	redirectURI := r.URL.Query().Get("redirect")

	// Store state in DB (5-minute expiry).
	if err := h.queries.CreateOAuthState(ctx, gendb.CreateOAuthStateParams{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: verifier,
		RedirectUri:  redirectURI,
		ExpiresAt:    time.Now().Add(5 * time.Minute).UTC(),
	}); err != nil {
		slog.Error("oauth: create state", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	authURL := provider.AuthURL(state, challenge)
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

	// Load provider.
	dbProvider, err := h.queries.GetOAuthProviderByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return
	}

	provider, err := h.buildProvider(ctx, &dbProvider)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Exchange code for tokens.
	tokenSet, claims, err := provider.Exchange(ctx, code, oauthState.PkceVerifier)
	if err != nil {
		slog.Error("oauth: exchange", "error", err)
		http.Error(w, "OAuth exchange failed", http.StatusBadRequest)
		return
	}

	// Find or create user.
	user, err := h.findOrCreateUser(ctx, providerID, claims)
	if err != nil {
		slog.Error("oauth: find/create user", "error", err)
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}

	// Store encrypted tokens.
	if err := h.storeTokens(ctx, user.ID, providerID, tokenSet); err != nil {
		slog.Error("oauth: store tokens", "error", err)
		// Continue — login still works, tokens just won't be persisted for refresh.
	}

	// Create session.
	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, h.queries, user.ID, r.UserAgent(), r.RemoteAddr)
	if sessionErr != nil {
		slog.Error("oauth: create session", "error", sessionErr)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Set session cookie.
	cookie := auth.BuildSessionCookie(sessionID, expiresAt, h.cfg.SecureCookies)
	http.SetCookie(w, cookie)

	// Redirect to frontend.
	redirectTo := "/"
	if oauthState.RedirectUri != "" {
		redirectTo = oauthState.RedirectUri
	}
	http.Redirect(w, r, redirectTo, http.StatusFound)
}

func (h *OAuthHandler) findOrCreateUser(ctx context.Context, providerID string, claims *huboauth.UserClaims) (*gendb.User, error) {
	// Check if there's an existing link.
	link, err := h.queries.GetOAuthUserLink(ctx, gendb.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
	})
	if err == nil {
		// Existing link — return the linked user.
		user, err := h.queries.GetUserByID(ctx, link.UserID)
		if err != nil {
			return nil, fmt.Errorf("linked user not found")
		}
		return &user, nil
	}
	if err != sql.ErrNoRows {
		return nil, fmt.Errorf("query user link: %w", err)
	}

	// No existing link — create new user if signup is allowed.
	if !h.cfg.SignupEnabled {
		return nil, fmt.Errorf("sign-up is disabled; no existing account linked to this identity")
	}

	// Derive username from claims.
	username := deriveUsername(claims)

	// Ensure uniqueness.
	username, err = ensureUniqueUsername(ctx, h.queries, username)
	if err != nil {
		return nil, err
	}

	// Create personal org.
	orgID := id.Generate()
	if err := h.queries.CreateOrg(ctx, gendb.CreateOrgParams{
		ID:         orgID,
		Name:       username,
		IsPersonal: 1,
	}); err != nil {
		return nil, fmt.Errorf("create org: %w", err)
	}

	// Create user (no password — OAuth-only account).
	userID := id.Generate()
	displayName := claims.DisplayName
	if displayName == "" {
		displayName = claims.Name
	}

	// Generate a random password hash for the password_hash NOT NULL constraint.
	// This password is unusable (random, never revealed).
	randomPwdHash, err := password.Hash(id.Generate())
	if err != nil {
		return nil, fmt.Errorf("generate random password: %w", err)
	}

	if err := h.queries.CreateUser(ctx, gendb.CreateUserParams{
		ID:           userID,
		OrgID:        orgID,
		Username:     username,
		PasswordHash: randomPwdHash,
		DisplayName:  displayName,
		Email:        claims.Email,
		IsAdmin:      0,
	}); err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}

	// Add to org_members.
	if err := h.queries.CreateOrgMember(ctx, gendb.CreateOrgMemberParams{
		OrgID:  orgID,
		UserID: userID,
		Role:   leapmuxv1.OrgMemberRole_ORG_MEMBER_ROLE_OWNER,
	}); err != nil {
		return nil, fmt.Errorf("create org member: %w", err)
	}

	// Create default preferences.
	if err := h.queries.UpsertUserPreferences(ctx, gendb.UpsertUserPreferencesParams{
		UserID: userID,
	}); err != nil {
		return nil, fmt.Errorf("create preferences: %w", err)
	}

	// Create OAuth user link.
	if err := h.queries.CreateOAuthUserLink(ctx, gendb.CreateOAuthUserLinkParams{
		UserID:          userID,
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
	}); err != nil {
		return nil, fmt.Errorf("create user link: %w", err)
	}

	user, err := h.queries.GetUserByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("get created user: %w", err)
	}
	return &user, nil
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

	expiresAt := time.Now().Add(time.Duration(tokenSet.ExpiresIn) * time.Second).UTC()
	if tokenSet.ExpiresIn <= 0 {
		expiresAt = time.Now().Add(1 * time.Hour).UTC() // default 1 hour
	}

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
	// Decrypt client secret.
	clientSecret, err := h.keystore.Decrypt(dbProvider.ClientSecret, keystore.ProviderAAD(dbProvider.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt client secret: %w", err)
	}

	// Build redirect URL.
	redirectURL := fmt.Sprintf("%s/auth/oauth/%s/callback", h.cfg.BaseURL(), dbProvider.ID)

	scopes := strings.Split(dbProvider.Scopes, " ")

	switch dbProvider.ProviderType {
	case huboauth.ProviderTypeOIDC:
		return huboauth.NewOIDCProvider(ctx, dbProvider.IssuerUrl, dbProvider.ClientID, string(clientSecret), redirectURL, scopes)
	case huboauth.ProviderTypeGitHub:
		return huboauth.NewGitHubProvider(dbProvider.ClientID, string(clientSecret), redirectURL, scopes), nil
	default:
		return nil, fmt.Errorf("unknown provider type: %s", dbProvider.ProviderType)
	}
}

func deriveUsername(claims *huboauth.UserClaims) string {
	if claims.Name != "" {
		return sanitizeUsername(claims.Name)
	}
	if claims.Email != "" {
		parts := strings.SplitN(claims.Email, "@", 2)
		return sanitizeUsername(parts[0])
	}
	return "user-" + id.Generate()[:8]
}

func sanitizeUsername(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
			b.WriteRune(c)
		}
	}
	result := b.String()
	if result == "" {
		return "user-" + id.Generate()[:8]
	}
	return result
}

func ensureUniqueUsername(ctx context.Context, q *gendb.Queries, username string) (string, error) {
	_, err := q.GetUserByUsername(ctx, username)
	if err == sql.ErrNoRows {
		return username, nil // available
	}
	if err != nil {
		return "", fmt.Errorf("check username: %w", err)
	}

	// Username taken — append random suffix.
	for i := 0; i < 10; i++ {
		candidate := username + "-" + id.Generate()[:6]
		_, err := q.GetUserByUsername(ctx, candidate)
		if err == sql.ErrNoRows {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("could not find unique username")
}
