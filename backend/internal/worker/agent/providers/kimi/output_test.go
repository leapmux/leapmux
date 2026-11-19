package kimi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// kimiOutputRig is an agent with no server, fed events of session_1.
type kimiOutputRig struct {
	agent *Agent
	sink  *agenttest.ControlSink
}

func newKimiOutputRig(t *testing.T) *kimiOutputRig {
	t.Helper()
	sink := &agenttest.ControlSink{}
	return &kimiOutputRig{agent: newOfflineKimiAgent(t, sink), sink: sink}
}

func (r *kimiOutputRig) sessionID() string {
	r.agent.Mu.Lock()
	defer r.agent.Mu.Unlock()
	return r.agent.sessionID
}

func (r *kimiOutputRig) feed(t *testing.T, payload map[string]any) {
	t.Helper()
	r.agent.HandleOutput(kimiEventFrame(t, "session_1", payload))
}

// assembledRow decodes a row the worker assembled from deltas.
func assembledRow(t *testing.T, message agenttest.Message) (kind, text string) {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row), string(message.Content))
	require.Equal(t, contracts.AssembledMessageType, row[contracts.AssembledMessageFieldType])
	return row[contracts.AssembledMessageFieldKind], row[contracts.AssembledMessageFieldText]
}

// assembledCompletion reads the completion an assembled row states.
func assembledCompletion(t *testing.T, message agenttest.Message) string {
	t.Helper()
	var row map[string]string
	require.NoError(t, json.Unmarshal(message.Content, &row), string(message.Content))
	return row[contracts.AssembledMessageFieldCompletion]
}

// eventType reads the `type` of a persisted event payload.
func eventType(t *testing.T, message agenttest.Message) string {
	t.Helper()
	var head struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(message.Content, &head), string(message.Content))
	return head.Type
}

func metadataField(t *testing.T, message agenttest.Message, key string) (json.RawMessage, bool) {
	t.Helper()
	if len(message.Metadata) == 0 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(message.Metadata, &fields))
	value, ok := fields[key]
	return value, ok
}

