package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	"github.com/leapmux/leapmux/internal/hub/keystore"
	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	oauthStateExpiry         = 5 * time.Minute
	pendingOAuthSignupExpiry = 5 * time.Minute
	defaultTokenExpiry       = 1 * time.Hour
)

// OAuthHandler handles OAuth login/callback HTTP endpoints.
type OAuthHandler struct {
	store    store.Store
	cfg      *config.Config
	keystore *keystore.Keystore

	// providers caches built Provider instances by provider ID.
	// Provider config (issuer, client_id, scopes) is immutable after creation,
	// so entries stay valid for the lifetime of the process.
	providers   map[string]huboauth.Provider
	providersMu sync.RWMutex
}

// NewOAuthHandler creates a new OAuth HTTP handler.
func NewOAuthHandler(st store.Store, cfg *config.Config, ks *keystore.Keystore) *OAuthHandler {
	return &OAuthHandler{store: st, cfg: cfg, keystore: ks, providers: make(map[string]huboauth.Provider)}
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
// builds the cached Provider instance. Returns the provider, its trust_email
// setting, and whether the load succeeded. Writes an HTTP error on failure.
func (h *OAuthHandler) loadEnabledProvider(w http.ResponseWriter, ctx context.Context, providerID string) (huboauth.Provider, bool, bool) {
	dbProvider, err := h.store.OAuthProviders().GetByID(ctx, providerID)
	if err != nil {
		http.Error(w, "unknown provider", http.StatusNotFound)
		return nil, false, false
	}
	if !dbProvider.Enabled {
		http.Error(w, "provider disabled", http.StatusForbidden)
		return nil, false, false
	}
	provider, err := h.buildProvider(ctx, dbProvider)
	if err != nil {
		slog.Error("oauth: build provider", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return nil, false, false
	}
	return provider, dbProvider.TrustEmail, true
}

func (h *OAuthHandler) handleLogin(w http.ResponseWriter, r *http.Request, providerID string) {
	ctx := r.Context()

	provider, _, ok := h.loadEnabledProvider(w, ctx, providerID)
	if !ok {
		return
	}

	verifier := oauth2.GenerateVerifier()
	state := id.Generate()

	redirectURI := sanitizeRedirectURI(r.URL.Query().Get("redirect"))

	if err := h.store.OAuthStates().Create(ctx, store.CreateOAuthStateParams{
		State:        state,
		ProviderID:   providerID,
		PkceVerifier: verifier,
		RedirectURI:  redirectURI,
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

	oauthState, err := h.store.OAuthStates().Get(ctx, state)
	if err != nil {
		http.Error(w, "invalid or expired state", http.StatusBadRequest)
		return
	}
	_ = h.store.OAuthStates().Delete(ctx, state)

	if time.Now().UTC().After(oauthState.ExpiresAt) {
		http.Error(w, "state expired", http.StatusBadRequest)
		return
	}
	if oauthState.ProviderID != providerID {
		http.Error(w, "state/provider mismatch", http.StatusBadRequest)
		return
	}

	provider, trustEmail, ok := h.loadEnabledProvider(w, ctx, providerID)
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

	// Require a valid email from the OAuth provider.
	if claims.Email == "" {
		slog.Error("oauth: provider did not return an email", "provider_id", providerID)
		http.Error(w, "OAuth provider did not return an email address; ensure the 'email' scope is granted", http.StatusBadRequest)
		return
	}
	if err := validate.ValidateEmail(claims.Email); err != nil {
		slog.Error("oauth: provider returned invalid email", "email", claims.Email, "provider_id", providerID)
		http.Error(w, "OAuth provider returned an invalid email address", http.StatusBadRequest)
		return
	}

	// Check if this OAuth identity is already linked to a user.
	link, err := h.store.OAuthUserLinks().Get(ctx, store.GetOAuthUserLinkParams{
		ProviderID:      providerID,
		ProviderSubject: claims.Subject,
	})
	if err == nil {
		// Existing link — direct login.
		user, userErr := h.store.Users().GetByID(ctx, link.UserID)
		if userErr != nil {
			http.Error(w, "linked user not found", http.StatusInternalServerError)
			return
		}

		h.loginOAuthUser(w, r, user.ID, providerID, tokenSet, oauthState.RedirectURI)
		return
	}
	if !errors.Is(err, store.ErrNotFound) {
		slog.Error("oauth: query user link", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Auto-link by verified email: if the OAuth provider is trusted, look for
	// an existing user with the same verified email and link automatically.
	if trustEmail {
		existingUser, emailErr := h.store.Users().GetByEmail(ctx, claims.Email)
		if emailErr == nil && existingUser.EmailVerified {
			if err := h.store.OAuthUserLinks().Create(ctx, store.CreateOAuthUserLinkParams{
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
			h.loginOAuthUser(w, r, existingUser.ID, providerID, tokenSet, oauthState.RedirectURI)
			return
		}
	}

	// New user — store pending signup for username selection.
	if !h.cfg.SignupEnabled {
		http.Error(w, "sign-up is disabled; no existing account linked to this identity", http.StatusForbidden)
		return
	}

	signupToken, err := h.storePendingSignup(ctx, providerID, claims, tokenSet, oauthState.RedirectURI)
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

	sessionID, expiresAt, sessionErr := auth.CreateSession(ctx, h.store, userID, auth.SessionMeta{
		UserAgent: r.UserAgent(),
		IPAddress: r.RemoteAddr,
	})
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

// sanitizeRedirectURI ensures the redirect URI is a safe relative path.
// Returns empty string for anything that could be an open redirect.
func sanitizeRedirectURI(uri string) string {
	if uri == "" {
		return ""
	}
	// Must start with "/" and must not start with "//" (protocol-relative URL).
	if uri[0] != '/' || (len(uri) > 1 && uri[1] == '/') {
		return ""
	}
	return uri
}

func tokenExpiryTime(tokenSet *huboauth.TokenSet) time.Time {
	if !tokenSet.ExpiresAt.IsZero() {
		return tokenSet.ExpiresAt
	}
	return time.Now().Add(defaultTokenExpiry).UTC()
}

func (h *OAuthHandler) storePendingSignup(ctx context.Context, providerID string, claims *huboauth.UserClaims, tokenSet *huboauth.TokenSet, redirectURI string) (string, error) {
	token := id.Generate()

	// Use the signup token as entity ID for AAD since the user doesn't exist yet.
	encAccessToken, encRefreshToken, err := encryptTokenPair(h.keystore, tokenSet.AccessToken, tokenSet.RefreshToken, token, providerID)
	if err != nil {
		return "", err
	}

	tokenExpiresAt := tokenExpiryTime(tokenSet)

	displayName := claims.DisplayName
	if displayName == "" {
		displayName = claims.Name
	}

	if err := h.store.PendingOAuthSignups().Create(ctx, store.CreatePendingOAuthSignupParams{
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
		RedirectURI:     redirectURI,
		ExpiresAt:       time.Now().Add(pendingOAuthSignupExpiry).UTC(),
	}); err != nil {
		return "", fmt.Errorf("create pending signup: %w", err)
	}

	return token, nil
}

// encryptTokenPair encrypts an access/refresh token pair. The entityID is used
// as part of the AAD and is typically a user ID or a pending-signup token.
func encryptTokenPair(ks *keystore.Keystore, accessToken, refreshToken string, entityID, providerID string) (encAccess, encRefresh []byte, err error) {
	encAccess, err = ks.Encrypt([]byte(accessToken), keystore.AccessTokenAAD(entityID, providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt access token: %w", err)
	}
	encRefresh, err = ks.Encrypt([]byte(refreshToken), keystore.RefreshTokenAAD(entityID, providerID))
	if err != nil {
		return nil, nil, fmt.Errorf("encrypt refresh token: %w", err)
	}
	return encAccess, encRefresh, nil
}

func (h *OAuthHandler) storeTokens(ctx context.Context, userID, providerID string, tokenSet *huboauth.TokenSet) error {
	encAccess, encRefresh, err := encryptTokenPair(h.keystore, tokenSet.AccessToken, tokenSet.RefreshToken, userID, providerID)
	if err != nil {
		return err
	}

	expiresAt := tokenExpiryTime(tokenSet)

	return h.store.OAuthTokens().Upsert(ctx, store.UpsertOAuthTokensParams{
		UserID:       userID,
		ProviderID:   providerID,
		AccessToken:  encAccess,
		RefreshToken: encRefresh,
		TokenType:    tokenSet.TokenType,
		ExpiresAt:    expiresAt,
		KeyVersion:   int64(h.keystore.ActiveVersion()),
	})
}

func (h *OAuthHandler) buildProvider(ctx context.Context, dbProvider *store.OAuthProvider) (huboauth.Provider, error) {
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
		provider, err = huboauth.NewOIDCProvider(ctx, dbProvider.IssuerURL, dbProvider.ClientID, string(clientSecret), redirectURL, scopes)
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
