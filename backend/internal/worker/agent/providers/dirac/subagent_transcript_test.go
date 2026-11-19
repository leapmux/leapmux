package dirac

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func diracTranscriptRecords(t *testing.T) []contracts.DiracTranscriptRecord {
	t.Helper()
	messages := []json.RawMessage{
		json.RawMessage(`{"role":"assistant","content":[{"type":"tool_use","id":"read-1","name":"read_file","input":{"paths":["a.txt"]}}]}`),
		json.RawMessage(`{"role":"user","content":[{"type":"tool_result","tool_use_id":"read-1","content":"Actual native contents"}]}`),
	}
	result, err := diracTaskletToolRecords("parent", diracTaskletRecord{ID: "Witten", ConversationID: "child"}, messages)
	require.NoError(t, err)
	return result
}

func TestDiracTaskletTranscriptReplayKeepsOneImmutablePair(t *testing.T) {
	t.Parallel()
	sink := &agenttest.Sink{}
	sink.UpdateSessionID("child")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	writer, err := captureDiracTaskletTranscript(services, "child")
	require.NoError(t, err)
	records := diracTranscriptRecords(t)
	require.Len(t, records, 2)
	require.NoError(t, writer.publish(records))
	require.NoError(t, agent.FlushPendingOutput(services))
	require.NoError(t, writer.publish(records))
	require.NoError(t, agent.FlushPendingOutput(services))
	rows := sink.Messages()
	require.Len(t, rows, 2)
	var request, result contracts.DiracTranscriptRecord
	require.NoError(t, json.Unmarshal(rows[0].Content, &request))
	require.NoError(t, json.Unmarshal(rows[1].Content, &result))
	assert.Empty(t, request.Result)
	assert.JSONEq(t, `{"type":"tool_result","tool_use_id":"read-1","content":"Actual native contents"}`, string(result.Result))
	assert.Equal(t, rows[0].SpanID, rows[1].SpanID)
	assert.False(t, rows[0].Closing)
	assert.Empty(t, rows[0].Completion, "a declaration has no finished outcome")
	assert.True(t, rows[1].Closing)
	assert.Equal(t, "child", rows[0].AgentSessionID)
	assert.NotEqual(t, rows[0].IdempotencyKey, rows[1].IdempotencyKey)
}

func TestDiracTaskletToolRecordRefusesMixedNativeIdentities(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"session", "parent", "tasklet", "role", "result", "request result", "missing result", "retained result"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			records := diracTranscriptRecords(t)
			record := records[1]
			switch change {
			case "session":
				record.SessionID = ""
			case "parent":
				record.ParentSessionID = ""
			case "tasklet":
				record.TaskletID = ""
			case "role":
				record.Role = "other"
			case "result":
				record.Result = json.RawMessage(`{"type":"tool_result","tool_use_id":"other","content":"Foreign"}`)
			case "request result":
				record.Role = contracts.DiracRecordRoleRequest
			case "missing result":
				record.Result = nil
			case "retained result":
				record.Role = contracts.DiracRecordRoleRetained
			}
			_, err := diracTranscriptRecordKey(record)
			require.Error(t, err)
		})
	}
}
