package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONRPCControlRequestIdentityValidation(t *testing.T) {
	for _, invalid := range []string{"", " ", "null", "true", "false", "[]", "{}", "01", "NaN", `"broken`} {
		_, valid := JSONRPCControlRequestID(json.RawMessage(invalid))
		require.False(t, valid, invalid)
	}
	for _, valid := range []string{`0`, `-1`, `1e3`, `9007199254740993`, `""`, `"7"`} {
		key, ok := JSONRPCControlRequestID(json.RawMessage(valid))
		require.True(t, ok, valid)
		require.NotEmpty(t, key)
	}
	literal, ok := JSONRPCControlRequestID(json.RawMessage(`"request"`))
	require.True(t, ok)
	escaped, ok := JSONRPCControlRequestID(json.RawMessage(` "\u0072equest" `))
	require.True(t, ok)
	require.Equal(t, literal, escaped)
}

type controlIdentityCancelSink struct {
	recordingControlSink
	cancelled []string
}

func (s *controlIdentityCancelSink) CancelControlRequest(id string) {
	s.cancelled = append(s.cancelled, id)
}

func TestCodexControlResolutionKeepsNativeIDTypesSeparate(t *testing.T) {
	sink := &controlIdentityCancelSink{}
	a := newCodexAgentWithSink(sink)
	for _, nativeID := range []string{`7`, `"7"`, `""`, `9007199254740993`} {
		a.HandleOutput([]byte(`{"id":` + nativeID + `,"method":"item/commandExecution/requestApproval","params":{}}`))
		request := sink.LastPublishedControl()
		a.handleServerRequestResolved(json.RawMessage(`{"requestId":` + nativeID + `}`))
		require.Equal(t, request.RequestID, sink.cancelled[len(sink.cancelled)-1])
	}
	require.NotEqual(t, sink.cancelled[0], sink.cancelled[1])
	for _, invalid := range []string{`{}`, `{"requestId":null}`, `{"requestId":false}`, `{"requestId":{}}`, `broken`} {
		a.handleServerRequestResolved(json.RawMessage(invalid))
	}
	require.Len(t, sink.cancelled, 4)
}
