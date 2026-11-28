package cline

import (
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
)

func TestReplyTextConformance(t *testing.T) {
	agenttest.RunReplyTextConformance(t, Registration())
}

func TestClineReplyTextEdgeCases(t *testing.T) {
	t.Parallel()
	provider := clineProvider{}
	for _, tc := range []struct {
		name     string
		original string
		wantText string
		wantOK   bool
	}{
		{
			name:     "assistant.media prefers mediaType over mimeType and type",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":{"mediaType":"image/png","mimeType":"image/jpeg","type":"image"}}}`,
			wantText: "The model returned media (image/png).",
			wantOK:   true,
		},
		{
			name:     "assistant.media falls back to mimeType when mediaType absent",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":{"mimeType":"audio/wav","type":"audio"}}}`,
			wantText: "The model returned media (audio/wav).",
			wantOK:   true,
		},
		{
			name:     "assistant.media falls back to type when mediaType and mimeType absent",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":{"type":"video"}}}`,
			wantText: "The model returned media (video).",
			wantOK:   true,
		},
		{
			name:     "assistant.media empty media object uses generic message",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":{}}}`,
			wantText: "The model returned media.",
			wantOK:   true,
		},
		{
			name:     "assistant.media non-object media returns false",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":"image/png"}}`,
		},
		{
			name:     "assistant.media array media returns false",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":[]}}`,
		},
		{
			name:     "assistant.media null media returns false",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{"media":null}}`,
		},
		{
			name:     "assistant.media absent media returns false",
			original: `{"event":"` + contracts.ClineEventAssistantMedia + `","payload":{}}`,
		},
		{
			name:     "non-object payload returns false",
			original: `{"event":"` + contracts.ClineEventAssistantFinished + `","payload":"not-an-object"}`,
		},
		{
			name:     "non-object original array returns false",
			original: `["not-an-object"]`,
		},
		{
			name:     "non-object original string returns false",
			original: `"not-an-object"`,
		},
		{
			name:     "malformed json returns false",
			original: `{"event":`,
		},
		{
			name:     "unknown event returns false",
			original: `{"event":"tool.started","payload":{"text":"ignored"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := provider.ReplyText(agent.MessageContent{Original: []byte(tc.original)})
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantText, got)
		})
	}
}
