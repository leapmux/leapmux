package claude

import (
	"encoding/json"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestIsNotificationThreadable_ClaudeRateLimitEventAsAgent(t *testing.T) {
	t.Parallel()

	// rate_limit_event arrives as AGENT. The plugin classifies it as
	// provider-scoped, so isNotificationThreadable returns true and it
	// threads with surrounding notifications.
	assert.True(t, isNotificationThreadable([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"exceeded"}}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
}

func TestIsNotificationThreadable_ClaudeStatusCompactingAsAgent(t *testing.T) {
	t.Parallel()

	// The worker persists the raw `system` message as AGENT (not a
	// synthesized `{type:"compacting"}` envelope), and
	// isNotificationThreadable still returns true because the plugin
	// classifies it as a Status notification.
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"compacting"}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
}

func TestIsNotificationThreadable_ClaudeSystemUsesPlugin(t *testing.T) {
	t.Parallel()

	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"idle"}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	assert.False(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"other"}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
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
		agent.NotificationClassification{Kind: agent.NotificationKindStatus, Key: "claude:system:status"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"status","status":"idle"}`)),
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindAPIRetry, Key: "claude:system:api_retry"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"api_retry","attempt":2}`)),
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindCompactionBoundary, Key: "claude:system:compact_boundary"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"compact_boundary"}`)),
	)
	assert.Equal(t,
		agent.NotificationClassification{Kind: agent.NotificationKindProviderScoped, Key: "claude:rate_limit_event"},
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
