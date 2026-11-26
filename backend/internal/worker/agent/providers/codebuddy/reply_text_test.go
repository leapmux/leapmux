package codebuddy

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestCodebuddyReplyTextMatchesJSONKeysCaseSensitively(t *testing.T) {
	t.Parallel()

	p := codebuddyProvider{}
	for _, tc := range []struct {
		name     string
		original string
		wantText string
		wantOK   bool
	}{
		{
			name:     "live assistant frame with exact lowercase keys",
			original: `{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
			wantText: "hello",
			wantOK:   true,
		},
		{
			name:     "live assistant frame with uppercase TYPE",
			original: `{"TYPE":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name:     "live assistant frame with uppercase MESSAGE",
			original: `{"type":"assistant","MESSAGE":{"content":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name:     "live assistant frame with uppercase CONTENT",
			original: `{"type":"assistant","message":{"CONTENT":[{"type":"text","text":"hello"}]}}`,
		},
		{
			name:     "live assistant frame with uppercase block keys",
			original: `{"type":"assistant","message":{"content":[{"TYPE":"text","TEXT":"hello"}]}}`,
		},
		{
			name:     "stored message record with exact lowercase keys",
			original: `{"type":"message","role":"assistant","content":[{"type":"output_text","text":"first"},{"type":"output_text","text":"second"}]}`,
			wantText: "firstsecond",
			wantOK:   true,
		},
		{
			name:     "stored message record with uppercase TYPE",
			original: `{"TYPE":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
		},
		{
			name:     "stored message record with uppercase ROLE",
			original: `{"type":"message","ROLE":"assistant","content":[{"type":"output_text","text":"hello"}]}`,
		},
		{
			name:     "stored message record with uppercase CONTENT",
			original: `{"type":"message","role":"assistant","CONTENT":[{"type":"output_text","text":"hello"}]}`,
		},
		{
			name:     "stored message record with uppercase block TYPE",
			original: `{"type":"message","role":"assistant","content":[{"TYPE":"output_text","text":"hello"}]}`,
		},
		{
			name:     "stored message record with uppercase block TEXT",
			original: `{"type":"message","role":"assistant","content":[{"type":"output_text","TEXT":"hello"}]}`,
		},
		{
			name:     "stored message record with non-string text",
			original: `{"type":"message","role":"assistant","content":[{"type":"output_text","text":123}]}`,
		},
		{
			name:     "stored message record with empty text only",
			original: `{"type":"message","role":"assistant","content":[{"type":"output_text","text":""}]}`,
		},
		{
			name:     "stored message record with non-assistant role",
			original: `{"type":"message","role":"user","content":[{"type":"output_text","text":"hello"}]}`,
		},
		{
			name:     "live assistant frame with non-object message",
			original: `{"type":"assistant","message":"bad"}`,
		},
		{
			name:     "live assistant frame with non-array content",
			original: `{"type":"assistant","message":{"content":"bad"}}`,
		},
		{
			name:     "stored message record with non-array content",
			original: `{"type":"message","role":"assistant","content":"bad"}`,
		},
		{
			name:     "stored message record skips non-object and non-output_text blocks",
			original: `{"type":"message","role":"assistant","content":[null,42,{"type":"input_text","text":"skip"},{"type":"output_text","text":""},{"type":"output_text","text":"kept"}]}`,
			wantText: "kept",
			wantOK:   true,
		},
		{
			name:     "non-object payload",
			original: `["assistant"]`,
		},
		{
			name:     "empty payload",
			original: ``,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotText, gotOK := p.ReplyText(agent.MessageContent{Original: []byte(tc.original)})
			assert.Equal(t, tc.wantOK, gotOK)
			assert.Equal(t, tc.wantText, gotText)
		})
	}
}
