package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// helper: build a json.RawMessage from an arbitrary value.
func raw(t *testing.T, v interface{}) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(v)
	require.NoError(t, err)
	return data
}

// helper: parse a json.RawMessage back into a map.
func parseRaw(t *testing.T, r json.RawMessage) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	require.NoError(t, json.Unmarshal(r, &m))
	return m
}

// helper: get the "type" string from a raw message.
func msgType(t *testing.T, r json.RawMessage) string {
	t.Helper()
	m := parseRaw(t, r)
	return m["type"].(string)
}

// helper: collect types from a slice of raw messages.
func types(t *testing.T, msgs []json.RawMessage) []string {
	t.Helper()
	result := make([]string, len(msgs))
	for i, m := range msgs {
		parsed := parseRaw(t, m)
		if method, ok := parsed["method"].(string); ok && method != "" {
			result[i] = method
			continue
		}
		result[i] = parsed["type"].(string)
	}
	return result
}

func settingsChanged(old, new string) map[string]interface{} {
	return map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]interface{}{"old": old, "new": new},
		},
	}
}

func codexStartupStatus(name, status string, errorText interface{}) map[string]interface{} {
	return map[string]interface{}{
		"method": "mcpServer/startupStatus/updated",
		"params": map[string]interface{}{
			"name":   name,
			"status": status,
			"error":  errorText,
		},
	}
}

func consolidateForProvider(provider leapmuxv1.AgentProvider, msgs []json.RawMessage) []json.RawMessage {
	return consolidateNotificationThread(msgs, agent.NotificationConsolidatorForProvider(provider))
}

func TestConsolidateNotificationThread_OrderPreserved(t *testing.T) {
	t.Run("context_cleared then settings_changed", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("A", "B")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"context_cleared", "settings_changed"}, types(t, result))
	})

	t.Run("settings_changed then context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"settings_changed", "context_cleared"}, types(t, result))
	})

	t.Run("api_retry then context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 3}),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		require.Len(t, result, 2)
		first := parseRaw(t, result[0])
		second := parseRaw(t, result[1])
		assert.Equal(t, "system", first["type"])
		assert.Equal(t, "api_retry", first["subtype"])
		assert.Equal(t, "context_cleared", second["type"])
	})
}

func TestConsolidateNotificationThread_Dedup(t *testing.T) {
	t.Run("context_cleared deduped to last occurrence", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"settings_changed", "context_cleared"}, types(t, result))
	})

	t.Run("settings_changed deduped to last occurrence", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("B", "C")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"context_cleared", "settings_changed"}, types(t, result))
		// Verify merged: old=A, new=C
		changes := parseRaw(t, result[1])["changes"].(map[string]interface{})
		model := changes["model"].(map[string]interface{})
		assert.Equal(t, "A", model["old"])
		assert.Equal(t, "C", model["new"])
	})

	t.Run("api_retry deduped to last occurrence after context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "system", "subtype": "api_retry", "attempt": 1, "max_retries": 3}),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "api_retry", "attempt": 2, "max_retries": 3}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		require.Len(t, result, 2)
		first := parseRaw(t, result[0])
		second := parseRaw(t, result[1])
		assert.Equal(t, "context_cleared", first["type"])
		assert.Equal(t, "system", second["type"])
		assert.Equal(t, "api_retry", second["subtype"])
		assert.EqualValues(t, 2, second["attempt"])
	})
}

func TestConsolidateNotificationThread_ChangesCancelOut(t *testing.T) {
	t.Run("settings_changed cancels out when A->B then B->A", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("B", "A")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		// settings_changed drops because old==new; only context_cleared remains.
		assert.Equal(t, []string{"context_cleared"}, types(t, result))
	})

	t.Run("settings_changed alone cancels out", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, settingsChanged("B", "A")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []json.RawMessage{}, result)
	})
}

func TestConsolidateNotificationThread_SettingsMerged(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, settingsChanged("A", "B")),
		raw(t, settingsChanged("B", "C")),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	require.Len(t, result, 1)
	assert.Equal(t, "settings_changed", msgType(t, result[0]))
	changes := parseRaw(t, result[0])["changes"].(map[string]interface{})
	model := changes["model"].(map[string]interface{})
	assert.Equal(t, "A", model["old"])
	assert.Equal(t, "C", model["new"])
}

func TestConsolidateNotificationThread_PlanExecution(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{"type": "plan_execution", "plan_file_path": "/p.md"}),
		raw(t, map[string]interface{}{"type": "context_cleared"}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	assert.Equal(t, []string{"plan_execution", "context_cleared"}, types(t, result))
}

func TestConsolidateNotificationThread_NoContextClearedOnSettingsChanged(t *testing.T) {
	// Even if the input has contextCleared on settings_changed, output must not.
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{
			"type":           "settings_changed",
			"changes":        map[string]interface{}{"model": map[string]interface{}{"old": "A", "new": "B"}},
			"contextCleared": true,
		}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	require.Len(t, result, 1)
	m := parseRaw(t, result[0])
	assert.Nil(t, m["contextCleared"], "contextCleared should not appear in output")
}

