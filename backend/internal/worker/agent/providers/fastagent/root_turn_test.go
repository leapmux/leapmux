package fastagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/acp/acptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type rootTurnObserver struct {
	*agenttest.Sink
	ended chan struct{}
	once  sync.Once
}

func (sink *rootTurnObserver) SetTurnState(state agent.TurnState, seq uint64) {
	sink.Sink.SetTurnState(state, seq)
	if !state.Active {
		sink.once.Do(func() { close(sink.ended) })
	}
}

func TestFastagentRootTurnKeepsTheCompleteNativeConversationOrder(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	beforeReply, allowReply := make(chan struct{}), make(chan struct{})
	var release sync.Once
	allow := func() { release.Do(func() { close(allowReply) }) }
	t.Cleanup(allow)
	ready := make(chan *Agent, 1)
	a, _ := acptest.NewAgentForRPCWithRequestResponder(t, func() *Agent {
		return &Agent{home: home, workingDir: cwd, nativeCommands: true, bypassPermissions: true}
	}, func(a *Agent) *acp.Base { return &a.Base }, func(request agenttest.RecordedRequest) agenttest.RPCReply {
		if request.Method != acp.MethodSessionPrompt {
			return agenttest.RPCReply{Result: json.RawMessage("{}")}
		}
		current := <-ready
		updates := []string{
			"{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"Before native command.\"}}",
			"{\"sessionUpdate\":\"tool_call\",\"toolCallId\":\"live-read\",\"title\":\"read_text_file\",\"kind\":\"read\",\"status\":\"in_progress\",\"rawInput\":{\"path\":\"note.txt\"}}",
			"{\"sessionUpdate\":\"tool_call_update\",\"toolCallId\":\"live-read\",\"status\":\"completed\",\"content\":[{\"type\":\"content\",\"content\":{\"type\":\"text\",\"text\":\"Native file content.\"}}]}",
			"{\"sessionUpdate\":\"tool_call\",\"toolCallId\":\"unrelated-live-bash-uuid\",\"title\":\"bash\",\"kind\":\"execute\",\"status\":\"in_progress\"}",
			"{\"sessionUpdate\":\"tool_call_update\",\"toolCallId\":\"unrelated-live-bash-uuid\",\"status\":\"completed\",\"content\":[{\"type\":\"content\",\"content\":{\"type\":\"text\",\"text\":\"limited live tail\"}}]}",
			"{\"sessionUpdate\":\"agent_message_chunk\",\"content\":{\"type\":\"text\",\"text\":\"After native command.\"}}",
		}
		for _, update := range updates {
			packet, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "session-1", "update": json.RawMessage(update)}})
			require.NoError(t, err)
			current.HandleOutput(packet)
		}
		directory := writeFastagentRootArchive(t, home, "session-1", cwd, nil)
		history := "{\"messages\":[" +
			"{\"role\":\"user\",\"content\":[{\"type\":\"text\",\"text\":\"Run the native command.\"}]}," +
			"{\"role\":\"assistant\",\"timestamp\":\"2026-10-26T00:00:00Z\",\"content\":[{\"type\":\"text\",\"text\":\"Before native command.\"}],\"tool_calls\":{\"native-read\":{\"method\":\"tools/call\",\"params\":{\"name\":\"read_text_file\",\"arguments\":{\"path\":\"note.txt\"}}}},\"stop_reason\":\"toolUse\"}," +
			"{\"role\":\"user\",\"tool_results\":{\"native-read\":{\"content\":[{\"type\":\"text\",\"text\":\"Native file content.\"}],\"isError\":false}}}," +
			"{\"role\":\"assistant\",\"timestamp\":\"2026-10-26T00:00:01Z\",\"tool_calls\":{\"native-bash\":{\"method\":\"tools/call\",\"params\":{\"name\":\"bash\",\"arguments\":{\"command\":\"printf complete\"}}}},\"stop_reason\":\"toolUse\"}," +
			"{\"role\":\"user\",\"tool_results\":{\"native-bash\":{\"content\":[{\"type\":\"text\",\"text\":\"Complete native command output.\"}],\"isError\":false}}}," +
			"{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"After native command.\"}],\"stop_reason\":\"endTurn\"}" +
			"]}"
		require.NoError(t, os.WriteFile(filepath.Join(directory, "history_agent.json"), []byte(history), 0o600))
		close(beforeReply)
		<-allowReply
		return agenttest.RPCReply{Result: json.RawMessage("{\"stopReason\":\"end_turn\"}")}
	})
	sink := &rootTurnObserver{Sink: &agenttest.Sink{}, ended: make(chan struct{})}
	sink.UpdateSessionID("session-1")
	services := agent.NewOrderedProviderServices(sink.OutputQueue(), agent.NewProviderServices(sink))
	a.SetSinkForTest(services)
	*a.HooksForTest() = a.hooks("", services)
	a.ObserveCurrentMode("agent")
	a.WireTurnActiveForTest()
	require.NoError(t, a.initializeRootArchive(false))
	ready <- a
	require.NoError(t, a.SendInput("Run the native command.", nil))
	ctx := testutil.DeadlineContext(t)
	select {
	case <-beforeReply:
	case <-ctx.Done():
		t.Fatal("the native peer did not finish its root updates")
	}
	assert.Empty(t, sink.Messages(), "the selected root-turn policy holds every root row until its native ending")
	allow()
	select {
	case <-sink.ended:
	case <-ctx.Done():
		t.Fatal("the completed root turn did not release its transcript")
	}
	rows := sink.Messages()
	require.Len(t, rows, 7)
	expected := []string{
		"{\"type\":\"text\",\"text\":\"Before native command.\"}",
		"{\"method\":\"tools/call\",\"params\":{\"name\":\"read_text_file\",\"arguments\":{\"path\":\"note.txt\"}}}",
		"{\"content\":[{\"type\":\"text\",\"text\":\"Native file content.\"}],\"isError\":false}",
		"{\"method\":\"tools/call\",\"params\":{\"name\":\"bash\",\"arguments\":{\"command\":\"printf complete\"}}}",
		"{\"content\":[{\"type\":\"text\",\"text\":\"Complete native command output.\"}],\"isError\":false}",
		"{\"type\":\"text\",\"text\":\"After native command.\"}",
	}
	for index, source := range expected {
		assert.JSONEq(t, source, string(rows[index].Content), "row %d retains its unchanged native element", index)
		assert.Equal(t, "session-1", rows[index].AgentSessionID)
	}
	assert.True(t, rows[6].TurnEnd)
}
