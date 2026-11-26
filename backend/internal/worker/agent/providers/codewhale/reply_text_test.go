package codewhale

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestReplyTextRejectsNullIndexNonNumericIndexAndWrongCaseKeys(t *testing.T) {
	t.Parallel()
	provider := codewhaleProvider{}
	for name, raw := range map[string]string{
		"child with null index":           `{"kind":"message","index":null,"message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child with string index":         `{"kind":"message","index":"0","message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child without index":             `{"kind":"message","message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child with boolean index":        `{"kind":"message","index":true,"message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child with null content":         `{"kind":"message","index":0,"message":{"role":"assistant","content":null}}`,
		"child with null block":           `{"kind":"message","index":0,"message":{"role":"assistant","content":[null]}}`,
		"child with wrong-case kind":      `{"KIND":"message","index":0,"message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child with wrong-case index":     `{"kind":"message","INDEX":0,"message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"item with wrong-case event":      `{"EVENT":"item.completed","payload":{"item":{"kind":"agent_message","detail":"Hi."}}}`,
		"item with wrong-case detail key": `{"event":"item.completed","payload":{"item":{"kind":"agent_message","DETAIL":"Hi."}}}`,
		"item with null payload":          `{"event":"item.completed","payload":null}`,
		"item with null item":             `{"event":"item.completed","payload":{"item":null}}`,
		"non-final item event":            `{"event":"item.delta","payload":{"item":{"kind":"agent_message","detail":"Hi."}}}`,
		"non-final event with message":    `{"event":"item.delta","kind":"message","index":0,"message":{"role":"assistant","content":[{"type":"text","text":"Hi."}]}}`,
		"child with null message":         `{"kind":"message","index":0,"message":null}`,
		"non-object JSON":                 `null`,
	} {
		text, ok := provider.ReplyText(agent.MessageContent{Original: []byte(raw)})
		if name == "item with wrong-case detail key" {
			// Matches frontend: agent_message without lowercase "detail" returns empty string reply.
			assert.True(t, ok, name)
			assert.Empty(t, text, name)
			continue
		}
		assert.False(t, ok, name)
		assert.Empty(t, text, name)
	}

	// Numeric index 0 and negative/fractional finite numbers are valid JSON numbers (matching pickNumber).
	for _, tc := range []struct {
		raw      string
		wantText string
	}{
		{`{"kind":"message","index":0,"message":{"role":"assistant","content":[{"type":"text","text":"Zero."}]}}`, "Zero."},
		{`{"kind":"message","index":-1.5,"message":{"role":"assistant","content":[{"type":"text","text":"Fraction."}]}}`, "Fraction."},
		{`{"kind":"message","index":0,"message":{"role":"assistant","content":[{"type":"text","text":123}]}}`, ""},
	} {
		text, ok := provider.ReplyText(agent.MessageContent{Original: []byte(tc.raw)})
		assert.True(t, ok, tc.raw)
		assert.Equal(t, tc.wantText, text, tc.raw)
	}

	// All four final item events carry an agent_message reply; non-string or absent detail yields empty string.
	for _, event := range []string{"item.completed", "item.failed", "item.interrupted", "item.canceled"} {
		reply, ok := provider.ReplyText(agent.MessageContent{
			Original: []byte(`{"event":"` + event + `","payload":{"item":{"kind":"agent_message","detail":"Done."}}}`),
		})
		assert.True(t, ok, event)
		assert.Equal(t, "Done.", reply, event)
	}
	emptyDetail, ok := provider.ReplyText(agent.MessageContent{
		Original: []byte(`{"event":"item.completed","payload":{"item":{"kind":"agent_message","detail":null}}}`),
	})
	assert.True(t, ok)
	assert.Empty(t, emptyDetail)

	// Child messages with 0 blocks, 2 blocks, non-assistant role, non-text block, or malformed JSON are rejected.
	for name, raw := range map[string]string{
		"empty blocks":    `{"kind":"message","index":0,"message":{"role":"assistant","content":[]}}`,
		"multiple blocks": `{"kind":"message","index":0,"message":{"role":"assistant","content":[{"type":"text","text":"A"},{"type":"text","text":"B"}]}}`,
		"user role":       `{"kind":"message","index":0,"message":{"role":"user","content":[{"type":"text","text":"Hi"}]}}`,
		"thinking block":  `{"kind":"message","index":0,"message":{"role":"assistant","content":[{"type":"thinking","thinking":"Hmm"}]}}`,
		"malformed JSON":  `{not json`,
	} {
		reply, ok := provider.ReplyText(agent.MessageContent{Original: []byte(raw)})
		assert.False(t, ok, name)
		assert.Empty(t, reply, name)
	}
}
