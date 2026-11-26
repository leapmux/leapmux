package deepseekharness

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestReplyTextRejectsNullBlockIndexAndCaseMismatchedKeys(t *testing.T) {
	t.Parallel()
	provider := deepseekHarnessProvider{}
	cases := []struct {
		name string
		raw  string
	}{
		{
			name: "null blockIndex",
			raw:  `{"type":"assistant/message","blockIndex":null,"data":{"message":{"content":[{"type":"text","text":"Should not match"}]}}}`,
		},
		{
			name: "case-mismatched block Type",
			raw:  `{"type":"assistant/message","blockIndex":0,"data":{"message":{"content":[{"Type":"text","text":"Wrong key case"}]}}}`,
		},
		{
			name: "case-mismatched block Text",
			raw:  `{"type":"assistant/message","blockIndex":0,"data":{"message":{"content":[{"type":"text","Text":"Wrong text case"}]}}}`,
		},
		{
			name: "non-assistant row",
			raw:  `{"type":"tool/call","blockIndex":0,"data":{"message":{"content":[{"type":"text","text":"Not assistant"}]}}}`,
		},
		{
			name: "fractional blockIndex",
			raw:  `{"type":"assistant/message","blockIndex":0.5,"data":{"message":{"content":[{"type":"text","text":"Fraction"}]}}}`,
		},
		{
			name: "negative blockIndex",
			raw:  `{"type":"assistant/message","blockIndex":-1,"data":{"message":{"content":[{"type":"text","text":"Negative"}]}}}`,
		},
		{
			name: "string blockIndex",
			raw:  `{"type":"assistant/message","blockIndex":"0","data":{"message":{"content":[{"type":"text","text":"String index"}]}}}`,
		},
		{
			name: "unsafe integer blockIndex",
			raw:  `{"type":"assistant/message","blockIndex":9007199254740993,"data":{"message":{"content":[{"type":"text","text":"Overflow"}]}}}`,
		},
		{
			name: "non-object content block",
			raw:  `{"type":"assistant/message","blockIndex":0,"data":{"message":{"content":[null]}}}`,
		},
		{
			name: "non-array content",
			raw:  `{"type":"assistant/message","blockIndex":0,"data":{"message":{"content":{}}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, ok := provider.ReplyText(agent.MessageContent{Original: []byte(tc.raw)})
			if tc.name == "case-mismatched block Text" {
				assert.True(t, ok)
				assert.Empty(t, text, "case-mismatched Text field must not match lowercase text")
				return
			}
			assert.False(t, ok)
			assert.Empty(t, text)
		})
	}
	t.Run("escaped slash in assistant/message type", func(t *testing.T) {
		t.Parallel()
		raw := `{"type":"assistant\/message","blockIndex":0,"data":{"message":{"content":[{"type":"text","text":"Escaped slash"}]}}}`
		text, ok := provider.ReplyText(agent.MessageContent{Original: []byte(raw)})
		assert.True(t, ok)
		assert.Equal(t, "Escaped slash", text)
	})
}
