package claude

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestClaudeIsConsolidatable_RateLimitEvent(t *testing.T) {
	t.Parallel()

	assert.True(t, claudeIsConsolidatable([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"exceeded"}}`)))
}

func TestClaudeClassify_OnlyAFailedStatusIsAClassifiedStatus(t *testing.T) {
	t.Parallel()
	assert.Equal(t, agent.CompactionBoundaryNotification(), claudeProvider{}.Classify([]byte(`{"type":"system","subtype":"status","status":null,"compact_result":"failed"}`)))
	assert.Equal(t, agent.NotificationClassification{}, claudeProvider{}.Classify([]byte(`{"type":"system","subtype":"status","status":"compacting"}`)))
	assert.Equal(t, agent.NotificationClassification{}, claudeProvider{}.Classify([]byte(`{"type":"system","subtype":"status","status":null,"compact_result":"success"}`)))
}

func TestClaudeClassifyNotificationUsesItsNativeKey(t *testing.T) {
	t.Parallel()
	plugin := claudeProvider{}
	// The hooks_fail probe identifies its repeated notice with this key.
	assert.Equal(t, agent.ProviderScopedNotification("claude:notification:stop-hook-error"), plugin.Classify([]byte(`{"type":"system","subtype":"notification","key":"stop-hook-error","message":"Stop hook error occurred"}`)))
	for _, frame := range []string{
		`{"type":"system","subtype":"notification"}`,
		`{"type":"system","subtype":"notification","key":""}`,
		`{"type":"system","subtype":"notification","key":null}`,
		`{"type":"system","subtype":"notification","key":12}`,
	} {
		assert.Equal(t, agent.NotificationClassification{}, plugin.Classify([]byte(frame)))
	}
}

func TestClaudeIsConsolidatable_SystemUsesPlugin(t *testing.T) {
	t.Parallel()

	assert.False(t, claudeIsConsolidatable([]byte(`{"type":"system","subtype":"status","status":"idle"}`)))
	assert.True(t, claudeIsConsolidatable([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`)))
	assert.False(t, claudeIsConsolidatable([]byte(`{"type":"system","subtype":"other"}`)))
}

func TestPermissionModeFromRawInput(t *testing.T) {
	t.Parallel()

	// Claude owns the set_permission_mode wire parse behind the Provider interface.
	claude := claudeProvider{}

	mode, ok := claude.PermissionModeFromRawInput(`{"type":"control_request","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`)
	assert.True(t, ok)
	assert.Equal(t, "bypassPermissions", mode)

	// A control_request that isn't set_permission_mode, an empty mode, invalid JSON that happens to
	// contain the substring, and unrelated content (fast-path miss) all yield ("", false).
	for name, content := range map[string]string{
		"wrong subtype":     `{"request":{"subtype":"can_use_tool","mode":"bypassPermissions"}}`,
		"empty mode":        `{"request":{"subtype":"set_permission_mode","mode":""}}`,
		"invalid json":      `{"request":{"subtype":"set_permission_mode",`,
		"unrelated content": `{"type":"control_request","request":{"subtype":"interrupt"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			mode, ok := claude.PermissionModeFromRawInput(content)
			assert.False(t, ok)
			assert.Empty(t, mode)
		})
	}
}

func TestProviderFor_ClaudeClassification(t *testing.T) {
	t.Parallel()

	registry := agenttest.MustNewRegistry(Registration())

	plugin := registry.Plugin(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	assert.Equal(t,
		agent.NotificationClassification{},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"status","status":"idle"}`)),
	)
	assert.Equal(t,
		agent.APIRetryNotification(),
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"api_retry","attempt":2}`)),
	)
	assert.Equal(t,
		agent.CompactionBoundaryNotification(),
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"compact_boundary"}`)),
	)
	assert.Equal(t, agent.CompactionBoundaryNotification(), plugin.Classify(json.RawMessage(`{"type":"system","subtype":"microcompact_boundary"}`)))
	assert.Equal(t, agent.NotificationClassification{}, plugin.Classify(json.RawMessage(`{"type":"system","subtype":"unknown"}`)))
	assert.Equal(t, agent.NotificationClassification{}, plugin.Classify(json.RawMessage(`invalid JSON`)))
	assert.Equal(t,
		agent.ProviderScopedNotification("claude:rate_limit_event"),
		plugin.Classify(json.RawMessage(`{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"exceeded"}}`)),
		"raw rate_limit_event must be consolidatable so a flurry of tier updates collapses to the latest snapshot",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"rate_limit","rate_limit_info":{}}`)).Consolidatable(),
		"the legacy synthesized {type:\"rate_limit\"} envelope is no longer consolidatable — old DB rows render via raw-JSON fallback",
	)
}

// The plugin states the child capabilities that the agent type implements. A
// subagent tab reads them before its root runs.
func TestPluginStatesTheChildCapabilitiesOfTheAgent(t *testing.T) {
	t.Parallel()
	agenttest.AssertChildCapabilities(t, Registration().Plugin, (*Agent)(nil))
}
