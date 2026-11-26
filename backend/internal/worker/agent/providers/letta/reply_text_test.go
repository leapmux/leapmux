package letta

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

// ReplyText matches JSON keys case-sensitively so wrong-case fields never
// masquerade as Letta assistant text.
func TestReplyTextMatchesKeysCaseSensitively(t *testing.T) {
	t.Parallel()
	p := lettaProvider{}

	for name, raw := range map[string]string{
		"capitalized type":         `{"Type":"assembled_message","text":"hello"}`,
		"capitalized message_type": `{"Message_Type":"assistant_message","text":"hello"}`,
		"turn_finished row":        `{"type":"turn_finished","message_type":"assistant_message","text":"hello"}`,
		"json null":                `null`,
		"json array":               `["assistant_message"]`,
		"malformed json":           `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text, ok := p.ReplyText(agent.MessageContent{Original: []byte(raw)})
			assert.False(t, ok, raw)
			assert.Empty(t, text, raw)
		})
	}

	// Wrong-case "Text" on an otherwise valid frame reads as empty text.
	text, ok := p.ReplyText(agent.MessageContent{Original: []byte(`{"message_type":"assistant_message","Text":"hello"}`)})
	assert.True(t, ok)
	assert.Empty(t, text)

	// Exact-case keys return the text.
	text, ok = p.ReplyText(agent.MessageContent{Original: []byte(`{"message_type":"assistant_message","text":"hello"}`)})
	assert.True(t, ok)
	assert.Equal(t, "hello", text)

	// Non-string text reads as empty.
	text, ok = p.ReplyText(agent.MessageContent{Original: []byte(`{"type":"assembled_message","text":42}`)})
	assert.True(t, ok)
	assert.Empty(t, text)
}
