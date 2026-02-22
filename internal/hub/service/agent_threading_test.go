package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractToolUseID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts first tool_use ID",
			content: `{"message":{"content":[{"type":"tool_use","id":"toolu_abc123","name":"Bash","input":{}}]}}`,
			want:    "toolu_abc123",
		},
		{
			name:    "skips non-tool_use blocks",
			content: `{"message":{"content":[{"type":"text","text":"hello"},{"type":"tool_use","id":"toolu_xyz","name":"Read","input":{}}]}}`,
			want:    "toolu_xyz",
		},
		{
			name:    "returns empty for no tool_use",
			content: `{"message":{"content":[{"type":"text","text":"hello"}]}}`,
			want:    "",
		},
		{
			name:    "returns empty for empty content array",
			content: `{"message":{"content":[]}}`,
			want:    "",
		},
		{
			name:    "returns empty for invalid JSON",
			content: `not json`,
			want:    "",
		},
		{
			name:    "returns empty for tool_use with empty ID",
			content: `{"message":{"content":[{"type":"tool_use","id":"","name":"Bash"}]}}`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolUseID([]byte(tt.content))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractToolResultID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts first tool_result tool_use_id",
			content: `{"message":{"content":[{"type":"tool_result","tool_use_id":"toolu_abc123","content":"ok"}]}}`,
			want:    "toolu_abc123",
		},
		{
			name:    "skips non-tool_result blocks",
			content: `{"message":{"content":[{"type":"text","text":"hi"},{"type":"tool_result","tool_use_id":"toolu_xyz"}]}}`,
			want:    "toolu_xyz",
		},
		{
			name:    "returns empty for no tool_result",
			content: `{"message":{"content":[{"type":"text","text":"hello"}]}}`,
			want:    "",
		},
		{
			name:    "returns empty for invalid JSON",
			content: `{bad`,
			want:    "",
		},
		{
			name:    "returns empty for tool_result with empty tool_use_id",
			content: `{"message":{"content":[{"type":"tool_result","tool_use_id":""}]}}`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractToolResultID([]byte(tt.content))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractParentToolUseID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts parent_tool_use_id",
			content: `{"type":"user","parent_tool_use_id":"toolu_ctrl_1","message":{}}`,
			want:    "toolu_ctrl_1",
		},
		{
			name:    "returns empty when absent",
			content: `{"type":"user","message":{}}`,
			want:    "",
		},
		{
			name:    "returns empty for invalid JSON",
			content: `{bad`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractParentToolUseID([]byte(tt.content))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExtractSystemToolUseID(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name:    "extracts tool_use_id from task_started",
			content: `{"type":"system","subtype":"task_started","tool_use_id":"toolu_abc"}`,
			want:    "toolu_abc",
		},
		{
			name:    "extracts tool_use_id from any subtype",
			content: `{"type":"system","subtype":"other","tool_use_id":"toolu_xyz"}`,
			want:    "toolu_xyz",
		},
		{
			name:    "returns empty when tool_use_id absent",
			content: `{"type":"system","subtype":"task_started"}`,
			want:    "",
		},
		{
			name:    "returns empty for empty tool_use_id",
			content: `{"type":"system","subtype":"task_started","tool_use_id":""}`,
			want:    "",
		},
		{
			name:    "returns empty for invalid JSON",
			content: `{bad`,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractSystemToolUseID([]byte(tt.content))
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestWrapUnwrapContent(t *testing.T) {
	original := []byte(`{"type":"assistant","message":{"content":[{"type":"text","text":"Hello"}]}}`)

	wrapped := wrapContent(original)

	// Verify wrapped is valid JSON with expected structure.
	var w threadWrapper
	require.NoError(t, json.Unmarshal(wrapped, &w))
	assert.Empty(t, w.OldSeqs)
	require.Len(t, w.Messages, 1)

	// Round-trip: unwrap should return the same content.
	unwrapped, err := unwrapContent(wrapped)
	require.NoError(t, err)
	assert.Empty(t, unwrapped.OldSeqs)
	require.Len(t, unwrapped.Messages, 1)
	assert.JSONEq(t, string(original), string(unwrapped.Messages[0]))
}

func TestWrapUnwrapContent_InvalidJSON(t *testing.T) {
	_, err := unwrapContent([]byte(`not-json`))
	assert.Error(t, err)
}

func TestAppendToThread(t *testing.T) {
	parent := []byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1"}]}}`)
	child := []byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1"}]}}`)

	wrapped := wrapContent(parent)
	wrapper, err := unwrapContent(wrapped)
	require.NoError(t, err)

	parentSeq := int64(5)
	merged := appendToThread(wrapper, parentSeq, child)

	// Parse the merged result.
	var result threadWrapper
	require.NoError(t, json.Unmarshal(merged, &result))

	// old_seqs should contain the parent's seq.
	assert.Equal(t, []int64{5}, result.OldSeqs)

	// Messages should have both parent and child.
	require.Len(t, result.Messages, 2)
	assert.JSONEq(t, string(parent), string(result.Messages[0]))
	assert.JSONEq(t, string(child), string(result.Messages[1]))
}

func TestAppendToThread_MultipleAppends(t *testing.T) {
	msg1 := []byte(`{"type":"assistant"}`)
	msg2 := []byte(`{"type":"user"}`)
	msg3 := []byte(`{"type":"user"}`)

	wrapped := wrapContent(msg1)
	wrapper, err := unwrapContent(wrapped)
	require.NoError(t, err)

	merged1 := appendToThread(wrapper, 1, msg2)
	wrapper2, err := unwrapContent(merged1)
	require.NoError(t, err)

	merged2 := appendToThread(wrapper2, 2, msg3)
	var result threadWrapper
	require.NoError(t, json.Unmarshal(merged2, &result))

	assert.Equal(t, []int64{1, 2}, result.OldSeqs)
	require.Len(t, result.Messages, 3)
}

func TestConsolidateNotificationThread_SettingsChangedMerge(t *testing.T) {
	// Two settings_changed messages: model A→B, then B→C.
	// Should merge into one with model A→C.
	msg1, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "A", "new": "B"},
		},
	})
	msg2, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "B", "new": "C"},
		},
	})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	require.Len(t, result, 1)

	var entry struct {
		Type    string `json:"type"`
		Changes map[string]struct {
			Old string `json:"old"`
			New string `json:"new"`
		} `json:"changes"`
	}
	require.NoError(t, json.Unmarshal(result[0], &entry))
	assert.Equal(t, "settings_changed", entry.Type)
	assert.Equal(t, "A", entry.Changes["model"].Old)
	assert.Equal(t, "C", entry.Changes["model"].New)
}

