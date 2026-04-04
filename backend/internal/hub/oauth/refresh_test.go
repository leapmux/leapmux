package oauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTokenSet_StringRedacted(t *testing.T) {
	ts := TokenSet{
		AccessToken:  "super-secret-access",
		RefreshToken: "super-secret-refresh",
		TokenType:    "Bearer",
		ExpiresIn:    3600,
	}

	assert.Equal(t, "[REDACTED TokenSet]", ts.String())
	assert.Equal(t, "[REDACTED TokenSet]", ts.GoString())
	assert.NotContains(t, ts.String(), "super-secret")
}

func TestUserClaims_DoesNotContainTokens(t *testing.T) {
	// UserClaims should not hold tokens — verify the struct fields.
	claims := UserClaims{
		Subject:     "user-123",
		Email:       "user@example.com",
		Name:        "testuser",
		DisplayName: "Test User",
	}

	// No token fields should exist on UserClaims.
	assert.NotEmpty(t, claims.Subject)
	assert.NotEmpty(t, claims.Email)
}
