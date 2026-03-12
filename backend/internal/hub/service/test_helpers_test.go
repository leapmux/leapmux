package service_test

import (
	"connectrpc.com/connect"
	"github.com/leapmux/leapmux/internal/hub/config"
)

func authedReq[T any](msg *T, token string) *connect.Request[T] {
	req := connect.NewRequest(msg)
	req.Header().Set("Authorization", "Bearer "+token)
	return req
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
