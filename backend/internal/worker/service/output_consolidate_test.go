package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		result[i] = msgType(t, m)
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

func TestConsolidateNotificationThread_OrderPreserved(t *testing.T) {
	t.Run("context_cleared then settings_changed", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("A", "B")),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []string{"context_cleared", "settings_changed"}, types(t, result))
	})

	t.Run("settings_changed then context_cleared", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []string{"settings_changed", "context_cleared"}, types(t, result))
	})
}

func TestConsolidateNotificationThread_Dedup(t *testing.T) {
	t.Run("context_cleared deduped to last occurrence", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []string{"settings_changed", "context_cleared"}, types(t, result))
	})

	t.Run("settings_changed deduped to last occurrence", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("B", "C")),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []string{"context_cleared", "settings_changed"}, types(t, result))
		// Verify merged: old=A, new=C
		changes := parseRaw(t, result[1])["changes"].(map[string]interface{})
		model := changes["model"].(map[string]interface{})
		assert.Equal(t, "A", model["old"])
		assert.Equal(t, "C", model["new"])
	})
}

func TestConsolidateNotificationThread_ChangesCancelOut(t *testing.T) {
	t.Run("settings_changed cancels out when A->B then B->A", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
			raw(t, settingsChanged("B", "A")),
		}
		result := consolidateNotificationThread(msgs)
		// settings_changed drops because old==new; only context_cleared remains.
		assert.Equal(t, []string{"context_cleared"}, types(t, result))
	})

	t.Run("settings_changed alone cancels out", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, settingsChanged("A", "B")),
			raw(t, settingsChanged("B", "A")),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []json.RawMessage{}, result)
	})
}

func TestConsolidateNotificationThread_SettingsMerged(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, settingsChanged("A", "B")),
		raw(t, settingsChanged("B", "C")),
	}
	result := consolidateNotificationThread(msgs)
	require.Len(t, result, 1)
	assert.Equal(t, "settings_changed", msgType(t, result[0]))
	changes := parseRaw(t, result[0])["changes"].(map[string]interface{})
	model := changes["model"].(map[string]interface{})
	assert.Equal(t, "A", model["old"])
	assert.Equal(t, "C", model["new"])
}

func TestConsolidateNotificationThread_PlanExecution(t *testing.T) {
	t.Run("plan_execution with context_cleared both kept", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{"type": "plan_execution", "plan_file_path": "/p.md"}),
			raw(t, map[string]interface{}{"type": "context_cleared"}),
		}
		result := consolidateNotificationThread(msgs)
		assert.Equal(t, []string{"plan_execution", "context_cleared"}, types(t, result))
	})

	t.Run("plan_execution strips context_cleared field", func(t *testing.T) {
		msgs := []json.RawMessage{
			raw(t, map[string]interface{}{
				"type":            "plan_execution",
				"context_cleared": true,
				"plan_file_path":  "/p.md",
			}),
		}
		result := consolidateNotificationThread(msgs)
		require.Len(t, result, 1)
		m := parseRaw(t, result[0])
		assert.Equal(t, "plan_execution", m["type"])
		assert.Nil(t, m["context_cleared"], "context_cleared field should be stripped")
		assert.Equal(t, "/p.md", m["plan_file_path"])
	})
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
	result := consolidateNotificationThread(msgs)
	require.Len(t, result, 1)
	m := parseRaw(t, result[0])
	assert.Nil(t, m["contextCleared"], "contextCleared should not appear in output")
}

func TestConsolidateNotificationThread_CompactionBoundariesKept(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, map[string]interface{}{"type": "system", "subtype": "compact_boundary"}),
		raw(t, map[string]interface{}{"type": "system", "subtype": "microcompact_boundary"}),
	}
	result := consolidateNotificationThread(msgs)
	assert.Len(t, result, 2)
}

func TestConsolidateNotificationThread_Interrupted(t *testing.T) {
	msgs := []json.RawMessage{
		raw(t, settingsChanged("A", "B")),
		raw(t, map[string]interface{}{"type": "interrupted"}),
	}
	result := consolidateNotificationThread(msgs)
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
	result := consolidateNotificationThread(msgs)
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
	result := consolidateNotificationThread(msgs)
	require.Len(t, result, 1)
	m := parseRaw(t, result[0])
	assert.Equal(t, "idle", m["status"])
}

func TestConsolidateNotificationThread_Empty(t *testing.T) {
	result := consolidateNotificationThread(nil)
	assert.Equal(t, []json.RawMessage{}, result)
}