func TestKimiTurnEndCarriesTheToolCountAndTheContext(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "t0", 0)
	rig.agent.HandleOutput([]byte(`{"type":"usage","session_id":"session_1","agent_id":"main","context_tokens":1200,"max_context_tokens":262144}`))
	for _, id := range []string{"call_1", "call_2"} {
		common := fmt.Sprintf(`"type":"tool_call","item_id":%q,"step_index":1,"name":"Read","created_at":"2026-11-18T00:00:00Z","started_at":"2026-11-18T00:00:00Z","input":{"path":"a.go"}`, id)
		rig.conversationFrame(t, `{"type":"item.started","turn_id":"t0","item":{`+common+`,"status":"running","stage":"executing"}}`)
		rig.conversationFrame(t, `{"type":"item.ended","turn_id":"t0","item":{`+common+`,"status":"ended","outcome":"completed","output":"package a"}}`)
	}
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"t0","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:00Z","usage":{"input_other":10,"output":5,"input_cache_read":100,"input_cache_creation":1}}}`)
	rig.endPublicTurn(t, kimiMainAgentID, "t0", 0, "completed")
	awaitKimiTestOutput(t, rig.agent.sink)
	messages := rig.sink.Messages()
	require.Len(t, messages, 5)
	turnEnd := messages[len(messages)-1]
	require.True(t, turnEnd.TurnEnd)
	count, ok := metadataField(t, turnEnd, contracts.MessageMetadataFieldToolUses)
	require.True(t, ok, "the divider states the turn's tool count")
	assert.JSONEq(t, `2`, string(count))
	usage, ok := metadataField(t, turnEnd, contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "a reconnecting client reads the context off the turn end")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(usage, &fields))
	assert.EqualValues(t, 1200, fields[contracts.ContextUsageFieldContextTokens])
	assert.EqualValues(t, 262144, fields[contracts.ContextUsageFieldContextWindow])
	assert.EqualValues(t, 10, fields["input_tokens"])
	assert.EqualValues(t, 5, fields["output_tokens"])
	assert.EqualValues(t, 100, fields["cache_read_input_tokens"])
	assert.EqualValues(t, 1, fields["cache_creation_input_tokens"])

	info, ok := rig.sink.LastSessionInfoValue(contracts.SessionInfoKeyContextUsage)
	require.True(t, ok, "the status update broadcasts the context")
	assert.NotNil(t, info)
}

func TestKimiDiscardOutputRefusesPublicItemsAndNotices(t *testing.T) {
	t.Parallel()
	rig := newKimiConversationRig(t)
	rig.startPublicTurn(t, kimiMainAgentID, "retry", 0)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry","step":{"index":1,"outcome":"failed","started_at":"2026-11-18T00:00:00Z"},"retry":{"attempt":1,"next_attempt":2,"max_attempts":10,"delay_ms":0,"error":{"name":"status","message":"Failed"}}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry"}`)
	awaitKimiTestOutput(t, rig.agent.sink)
	require.Len(t, rig.sink.PersistedNotifications(), 1, "the test needs a retained retry to discard")
	rig.agent.DiscardOutput()
	rig.conversationFrame(t, `{"type":"item.ended","turn_id":"retry","item":{"type":"assistant_message","item_id":"answer","step_index":1,"status":"ended","outcome":"completed","created_at":"2026-11-18T00:00:00Z","text":"Discarded answer"}}`)
	rig.conversationFrame(t, `{"type":"turn.updated","turn_id":"retry","step":{"index":1,"outcome":"completed","started_at":"2026-11-18T00:00:00Z"}}`)
	rig.conversationFrame(t, `{"type":"notice","level":"warning","title":"Discarded warning"}`)
	rig.conversationFrame(t, kimiPublicCompactionFixtureSnapshot)
	awaitKimiTestOutput(t, rig.agent.sink)
	assert.Empty(t, rig.sink.Messages())
	assert.Len(t, rig.sink.PersistedNotifications(), 1)
	assert.Empty(t, rig.sink.Notifications(), "discarded output cannot resolve the retained retry")
}

func TestKimiWithContextUsage(t *testing.T) {
	t.Parallel()

	content := agent.MessageContent{Original: []byte(`{}`), Metadata: []byte(`{"num_tool_uses":2}`)}
	got := kimiWithContextUsage(content, map[string]any{"context_tokens": 5})
	assert.JSONEq(t, `{"num_tool_uses":2,"context_usage":{"context_tokens":5}}`, string(got.Metadata))

	assert.Equal(t, content, kimiWithContextUsage(content, nil), "no readout leaves the metadata alone")

	bad := agent.MessageContent{Metadata: []byte(`not json`)}
	assert.Equal(t, bad, kimiWithContextUsage(bad, map[string]any{"context_tokens": 5}))

	empty := kimiWithContextUsage(agent.MessageContent{}, map[string]any{"context_tokens": 5})
	assert.JSONEq(t, `{"context_usage":{"context_tokens":5}}`, string(empty.Metadata))
}

func TestKimiSpawnFromArgs(t *testing.T) {
	t.Parallel()

	spawn := kimiSpawnFromArgs("span", contracts.KimiToolAgent, json.RawMessage(`{"prompt":" Review it. ","description":" Review "}`))
	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent, label: "Review"}, spawn)

	swarm := kimiSpawnFromArgs("span", contracts.KimiToolAgentSwarm, json.RawMessage(`{"prompt":"ignored","description":"Audit"}`))
	assert.Equal(t, "Audit", swarm.label)

	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent}, kimiSpawnFromArgs("span", contracts.KimiToolAgent, json.RawMessage(`[1]`)))
	assert.Equal(t, kimiSpawn{spanID: "span", name: contracts.KimiToolAgent}, kimiSpawnFromArgs("span", contracts.KimiToolAgent, nil))
}
