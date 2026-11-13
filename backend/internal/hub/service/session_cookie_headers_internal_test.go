package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func TestSessionCookieHeaderValuesPreservesOtherCookies(t *testing.T) {
	for _, secure := range []bool{false, true} {
		existing := []string{"oauth-state=bound; Path=/", auth.ClearSessionCookie(false).String(), auth.ClearSessionCookie(true).String(), "this is not a cookie"}
		active := auth.BuildSessionCookie("new-session", time.Now().Add(time.Hour), secure)
		clear := auth.ClearSessionCookie(!secure)
		got := sessionCookieHeaderValues(existing, active, clear)
		require.Equal(t, []string{existing[0], existing[3], active.String(), clear.String()}, got)
		for _, line := range got[2:] {
			cookie, err := http.ParseSetCookie(line)
			require.NoError(t, err)
			assert.True(t, cookie.HttpOnly)
			assert.Equal(t, "/", cookie.Path)
			assert.Empty(t, cookie.Domain)
			assert.Equal(t, http.SameSiteLaxMode, cookie.SameSite)
		}
	}
	assert.Empty(t, sessionCookieHeaderValues(nil))
}
