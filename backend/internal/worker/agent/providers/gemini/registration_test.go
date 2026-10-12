package gemini

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
)

func TestGeminiRegistrationUsesOneNativeModeCatalog(t *testing.T) {
	t.Parallel()
	registration := Registration()
	assert.Equal(t, leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI, registration.Provider)
	assert.Equal(t, "LEAPMUX_GEMINI_DEFAULT_MODEL", registration.EnvModelKey)
	assert.Empty(t, registration.EnvEffortKey)
	assert.Equal(t, contracts.GeminiModeDefault, registration.PermissionDefaults.Fallback)
	assert.Equal(t, contracts.GeminiModeDefault, registration.PermissionDefaults.NewSession[agent.OptionIDPermissionMode])
	acptest.AssertSecondaryFallback(t, acp.SecondaryFallbackFrom(geminiStaticOptionGroups, acp.ModeChannelPermissionMode), geminiModes(), registration.OptionGroups, geminiStaticOptionGroups)
}
