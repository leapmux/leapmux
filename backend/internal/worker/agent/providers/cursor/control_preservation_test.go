package cursor

import (
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestCursorControlsPreserveNativePayloadBytes(t *testing.T) {
	for _, method := range []string{contracts.CursorMethodAskQuestion, contracts.CursorMethodCreatePlan} {
		t.Run(method, func(t *testing.T) {
			sink := &agenttest.ControlSink{}
			agent := newCursorAgentWithSink(agent.NewProviderServices(sink))
			raw := []byte(fmt.Sprintf(` {"jsonrpc":"2.0", "id":9007199254740993, "method":%q, "params":{"questions":[{"id":"q","prompt":"Choose","options":[{"id":"zero","label":"Zero","description":"Keep zero.","preview":"  +---+\n  | 0 |\n  +---+"}]}],"name":"Plan","overview":"Keep the original plan."},"future":9007199254740995} `, method))
			agent.HandleOutput(raw)
			require.Equal(t, 1, sink.PublishedControlCount())
			request := sink.LastPublishedControl()
			require.Equal(t, "jsonrpc:9007199254740993", request.RequestID)
			require.Equal(t, raw, request.Payload)
		})
	}
}
