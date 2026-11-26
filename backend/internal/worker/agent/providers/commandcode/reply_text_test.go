package commandcode

import (
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestCommandCodeReplyTextRequiresExactKeyCase(t *testing.T) {
	t.Parallel()
	provider := commandcodeProvider{}
	for _, tc := range []struct {
		name     string
		raw      string
		wantText string
		wantOK   bool
	}{
		{
			name:     "exact keys return joined text",
			raw:      `{"type":"event","event":{"type":"message_end","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}}`,
			wantText: "ab",
			wantOK:   true,
		},
		{
			name:   "uppercase frame type is rejected",
			raw:    `{"TYPE":"event","event":{"type":"message_end","content":[{"type":"text","text":"a"}]}}`,
			wantOK: false,
		},
		{
			name:   "uppercase event key is rejected",
			raw:    `{"type":"event","EVENT":{"type":"message_end","content":[{"type":"text","text":"a"}]}}`,
			wantOK: false,
		},
		{
			name:   "uppercase event type is rejected",
			raw:    `{"type":"event","event":{"TYPE":"message_end","content":[{"type":"text","text":"a"}]}}`,
			wantOK: false,
		},
		{
			name:     "uppercase content key yields empty text",
			raw:      `{"type":"event","event":{"type":"message_end","CONTENT":[{"type":"text","text":"a"}]}}`,
			wantText: "",
			wantOK:   true,
		},
		{
			name:     "uppercase block keys yield empty text",
			raw:      `{"type":"event","event":{"type":"message_end","content":[{"TYPE":"text","text":"a"},{"type":"text","TEXT":"b"}]}}`,
			wantText: "",
			wantOK:   true,
		},
		{
			name:     "non-string text yields empty text",
			raw:      `{"type":"event","event":{"type":"message_end","content":[{"type":"text","text":42}]}}`,
			wantText: "",
			wantOK:   true,
		},
		{
			name:     "non-array content yields empty text",
			raw:      `{"type":"event","event":{"type":"message_end","content":"plain"}}`,
			wantText: "",
			wantOK:   true,
		},
		{
			name:     "null content yields empty text",
			raw:      `{"type":"event","event":{"type":"message_end","content":null}}`,
			wantText: "",
			wantOK:   true,
		},
		{
			name:   "null event is rejected",
			raw:    `{"type":"event","event":null}`,
			wantOK: false,
		},
		{
			name:   "null frame is rejected",
			raw:    `null`,
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			text, ok := provider.ReplyText(agent.MessageContent{Original: []byte(tc.raw)})
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantText, text)
		})
	}
}
