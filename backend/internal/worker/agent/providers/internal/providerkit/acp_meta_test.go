package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func encodedMeta(t *testing.T, value map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	require.NoError(t, err)
	return string(encoded)
}

func TestWithACPMeta_SetsAKeyAndKeepsTheRest(t *testing.T) {
	result := map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject-once"},
		"_meta":   map[string]any{"other": 1},
	}
	got := WithACPMeta(result, "Use pnpm", "followup_message")
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject-once"},"_meta":{"other":1,"followup_message":"Use pnpm"}}`, encodedMeta(t, got))
}

func TestWithACPMeta_SetsANestedKeyAndKeepsEachLevel(t *testing.T) {
	result := map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject"},
		"_meta":   map[string]any{"other": 1, "kiro": map[string]any{"editedCommand": "ls"}},
	}
	got := WithACPMeta(result, "Use rg", "kiro", "rejectionReason")
	assert.JSONEq(t, `{"outcome":{"outcome":"selected","optionId":"reject"},"_meta":{"other":1,"kiro":{"editedCommand":"ls","rejectionReason":"Use rg"}}}`, encodedMeta(t, got))
}

func TestWithACPMeta_AddsTheObjectsThatAResultLacks(t *testing.T) {
	got := WithACPMeta(map[string]any{"outcome": map[string]any{"outcome": "cancelled"}}, "why", "kiro", "rejectionReason")
	assert.JSONEq(t, `{"outcome":{"outcome":"cancelled"},"_meta":{"kiro":{"rejectionReason":"why"}}}`, encodedMeta(t, got))

	got = WithACPMeta(nil, "why", "followup_message")
	assert.JSONEq(t, `{"_meta":{"followup_message":"why"}}`, encodedMeta(t, got), "a nil result gains the whole path")
}

func TestWithACPMeta_ReplacesALevelThatIsNoObjectAndAnEarlierValue(t *testing.T) {
	got := WithACPMeta(map[string]any{"_meta": "x"}, "why", "followup_message")
	assert.JSONEq(t, `{"_meta":{"followup_message":"why"}}`, encodedMeta(t, got))

	got = WithACPMeta(map[string]any{"_meta": map[string]any{"other": 1, "kiro": "x"}}, "why", "kiro", "rejectionReason")
	assert.JSONEq(t, `{"_meta":{"other":1,"kiro":{"rejectionReason":"why"}}}`, encodedMeta(t, got))

	got = WithACPMeta(map[string]any{"_meta": map[string]any{"followup_message": "old"}}, "new", "followup_message")
	assert.JSONEq(t, `{"_meta":{"followup_message":"new"}}`, encodedMeta(t, got))
}

// The caller may still hold the result, so neither it nor a map inside it
// changes.
func TestWithACPMeta_LeavesTheResultItReceivedUnchanged(t *testing.T) {
	result := map[string]any{
		"outcome": map[string]any{"outcome": "selected", "optionId": "reject"},
		"_meta":   map[string]any{"kiro": map[string]any{"editedCommand": "ls"}},
	}
	before := encodedMeta(t, result)
	_ = WithACPMeta(result, "why", "kiro", "rejectionReason")
	_ = WithACPMeta(result, "why", "followup_message")
	assert.JSONEq(t, before, encodedMeta(t, result))
}
