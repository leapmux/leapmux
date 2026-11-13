package auth

import (
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSessionRefreshClearsItsAlternateSpelling(t *testing.T) {
	for _, secure := range []bool{false, true} {
		h := &connect.Header{}
		sessionRefresh{token: "replacement", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, secure)
		lines := h.Values("Set-Cookie")
		require.Len(t, lines, 2)
		active, err := http.ParseSetCookie(lines[0])
		require.NoError(t, err)
		cleared, err := http.ParseSetCookie(lines[1])
		require.NoError(t, err)
		assert.Equal(t, cookieName(secure), active.Name)
		assert.Equal(t, "replacement", active.Value)
		assert.Equal(t, cookieName(!secure), cleared.Name)
		assert.Empty(t, cleared.Value)
		assert.Negative(t, cleared.MaxAge)
		assert.Equal(t, !secure, cleared.Secure)
	}
}

func TestSessionRefreshPreservesEitherHandlerSpelling(t *testing.T) {
	for _, secure := range []bool{false, true} {
		h := &connect.Header{}
		line := ClearSessionCookie(!secure).String()
		h.Add("Set-Cookie", line)
		sessionRefresh{token: "replacement", expiresAt: time.Now().Add(time.Hour)}.applyTo(h, secure)
		assert.Equal(t, []string{line}, h.Values("Set-Cookie"))
	}
}
