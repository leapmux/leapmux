package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli"
)

func TestAgentContextCommandsOverIPC_QueueTheirInputKind(t *testing.T) {
	id := fullTestID("ag-2")
	for verb, want := range map[string]contextCommand{"clear": clearContext, "compact": compactContext} {
		t.Run(verb, func(t *testing.T) {
			w := newQueueWorker(queuedInput(inputAlpha, "Run the tests", 1))
			startAgentWorker(t, id, w)

			data := runControl(t, "agent", verb, id).Data(t)
			assert.Equal(t, id, data["id"])
			inputID, _ := data["input_id"].(string)
			require.NotEmpty(t, inputID)
			requireRequests(t, w, &leapmuxv1.EnqueueAgentInputRequest{AgentId: id, InputId: inputID, Kind: want.kind, Text: want.text})
			assert.Equal(t, []string{inputAlpha, inputID}, w.itemIDs(), "the command waits behind the inputs before it")
		})
	}
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_CLEAR_CONTEXT, clearContext.kind)
	assert.Equal(t, "/clear", clearContext.text, "the transcript shows the row that a user's /clear shows")
	assert.Equal(t, leapmuxv1.AgentInputKind_AGENT_INPUT_KIND_COMPACT_CONTEXT, compactContext.kind)
	assert.Equal(t, "/compact", compactContext.text, "a provider without native compaction receives /compact")
}

func TestAgentContextCommandsOverIPC_Refusals(t *testing.T) {
	id := fullTestID("ag-2")
	w := newQueueWorker()
	startAgentWorker(t, id, w)

	for _, verb := range []string{"clear", "compact"} {
		f := runControl(t, "agent", verb).requireFailure(t, cli.CodeUsage)
		assert.Contains(t, f.Message, "missing argument <agent>", "a context command names its agent")
	}
	assert.Empty(t, w.recorded())

	// A subagent accepts no context command, and the Worker says so.
	w.refuse(codes.InvalidArgument, "invalid queued agent input: this agent does not accept that input")
	for _, verb := range []string{"clear", "compact"} {
		f := runControl(t, "agent", verb, id).requireFailure(t, cli.CodeInvalidArgument)
		assert.Contains(t, f.Message, "does not accept that input")
	}
}
