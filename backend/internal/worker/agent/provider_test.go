package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestProviderFor_CodexClassification(t *testing.T) {
	plugin := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX)

	rateLimit := json.RawMessage(`{"method":"account/rateLimits/updated","params":{"foo":"bar"}}`)
	skillsChanged := json.RawMessage(`{"method":"skills/changed","params":{}}`)
	remoteControlStatus := json.RawMessage(`{"method":"remoteControl/status/changed","params":{"status":"disabled","environmentId":null}}`)
	startup := json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps","status":"ready"}}`)
	threadCompacted := json.RawMessage(`{"method":"thread/compacted","params":{"threadId":"t1","turnId":"turn1"}}`)
	contextCompactionStart := json.RawMessage(`{"method":"item/started","params":{"item":{"type":"contextCompaction","id":"compact-1"}}}`)
	commandExecutionStart := json.RawMessage(`{"method":"item/started","params":{"item":{"type":"commandExecution","id":"cmd-1"}}}`)

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:account/rateLimits/updated",
	}, plugin.Classify(rateLimit))

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:skills/changed",
	}, plugin.Classify(skillsChanged))

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:remoteControl/status/changed",
	}, plugin.Classify(remoteControlStatus))

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindProviderScoped,
		Key:  "codex:mcpServer/startupStatus/updated:codex_apps",
	}, plugin.Classify(startup))

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindCompactionBoundary,
		Key:  "codex:thread/compacted",
	}, plugin.Classify(threadCompacted),
		"thread/compacted is the boundary signal — consolidatable so repeated boundaries collapse")

	assert.Equal(t, NotificationClassification{
		Kind: NotificationKindStatus,
		Key:  "codex:item/started:contextCompaction",
	}, plugin.Classify(contextCompactionStart),
		"item/started for a contextCompaction item is the in-progress compacting indicator")

	assert.False(t, plugin.Classify(commandExecutionStart).Consolidatable(),
		"item/started for non-contextCompaction items must NOT be classified as a notification — those go through PersistMessage as ASSISTANT spans")
}

func TestProviderFor_ClaudeClassification(t *testing.T) {
	plugin := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindStatus, Key: "claude:system:status"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"status","status":"idle"}`)),
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindAPIRetry, Key: "claude:system:api_retry"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"api_retry","attempt":2}`)),
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "claude:system:compact_boundary"},
		plugin.Classify(json.RawMessage(`{"type":"system","subtype":"compact_boundary"}`)),
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindProviderScoped, Key: "claude:rate_limit_event"},
		plugin.Classify(json.RawMessage(`{"type":"rate_limit_event","rate_limit_info":{"rateLimitType":"five_hour","status":"exceeded"}}`)),
		"raw rate_limit_event must be consolidatable so a flurry of tier updates collapses to the latest snapshot",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"rate_limit","rate_limit_info":{}}`)).Consolidatable(),
		"the legacy synthesized {type:\"rate_limit\"} envelope is no longer consolidatable — old DB rows render via raw-JSON fallback",
	)
}

func TestIsNotificationThreadable_ClaudeRateLimitEventViaSystem(t *testing.T) {
	// rate_limit_event arrives as SYSTEM after Phase 4.4. The plugin
	// classifies it as provider-scoped, so isNotificationThreadable returns
	// true and it threads with surrounding notifications.
	assert.True(t, isNotificationThreadable([]byte(`{"type":"rate_limit_event","rate_limit_info":{"status":"exceeded"}}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
}

func TestIsNotificationThreadable_ClaudeStatusCompactingViaSystem(t *testing.T) {
	// After Phase 4.1 the worker persists the raw `system` message as
	// SYSTEM (not the synthesized `{type:"compacting"}` envelope), and
	// isNotificationThreadable still returns true because the plugin
	// classifies it as a Status notification.
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"compacting"}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
}

