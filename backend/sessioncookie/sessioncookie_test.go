package sessioncookie_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/sessioncookie"
)

func TestIsNameAcceptsOnlyHubSessionCookies(t *testing.T) {
	t.Parallel()

	assert.True(t, sessioncookie.IsName(sessioncookie.Name))
	assert.True(t, sessioncookie.IsName(sessioncookie.SecureName))
	assert.False(t, sessioncookie.IsName("leapmux-oauth"))
	assert.False(t, sessioncookie.IsName("reverse-proxy-session"))
}