func TestConsolidateNotificationThread_CompactionBoundariesKept(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		raw(t, map[string]interface{}{"type": "system", "subtype": "microcompact_boundary"}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	assert.Len(t, result, 2)
}

func TestConsolidateNotificationThread_Interrupted(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, settingsChanged("A", "B")),
		raw(t, map[string]interface{}{"type": "interrupted"}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	assert.Equal(t, []string{"settings_changed", "interrupted"}, types(t, result))
}

func TestConsolidateNotificationThread_RateLimit(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{
			"type":            "rate_limit",
			"rate_limit_info": map[string]interface{}{"rateLimitType": "five_hour", "status": "exceeded"},
		}),
		raw(t, map[string]interface{}{
			"type":            "rate_limit",
			"rate_limit_info": map[string]interface{}{"rateLimitType": "five_hour", "status": "allowed"},
		}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	require.Len(t, result, 1)
	m := parseRaw(t, result[0])
	info := m["rate_limit_info"].(map[string]interface{})
	assert.Equal(t, "allowed", info["status"])
}

func TestConsolidateNotificationThread_SystemStatus(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{"type": "system", "subtype": "status", "status": "compacting"}),
		raw(t, map[string]interface{}{"type": "system", "subtype": "status", "status": "idle"}),
	}
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
	require.Len(t, result, 1)
	m := parseRaw(t, result[0])
	assert.Equal(t, "idle", m["status"])
}

func TestConsolidateNotificationThread_CompactionSupersedes_ContextCleared(t *testing.T) {
	t.Run("compact_boundary after context_cleared drops context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"system"}, types(t, result))
	})

	t.Run("microcompact_boundary after context_cleared drops context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "microcompact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"system"}, types(t, result))
	})

	t.Run("context_cleared after compact_boundary drops compact_boundary", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"context_cleared"}, types(t, result))
	})

	t.Run("context_cleared after microcompact_boundary drops microcompact_boundary", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "system", "subtype": "microcompact_boundary"}),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"context_cleared"}, types(t, result))
	})

	t.Run("settings_changed preserved when compaction supersedes context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"settings_changed", "system"}, types(t, result))
	})
}

func TestConsolidateNotificationThread_CompactingDroppedByBoundary(t *testing.T) {
	t.Run("compacting dropped when compact_boundary follows", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "compacting"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		require.Len(t, result, 1)
		m := parseRaw(t, result[0])
		assert.Equal(t, "system", m["type"])
		assert.Equal(t, "compact_boundary", m["subtype"])
	})

	t.Run("compacting dropped when microcompact_boundary follows", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "compacting"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "microcompact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		require.Len(t, result, 1)
		m := parseRaw(t, result[0])
		assert.Equal(t, "system", m["type"])
		assert.Equal(t, "microcompact_boundary", m["subtype"])
	})

	t.Run("compacting kept when no boundary follows", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "compacting"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		assert.Equal(t, []string{"compacting"}, types(t, result))
	})

	t.Run("compacting with settings_changed and boundary", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "compacting"}),
			raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, msgs)
		require.Len(t, result, 2)
		assert.Equal(t, "settings_changed", msgType(t, result[0]))
		m := parseRaw(t, result[1])
		assert.Equal(t, "system", m["type"])
		assert.Equal(t, "compact_boundary", m["subtype"])
	})
}

func TestConsolidateNotificationThread_Empty(t *testing.T) {
	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, nil)
	assert.Equal(t, []json.RawMessage{}, result)
}

func TestConsolidateNotificationThread_CodexMcpStartupStatus(t *testing.T) {
	t.Run("starting then ready collapses to ready", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, codexStartupStatus("codex_apps", "starting", nil)),
			raw(t, codexStartupStatus("codex_apps", "ready", nil)),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, msgs)
		require.Len(t, result, 1)
		m := parseRaw(t, result[0])
		assert.Equal(t, "mcpServer/startupStatus/updated", m["method"])
		params := m["params"].(map[string]interface{})
		assert.Equal(t, "codex_apps", params["name"])
		assert.Equal(t, "ready", params["status"])
	})

	t.Run("starting then failed collapses to failed", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, codexStartupStatus("codex_apps", "starting", nil)),
			raw(t, codexStartupStatus("codex_apps", "failed", "boom")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, msgs)
		require.Len(t, result, 1)
		params := parseRaw(t, result[0])["params"].(map[string]interface{})
		assert.Equal(t, "failed", params["status"])
		assert.Equal(t, "boom", params["error"])
	})

	t.Run("starting then cancelled collapses to cancelled", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, codexStartupStatus("codex_apps", "starting", nil)),
			raw(t, codexStartupStatus("codex_apps", "cancelled", nil)),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, msgs)
		require.Len(t, result, 1)
		params := parseRaw(t, result[0])["params"].(map[string]interface{})
		assert.Equal(t, "cancelled", params["status"])
	})

	t.Run("different servers keep separate latest entries in order", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, codexStartupStatus("codex_apps", "starting", nil)),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, codexStartupStatus("other", "starting", nil)),
			raw(t, codexStartupStatus("codex_apps", "ready", nil)),
			raw(t, codexStartupStatus("other", "failed", "boom")),
		}
		result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX, msgs)
		assert.Equal(t, []string{"context_cleared", "mcpServer/startupStatus/updated", "mcpServer/startupStatus/updated"}, types(t, result))

		firstParams := parseRaw(t, result[1])["params"].(map[string]interface{})
		secondParams := parseRaw(t, result[2])["params"].(map[string]interface{})
		assert.Equal(t, "codex_apps", firstParams["name"])
		assert.Equal(t, "ready", firstParams["status"])
		assert.Equal(t, "other", secondParams["name"])
		assert.Equal(t, "failed", secondParams["status"])
		assert.Equal(t, "boom", secondParams["error"])
	})
}

func TestConsolidateNotificationThread_DefaultProviderKeepsUnknownProviderNotifications(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, codexStartupStatus("codex_apps", "starting", nil)),
		raw(t, codexStartupStatus("codex_apps", "ready", nil)),
	}

	result := consolidateForProvider(leapmuxv1.AgentProvider_AGENT_PROVIDER_OPENCODE, msgs)
	require.Len(t, result, 2)
	assert.Equal(t, []string{"mcpServer/startupStatus/updated", "mcpServer/startupStatus/updated"}, types(t, result))
}