func TestProviderFor_PiClassification(t *testing.T) {
	plugin := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_PI)

	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindStatus, Key: "pi:compaction_start"},
		plugin.Classify(json.RawMessage(`{"type":"compaction_start","sessionId":"s1"}`)),
		"compaction_start is the in-progress indicator — Status so the latest collapses to one entry",
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindCompactionBoundary, Key: "pi:compaction_end"},
		plugin.Classify(json.RawMessage(`{"type":"compaction_end","sessionId":"s1","summary":"compacted"}`)),
		"compaction_end is the boundary signal — each occurrence is preserved as a marker",
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindAPIRetry, Key: "pi:auto_retry_start"},
		plugin.Classify(json.RawMessage(`{"type":"auto_retry_start","attempt":2}`)),
		"auto_retry_start mirrors Claude's api_retry — collapse to the latest attempt",
	)
	assert.Equal(t,
		NotificationClassification{Kind: NotificationKindAPIRetry, Key: "pi:auto_retry_end"},
		plugin.Classify(json.RawMessage(`{"type":"auto_retry_end","attempt":2}`)),
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"extension_error","message":"plugin failed"}`)).Consolidatable(),
		"extension_error is per-error — must not consolidate so partial failures stay visible",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`{"type":"some_future_event"}`)).Consolidatable(),
		"unknown event types must fall through unconsolidated, not panic",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(`not-json`)).Consolidatable(),
		"malformed JSON must produce an empty classification, not a panic",
	)
	assert.False(t,
		plugin.Classify(json.RawMessage(``)).Consolidatable(),
		"empty input must produce an empty classification",
	)

	// Merge mirrors Codex/Claude — last-write-wins with no field merge.
	merged, err := plugin.Merge(
		NotificationClassification{Kind: NotificationKindStatus, Key: "pi:compaction_start"},
		json.RawMessage(`{"type":"compaction_start","attempt":1}`),
		json.RawMessage(`{"type":"compaction_start","attempt":2}`),
	)
	require.NoError(t, err)
	assert.JSONEq(t, `{"type":"compaction_start","attempt":2}`, string(merged))
}

func TestProviderFor_NoopFallback(t *testing.T) {
	// UNSPECIFIED has no registered plugin, so the registry returns the
	// noop plugin: Classify produces empty, Merge keeps the newer entry,
	// IsInterrupt returns false.
	plugin := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED)
	class := plugin.Classify(json.RawMessage(`{"method":"mcpServer/startupStatus/updated","params":{"name":"codex_apps"}}`))
	assert.False(t, class.Consolidatable())

	merged, err := plugin.Merge(NotificationClassification{}, json.RawMessage(`{"a":1}`), json.RawMessage(`{"a":2}`))
	require.NoError(t, err)
	assert.JSONEq(t, `{"a":2}`, string(merged))

	assert.False(t, plugin.IsInterrupt(`{"type":"abort"}`))
}

func TestProviderFor_ACPSharesNoopClassification(t *testing.T) {
	// ACP-based providers register acpProvider which embeds noop
	// classify/merge — they only provide IsInterrupt. Verify a few of
	// them route to the same behavior.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_GEMINI_CLI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
	} {
		plugin := ProviderFor(provider)
		assert.False(t, plugin.Classify(json.RawMessage(`{"method":"session/cancel"}`)).Consolidatable(),
			"ACP provider %v must not consolidate notifications", provider)
		assert.True(t, plugin.IsInterrupt(`{"jsonrpc":"2.0","method":"session/cancel"}`),
			"ACP provider %v must recognize session/cancel as an interrupt", provider)
		assert.True(t, plugin.IsInterrupt(`{"method":"cancel"}`),
			"ACP provider %v must accept the legacy bare cancel form", provider)
	}
}

func TestProviderFor_IsInterruptIsolatedPerProvider(t *testing.T) {
	// Each provider's IsInterrupt must reject formats that belong to other
	// providers — otherwise the dispatcher's provider-aware design would be
	// silently undermined by misclassification.
	cases := []struct {
		name     string
		provider leapmuxv1.AgentProvider
		ownFrame string
	}{
		{"claude", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"control_request","request":{"subtype":"interrupt"}}`},
		{"codex", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"jsonrpc":"2.0","method":"turn/interrupt"}`},
		{"pi", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"type":"abort"}`},
	}
	for _, c := range cases {
		plugin := ProviderFor(c.provider)
		assert.True(t, plugin.IsInterrupt(c.ownFrame), "%s must accept its own interrupt frame", c.name)
		// Cross-provider frames must not match.
		for _, other := range cases {
			if other.name == c.name {
				continue
			}
			assert.False(t, plugin.IsInterrupt(other.ownFrame),
				"%s plugin must reject %s's interrupt frame (%s)", c.name, other.name, other.ownFrame)
		}
		assert.False(t, plugin.IsInterrupt(`not-json`),
			"%s plugin must reject malformed input", c.name)
	}
}

func TestIsNotificationThreadable_ClaudeSystemUsesPlugin(t *testing.T) {
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"idle"}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
	assert.False(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"other"}`), leapmuxv1.MessageRole_MESSAGE_ROLE_SYSTEM))
}
