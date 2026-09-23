package codex

import (
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/require"
)

func TestCodexControlResolutionKeepsNativeIDTypesSeparate(t *testing.T) {
	sink := &agenttest.ControlSink{}
	a := newCodexAgentWithSink(agent.NewProviderServices(sink))
	for _, nativeID := range []string{`7`, `"7"`, `""`, `9007199254740993`} {
		a.HandleOutput([]byte(`{"id":` + nativeID + `,"method":"item/commandExecution/requestApproval","params":{}}`))
		request := sink.LastPublishedControl()
		a.handleServerRequestResolved(json.RawMessage(`{"requestId":` + nativeID + `}`))
		canceled := sink.CanceledControls()
		require.Equal(t, request.RequestID, canceled[len(canceled)-1])
	}
	canceled := sink.CanceledControls()
	require.NotEqual(t, canceled[0], canceled[1])
	for _, invalid := range []string{`{}`, `{"requestId":null}`, `{"requestId":false}`, `{"requestId":{}}`, `broken`} {
		a.handleServerRequestResolved(json.RawMessage(invalid))
	}
	require.Len(t, sink.CanceledControls(), 4)
}
