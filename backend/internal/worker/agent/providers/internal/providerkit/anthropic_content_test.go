package providerkit

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReadAnthropicContent(t *testing.T) {
	t.Parallel()

	t.Run("empty and nil blocks", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, AnthropicContent{}, ReadAnthropicContent(nil))
		assert.Equal(t, AnthropicContent{}, ReadAnthropicContent([]json.RawMessage{}))
	})

	t.Run("text blocks preserve order and coerce non-string text to empty", func(t *testing.T) {
		t.Parallel()
		got := ReadAnthropicContent([]json.RawMessage{
			json.RawMessage(`{"type":"text","text":"first"}`),
			json.RawMessage(`{"type":"text","text":42}`),
			json.RawMessage(`{"type":"text","text":"second"}`),
			json.RawMessage(`{"type":"thinking","thinking":"ignored"}`),
		})
		assert.True(t, got.HasText)
		assert.False(t, got.HasToolUse)
		assert.Equal(t, []string{"first", "", "second"}, got.Texts)
	})

	t.Run("tool use and malformed blocks", func(t *testing.T) {
		t.Parallel()
		got := ReadAnthropicContent([]json.RawMessage{
			json.RawMessage(`"not-an-object"`),
			json.RawMessage(`null`),
			json.RawMessage(`{"type":"text","text":"before tool"}`),
			json.RawMessage(`{"type":"tool_use","id":"tu-1","name":"Bash"}`),
		})
		assert.True(t, got.HasText)
		assert.True(t, got.HasToolUse)
		assert.Equal(t, []string{"before tool"}, got.Texts)
	})

	t.Run("rejects case-mismatched keys", func(t *testing.T) {
		t.Parallel()
		got := ReadAnthropicContent([]json.RawMessage{
			json.RawMessage(`{"TYPE":"text","text":"upper type"}`),
			json.RawMessage(`{"type":"text","TEXT":"upper text"}`),
			json.RawMessage(`{"TYPE":"tool_use"}`),
		})
		assert.True(t, got.HasText)
		assert.False(t, got.HasToolUse)
		assert.Equal(t, []string{""}, got.Texts)
	})
}
