package kimi

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestKimiReplyTextFastPathAndValidation(t *testing.T) {
	t.Parallel()
	p := kimiProvider{}

	for name, raw := range map[string]string{
		"non-JSON":             `not json`,
		"turn record":          `{"type":"leapmux.kimi.turn","session_id":"s","agent_id":"main","turn_id":"t0","turn":{"turn_id":"t0"}}`,
		"notice record":        `{"type":"leapmux.kimi.notice","session_id":"s","agent_id":"main","notice":{"level":"info","title":"hi"}}`,
		"tool call item":       `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"result","item":{"type":"tool_call","item_id":"c1","status":"ended","outcome":"completed","output":"huge"}}`,
		"reasoning item":       `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"item","item":{"type":"reasoning","item_id":"r1","status":"ended","outcome":"completed","text":"thinking"}}`,
		"null item":            `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"item","item":null}`,
		"scalar item":          `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"item","item":"bad"}`,
		"invalid assistant":    `{"type":"leapmux.kimi.item","session_id":"","agent_id":"main","turn_id":"t0","role":"item","item":{"type":"assistant_message","item_id":"a1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"missing session"}}`,
		"blocked hook message": `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"item","turn_context":{"turn_id":"t0","kind":"agent","status":"ended","outcome":"failed","end_reason":"blocked"},"item":{"type":"assistant_message","item_id":"a1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"<hook_result hook_event=\"UserPromptSubmit\">\nblocked\n</hook_result>"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			text, ok := p.ReplyText(agent.MessageContent{Original: []byte(raw)})
			assert.False(t, ok)
			assert.Empty(t, text)
		})
	}

	valid := `{"type":"leapmux.kimi.item","session_id":"s","agent_id":"main","turn_id":"t0","role":"item","item":{"type":"assistant_message","item_id":"a1","status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Hello from Kimi."}}`
	text, ok := p.ReplyText(agent.MessageContent{Original: []byte(valid)})
	assert.True(t, ok)
	assert.Equal(t, "Hello from Kimi.", text)
}
