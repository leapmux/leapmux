package service_test

import (
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
)

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	return req
}

// sessionFromCookie extracts the session ID from a Set-Cookie header value.
func sessionFromCookie(t *testing.T, setCookie string) string {
	t.Helper()
	require.NotEmpty(t, setCookie, "Set-Cookie header must be present")
	for _, part := range strings.Split(setCookie, ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, auth.CookieName+"=") {
			return strings.TrimPrefix(part, auth.CookieName+"=")
		}
	}
	t.Fatalf("session cookie %q not found in Set-Cookie: %s", auth.CookieName, setCookie)
	return ""
}

func testConfig() *config.Config {
	return &config.Config{
		APITimeoutSeconds:            config.DefaultAPITimeoutSeconds,
		AgentStartupTimeoutSeconds:   config.DefaultAgentStartupTimeoutSeconds,
		WorktreeCreateTimeoutSeconds: config.DefaultWorktreeCreateTimeoutSeconds,
	}
}

func testConfigWithSignup() *config.Config {
	cfg := testConfig()
	cfg.SignupEnabled = true
	return cfg
}
