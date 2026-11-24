package oauth

import (
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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
