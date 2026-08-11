package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

func TestProviderFor_CodexClassification(t *testing.T) {
	t.Parallel()

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
		"item/started for non-contextCompaction items must NOT be classified as a notification — those go through PersistMessage as AGENT spans")
}

func TestProviderFor_TurnEndToolUses(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		content  string
		wantOK   bool
		want     int32
	}{
		{"claude present", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","num_tool_uses":3}`, true, 3},
		{"claude absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result"}`, false, 0},
		{"claude malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{`, false, 0},
		{"codex present", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"num_tool_uses":2}`, true, 2},
		{"codex absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"type":"result"}`, false, 0},
		{"codex malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{`, false, 0},
		{"pi present", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"num_tool_uses":0}`, true, 0},
		{"pi absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{}`, false, 0},
		{"pi malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{`, false, 0},
		{"acp present", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"num_tool_uses":1}`, true, 1},
		{"acp absent", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false, 0},
		{"acp malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count, ok := ProviderFor(tc.provider).TurnEndToolUses([]byte(tc.content))
			assert.Equal(t, tc.wantOK, ok)
			if ok {
				assert.Equal(t, tc.want, count)
			}
		})
	}
}

// IsTurnEndEnvelope decides whether a SUBAGENT transcript already closes
// itself, so the worker knows whether to add its own subagent-end divider.
// Only Claude forwards a subagent's terminal envelope; everyone else must
// answer false or their child transcripts would end with no divider at all.
func TestProviderFor_IsTurnEndEnvelope(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		provider leapmuxv1.AgentProvider
		content  string
		want     bool
	}{
		{"claude result", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","duration_ms":12}`, true},
		{"claude result with error", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"result","is_error":true}`, true},
		{"claude assistant", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{"type":"assistant"}`, false},
		{"claude no type", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{}`, false},
		{"claude malformed", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, `{`, false},
		{"claude empty", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, ``, false},
		// A result-shaped payload from another provider must NOT suppress that
		// provider's divider: only Claude actually forwards one.
		{"codex result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, `{"type":"result"}`, false},
		{"pi result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_PI, `{"type":"result"}`, false},
		{"opencode result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, `{"type":"result"}`, false},
		{"goose result-shaped", leapmuxv1.AgentProvider_AGENT_PROVIDER_GOOSE, `{"type":"result"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ProviderFor(tc.provider).IsTurnEndEnvelope([]byte(tc.content)))
		})
	}
}

func TestProviderFor_ClaudeClassification(t *testing.T) {
	t.Parallel()

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

func TestProviderFor_PiClassification(t *testing.T) {
	t.Parallel()

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
	t.Parallel()

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
	t.Parallel()

	// ACP-based providers register acpProvider which embeds noop
	// classify/merge — they only provide IsInterrupt. Verify a few of
	// them route to the same behavior.
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_KILO,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_REASONIX,
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
	t.Parallel()

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

func TestPlanApprovalOptions_PerProvider(t *testing.T) {
	t.Parallel()

	// Codex owns its plan-approval option settlement behind the Provider interface: Base
	// resets the collaboration axis, Bypass (mode-switch only) grants full network + no sandbox.
	codex := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX).PlanApprovalOptions()
	assert.Equal(t, map[string]string{CodexOptionCollaborationMode: CodexCollaborationDefault}, codex.Base)
	assert.Equal(t, map[string]string{
		CodexOptionNetworkAccess: CodexNetworkEnabled,
		CodexOptionSandboxPolicy: CodexSandboxDangerFullAccess,
	}, codex.Bypass)

	// Every other provider settles no plan-approval options (no plan-mode-prompt flow).
	for _, provider := range []leapmuxv1.AgentProvider{
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_PI,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_CURSOR,
		leapmuxv1.AgentProvider_AGENT_PROVIDER_UNSPECIFIED,
	} {
		opts := ProviderFor(provider).PlanApprovalOptions()
		assert.Empty(t, opts.Base, "provider %v must settle no base plan-approval options", provider)
		assert.Empty(t, opts.Bypass, "provider %v must settle no bypass plan-approval options", provider)
	}
}

func TestPermissionModeFromRawInput(t *testing.T) {
	t.Parallel()

	// Claude owns the set_permission_mode wire parse behind the Provider interface.
	claude := ProviderFor(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)

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

	// Only Claude speaks set_permission_mode. The exact Claude-shaped payload extracts nothing for
	// every other provider (they return ("", false)), so a stray frame to a non-Claude agent falls
	// through to the generic forward path instead of an eager DB write.
	claudePayload := `{"type":"control_request","request":{"subtype":"set_permission_mode","mode":"bypassPermissions"}}`
	for _, p := range []Provider{codexProvider{}, piProvider{}, acpProvider{}, noopProvider{}} {
		mode, ok := p.PermissionModeFromRawInput(claudePayload)
		assert.False(t, ok)
		assert.Empty(t, mode)
	}
}

func TestIsNotificationThreadable_ClaudeSystemUsesPlugin(t *testing.T) {
	t.Parallel()

	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"status","status":"idle"}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	assert.True(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"api_retry","attempt":1}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
	assert.False(t, isNotificationThreadable([]byte(`{"type":"system","subtype":"other"}`), leapmuxv1.MessageSource_MESSAGE_SOURCE_AGENT))
}
