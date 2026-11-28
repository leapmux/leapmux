package antigravity

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

func TestAntigravityReplyTextEdgeCases(t *testing.T) {
	provider := antigravityProvider{}
	for _, tc := range []struct {
		name     string
		original string
		wantText string
		wantOK   bool
	}{
		{
			name:     "interaction frame ignored even with planner step type",
			original: `{"name":"` + contracts.AntigravityFrameInteraction + `","type":"` + contracts.AntigravityStepTypePlannerResponse + `","plannerResponse":{"response":"ignored"}}`,
		},
		{
			name:     "turn end frame ignored even with planner step type",
			original: `{"name":"` + contracts.AntigravityFrameTurnEnd + `","type":"` + contracts.AntigravityStepTypePlannerResponse + `","plannerResponse":{"response":"ignored"}}`,
		},
		{
			name:     "non-string modifiedResponse and response",
			original: `{"type":"` + contracts.AntigravityStepTypePlannerResponse + `","plannerResponse":{"modifiedResponse":123,"response":{"nested":true}}}`,
		},
		{
			name:     "non-string modifiedResponse falls back to string response",
			original: `{"type":"` + contracts.AntigravityStepTypePlannerResponse + `","plannerResponse":{"modifiedResponse":["bad"],"response":"fallback text"}}`,
			wantText: "fallback text",
			wantOK:   true,
		},
		{
			name:     "malformed json",
			original: `{"type":`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := provider.ReplyText(agent.MessageContent{Original: []byte(tc.original)})
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantText, got)
		})
	}
}
