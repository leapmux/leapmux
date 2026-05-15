package oauth

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTokenSet_StringRedacted(t *testing.T) {
	ts := TokenSet{
		AccessToken:  "super-secret-access",
		RefreshToken: "super-secret-refresh",
		TokenType:    "Bearer",
		ExpiresAt:    time.Now().Add(1 * time.Hour).UTC(),
	}

	assert.Equal(t, "[REDACTED TokenSet]", ts.String())
	assert.Equal(t, "[REDACTED TokenSet]", ts.GoString())
	assert.NotContains(t, ts.String(), "super-secret")
}

func TestUserClaims_DoesNotContainTokens(t *testing.T) {
	// If a token field is ever added to UserClaims, this reflection-based
	// check fails — the type system guarantee in provider.go is enforced
	// for future maintainers, not just by today's struct definition.
	typ := reflect.TypeOf(UserClaims{})
	for i := range typ.NumField() {
		name := strings.ToLower(typ.Field(i).Name)
		assert.NotContains(t, name, "token", "UserClaims must not contain token fields, got %q", typ.Field(i).Name)
		assert.NotContains(t, name, "secret", "UserClaims must not contain secret fields, got %q", typ.Field(i).Name)
		assert.NotContains(t, name, "password", "UserClaims must not contain password fields, got %q", typ.Field(i).Name)
	}
}
