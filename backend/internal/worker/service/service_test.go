package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "failed to get home dir")

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"bare tilde", "~", home},
		{"tilde slash", "~/Documents", filepath.Join(home, "Documents")},
		{"tilde nested", "~/a/b/c", filepath.Join(home, "a/b/c")},
		{"absolute path unchanged", "/usr/local/bin", "/usr/local/bin"},
		{"relative path unchanged", "some/path", "some/path"},
		{"empty string", "", ""},
		{"double tilde unchanged", "~~", "~~"},
		{"tilde in middle unchanged", "/foo/~/bar", "/foo/~/bar"},
		{"tilde user unchanged", "~user/foo", "~user/foo"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := expandTilde(tt.in)
			assert.Equal(t, tt.want, got, "expandTilde(%q)", tt.in)
		})
	}
}

// TestProviderDisplayName keeps the backend's progress-message label
// mapping in lockstep with the frontend's agentProviderLabel (see
// frontend/src/components/common/AgentProviderIcon.tsx). When a new
// provider is added to the AgentProvider enum, both sides should be
// updated together so "Starting {provider}…" renders consistently.
func TestProviderDisplayName(t *testing.T) {
	cases := []struct {
		provider leapmuxv1.AgentProvider
		want     string
	}{
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, "Claude Code"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, "Codex"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, "Gemini CLI"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, "OpenCode"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GITHUB_COPILOT, "GitHub Copilot"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR, "Cursor"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, "Goose"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO, "Kilo"},
		{leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED, "agent"},
		{leapmuxv1.AgentProvider(9999), "agent"}, // unknown value → generic fallback
	}
	for _, tc := range cases {
		t.Run(tc.provider.String(), func(t *testing.T) {
			assert.Equal(t, tc.want, providerDisplayName(tc.provider))
		})
	}
}
