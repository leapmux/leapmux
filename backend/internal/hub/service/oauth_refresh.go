package service

import (
	"context"
	"log/slog"
	"time"

	huboauth "github.com/leapmux/leapmux/internal/hub/oauth"
)

// StartTokenRefresh starts a background goroutine that periodically refreshes
// OAuth tokens that are about to expire. It stops when ctx is cancelled.
func (h *OAuthHandler) StartTokenRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.refreshExpiringTokens(ctx)
			}
		}
	}()
}

func (h *OAuthHandler) refreshExpiringTokens(ctx context.Context) {
	tokens, err := h.queries.ListExpiringOAuthTokens(ctx)
	if err != nil {
		slog.Error("oauth refresh: list expiring tokens", "error", err)
		return
	}

	for _, tok := range tokens {
		provider, err := h.queries.GetOAuthProviderByID(ctx, tok.ProviderID)
		if err != nil {
			slog.Error("oauth refresh: get provider", "provider_id", tok.ProviderID, "error", err)
			continue
		}

		p, err := h.buildProvider(ctx, &provider)
		if err != nil {
			slog.Error("oauth refresh: build provider", "provider_id", tok.ProviderID, "error", err)
			continue
		}

		// Decrypt the refresh token.
		refreshTokenAAD := []byte("refresh_token:" + tok.UserID + ":" + tok.ProviderID)
		refreshTokenPlain, err := h.keystore.Decrypt(tok.RefreshToken, refreshTokenAAD)
		if err != nil {
			slog.Error("oauth refresh: decrypt refresh token", "user_id", tok.UserID, "error", err)
			continue
		}

		newTokens, err := p.Refresh(ctx, string(refreshTokenPlain))
		if err != nil {
			slog.Warn("oauth refresh: refresh failed, deleting tokens", "user_id", tok.UserID, "provider_id", tok.ProviderID, "error", err)
			_ = h.queries.DeleteOAuthTokensByUser(ctx, tok.UserID)
			continue
		}

		if err := h.storeTokens(ctx, tok.UserID, tok.ProviderID, &huboauth.TokenSet{
			AccessToken:  newTokens.AccessToken,
			RefreshToken: newTokens.RefreshToken,
			TokenType:    newTokens.TokenType,
			ExpiresIn:    newTokens.ExpiresIn,
		}); err != nil {
			slog.Error("oauth refresh: store tokens", "user_id", tok.UserID, "error", err)
		}
	}
}
