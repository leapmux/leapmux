package service_test

import (
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/leapmux/leapmux/internal/hub/auth"
	"github.com/leapmux/leapmux/internal/hub/config"
	hubtestutil "github.com/leapmux/leapmux/internal/hub/testutil"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

var ptrTime = ptrconv.Ptr[time.Time]

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Cookie", auth.CookieName+"="+token)
	return req
}

func sessionFromCookie(t *testing.T, setCookie string) string {
	return hubtestutil.SessionFromCookie(t, setCookie)
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
