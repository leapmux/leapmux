package auth_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/hub/auth"
)

func TestBuildSessionCookie_Insecure(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	c := auth.BuildSessionCookie("sess-123", expires, false)

	assert.Equal(t, auth.CookieName, c.Name)
	assert.Equal(t, "sess-123", c.Value)
	assert.Equal(t, "/", c.Path)
	assert.True(t, c.HttpOnly)
	assert.False(t, c.Secure)
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
}

func TestBuildSessionCookie_Secure(t *testing.T) {
	expires := time.Now().Add(24 * time.Hour)
	c := auth.BuildSessionCookie("sess-456", expires, true)

	assert.Equal(t, auth.SecureCookieName, c.Name)
	assert.True(t, c.Secure)
	assert.True(t, c.HttpOnly)
}

func TestClearSessionCookie(t *testing.T) {
	c := auth.ClearSessionCookie(false)

	assert.Equal(t, auth.CookieName, c.Name)
	assert.Empty(t, c.Value)
	assert.True(t, c.MaxAge < 0)
	assert.True(t, c.HttpOnly)
}

func TestSessionIDFromHeader_ValidCookie(t *testing.T) {
	header := auth.CookieName + "=my-session-id"
	got := auth.SessionIDFromHeader(header, false)
	assert.Equal(t, "my-session-id", got)
}

func TestSessionIDFromHeader_MultipleCookies(t *testing.T) {
	header := "other=value; " + auth.CookieName + "=correct-id; foo=bar"
	got := auth.SessionIDFromHeader(header, false)
	assert.Equal(t, "correct-id", got)
}

func TestSessionIDFromHeader_NoCookie(t *testing.T) {
	got := auth.SessionIDFromHeader("other=value; foo=bar", false)
	assert.Empty(t, got)
}

func TestSessionIDFromHeader_EmptyHeader(t *testing.T) {
	got := auth.SessionIDFromHeader("", false)
	assert.Empty(t, got)
}

func TestSessionIDFromHeader_SecureCookie(t *testing.T) {
	header := auth.SecureCookieName + "=secure-id"
	got := auth.SessionIDFromHeader(header, true)
	assert.Equal(t, "secure-id", got)
}
