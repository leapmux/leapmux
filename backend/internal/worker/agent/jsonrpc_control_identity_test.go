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

// Two frames can spell the SAME number differently -- the request payload and the
// withdrawal notification are separate frames -- so every spelling of one value has to
// give one key, and a string id must stay a different request.
func TestJSONRPCControlRequestIdentityCanonicalizesNumbers(t *testing.T) {
	t.Parallel()
	for _, group := range [][]string{
		{`12`, `12.0`, `1.2e1`, `0.12e2`},
		{`0`, `-0`, `0.0`, `-0.0`, `0e5`},
		{`-7`, `-7.0`, `-0.7e1`},
		{`1000`, `1e3`, `1000.0`},
	} {
		first, ok := JSONRPCControlRequestID(json.RawMessage(group[0]))
		require.True(t, ok, group[0])
		for _, spelling := range group[1:] {
			key, ok := JSONRPCControlRequestID(json.RawMessage(spelling))
			require.True(t, ok, spelling)
			require.Equal(t, first, key, spelling)
		}
	}
	// A 64-bit integer id keeps every digit: float64 cannot hold this one.
	wide, ok := JSONRPCControlRequestID(json.RawMessage(`9007199254740993`))
	require.True(t, ok)
	require.Equal(t, "jsonrpc:9007199254740993", wide)
	// The string branch keeps its quotation marks, so "12" and 12 stay two requests.
	number, ok := JSONRPCControlRequestID(json.RawMessage(`12`))
	require.True(t, ok)
	text, ok := JSONRPCControlRequestID(json.RawMessage(`"12"`))
	require.True(t, ok)
	require.NotEqual(t, number, text)
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
