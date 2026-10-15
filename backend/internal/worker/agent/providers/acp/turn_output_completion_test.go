package acp

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPCompletedTurnKeepsInterruptedTextCompletion(t *testing.T) {
	t.Parallel()
	for _, interrupted := range []bool{false, true} {
		for _, route := range []string{"prompt-response", "agent-work"} {
			for _, kind := range []agent.AssembledMessageKind{agent.AssembledMessageKindText, agent.AssembledMessageKindReasoning} {
				name := route + "/" + string(kind)
				if interrupted {
					name += "/interrupted"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					sink := &agenttest.Sink{}
					base := &Base{sink: agent.NewProviderServices(sink), sessionID: "session", promptActive: true}
					if interrupted {
						base.noteACPInterruptRequested()
					}
					if kind == agent.AssembledMessageKindText {
						base.main().out.appendAssistant("partial content")
					} else {
						base.main().out.appendThought("partial content")
					}
					if route == "prompt-response" {
						response := json.RawMessage(`{"stopReason":"end_turn"}`)
						if interrupted {
							response = json.RawMessage(`{"stopReason":"cancelled"}`)
						}
						base.handleACPPromptResponse(response)
					} else {
						base.storeWorkOutput(base.drainTurn())
					}
					want := agent.MessageCompletionComplete
					if interrupted {
						want = agent.MessageCompletionInterrupted
					}
					count := 0
					for _, message := range sink.Messages() {
						storedKind, text, completion, ok := agenttest.DecodeAssembledMessage(message.Content)
						if !ok {
							continue
						}
						count++
						assert.Equal(t, kind, storedKind)
						assert.Equal(t, "partial content", text)
						assert.Equal(t, want, completion)
					}
					require.Equal(t, 1, count, "the ending turn stores its text once")
				})
			}
		}
	}
}