func TestConsolidateNotificationThread_SettingsChangedNoOp(t *testing.T) {
	// Settings changed from A→B, then B→A. Net effect is no-op.
	// Should produce a context_cleared fallback (no effective changes).
	msg1, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "A", "new": "B"},
		},
	})
	msg2, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "B", "new": "A"},
		},
	})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	// No effective changes → fallback to context_cleared.
	require.Len(t, result, 1)
	var entry struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(result[0], &entry))
	assert.Equal(t, "context_cleared", entry.Type)
}

func TestConsolidateNotificationThread_ContextClearedDedup(t *testing.T) {
	msg1, _ := json.Marshal(map[string]interface{}{"type": "context_cleared"})
	msg2, _ := json.Marshal(map[string]interface{}{"type": "context_cleared"})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	// Deduped to one context_cleared.
	count := 0
	for _, r := range result {
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(r, &e) == nil && e.Type == "context_cleared" {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected exactly one context_cleared")
}

func TestConsolidateNotificationThread_InterruptedDedup(t *testing.T) {
	msg1, _ := json.Marshal(map[string]interface{}{"type": "interrupted"})
	msg2, _ := json.Marshal(map[string]interface{}{"type": "interrupted"})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	count := 0
	for _, r := range result {
		var e struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(r, &e) == nil && e.Type == "interrupted" {
			count++
		}
	}
	assert.Equal(t, 1, count, "expected exactly one interrupted")
}

func TestConsolidateNotificationThread_StatusKeepsLatest(t *testing.T) {
	msg1, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "status", "status": "compacting",
	})
	msg2, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "status", "status": nil,
	})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	// Should keep only the latest status.
	statusCount := 0
	for _, r := range result {
		var e struct {
			Type    string `json:"type"`
			Subtype string `json:"subtype"`
		}
		if json.Unmarshal(r, &e) == nil && e.Type == "system" && e.Subtype == "status" {
			statusCount++
		}
	}
	assert.Equal(t, 1, statusCount, "expected exactly one status message")
	// The kept one should be the latest (msg2).
	assert.JSONEq(t, string(msg2), string(result[0]))
}

func TestConsolidateNotificationThread_CompactBoundaryKeepsAll(t *testing.T) {
	msg1, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]interface{}{},
	})
	msg2, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "compact_boundary", "compact_metadata": map[string]interface{}{},
	})

	result := consolidateNotificationThread([]json.RawMessage{msg1, msg2})
	assert.Len(t, result, 2, "expected both compact_boundary messages kept")
}

func TestConsolidateNotificationThread_MixedMessages(t *testing.T) {
	settings, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "A", "new": "B"},
		},
	})
	interrupted, _ := json.Marshal(map[string]interface{}{"type": "interrupted"})
	status, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "status", "status": "compacting",
	})
	boundary, _ := json.Marshal(map[string]interface{}{
		"type": "system", "subtype": "compact_boundary",
	})

	result := consolidateNotificationThread([]json.RawMessage{settings, interrupted, status, boundary})
	// Should have: settings_changed, interrupted, status, compact_boundary.
	assert.Len(t, result, 4)
}

func TestConsolidateNotificationThread_SettingsChangedWithContextCleared(t *testing.T) {
	msg, _ := json.Marshal(map[string]interface{}{
		"type": "settings_changed",
		"changes": map[string]interface{}{
			"model": map[string]string{"old": "A", "new": "B"},
		},
		"contextCleared": true,
	})

	result := consolidateNotificationThread([]json.RawMessage{msg})
	require.Len(t, result, 1)

	var entry struct {
		Type           string `json:"type"`
		ContextCleared bool   `json:"contextCleared"`
	}
	require.NoError(t, json.Unmarshal(result[0], &entry))
	assert.Equal(t, "settings_changed", entry.Type)
	assert.True(t, entry.ContextCleared)
}
