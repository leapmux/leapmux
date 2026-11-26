package qoder

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestQoderReplyTextMatchesKeysCaseSensitively(t *testing.T) {
	t.Parallel()
	p := qoderProvider{}
	for _, tc := range []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{
			name: "exact lowercase keys",
			raw:  `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
			want: "hello",
			ok:   true,
		},
		{
			name: "uppercase type key is rejected",
			raw:  `{"TYPE":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name: "mixed-case message key is rejected",
			raw:  `{"type":"assistant","Message":{"content":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name: "mixed-case content key is rejected",
			raw:  `{"type":"assistant","message":{"Content":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name: "uppercase block type key is rejected",
			raw:  `{"type":"assistant","message":{"content":[{"TYPE":"text","text":"hello"}]}}`,
		},
		{
			name: "uppercase block text key reads as empty string",
			raw:  `{"type":"assistant","message":{"content":[{"type":"text","TEXT":"hello"}]}}`,
			want: "",
			ok:   true,
		},
		{
			name: "multiple text blocks join without separator",
			raw:  `{"type":"assistant","message":{"content":[{"type":"text","text":"hel"},{"type":"text","text":"lo"}]}}`,
			want: "hello",
			ok:   true,
		},
		{
			name: "tool_use alongside text is not a reply",
			raw:  `{"type":"assistant","message":{"content":[{"type":"text","text":"hi"},{"type":"tool_use","id":"1","name":"Bash"}]}}`,
		},
		{
			name: "non-object message is rejected",
			raw:  `{"type":"assistant","message":"bad"}`,
		},
		{
			name: "non-array content is rejected",
			raw:  `{"type":"assistant","message":{"content":"bad"}}`,
		},
		{
			name: "null payload is rejected",
			raw:  `null`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := p.ReplyText(agent.MessageContent{Original: []byte(tc.raw)})
			assert.Equal(t, tc.ok, ok)
			assert.Equal(t, tc.want, got)
		})
	}
}
