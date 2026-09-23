package agent

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestControlResponseIDReplacementPreservesEveryOtherByte(t *testing.T) {
	response := []byte(" { \"id\" : \"jsonrpc:9007199254740993\", \"extra\":1, \"extra\":2, \"result\":{\"large\":9999999999999999999999999,\"enabled\":false} } \n")
	resolution := restoreControlResponseID(ControlResponseContext{
		RequestID:       "jsonrpc:9007199254740993",
		RequestPayload:  []byte(`{"id":9007199254740993,"method":"session/request_permission"}`),
		ResponseContent: response,
	})
	require.False(t, resolution.Withhold)
	expected := bytes.Replace(response, []byte(`"jsonrpc:9007199254740993"`), []byte(`9007199254740993`), 1)
	require.Equal(t, expected, resolution.Content)
}

func TestControlResponseRejectsAnUnmatchedNativeRequestID(t *testing.T) {
	for _, request := range []string{
		`{"id":"another-request","method":"session/request_permission"}`,
		`{"id":null,"method":"session/request_permission"}`,
		`{"method":"session/request_permission"}`,
		`{malformed`,
	} {
		resolution := restoreControlResponseID(ControlResponseContext{
			RequestPayload: []byte(request), ResponseContent: []byte(`{"id":"request","result":false}`),
		})
		require.True(t, resolution.Withhold, request)
	}
}

func TestControlResponseIDKeepsUnrelatedEnvelopesAndExactMatchingBytes(t *testing.T) {
	for _, response := range []string{
		` {"id":7,"result":false,"future":9007199254740993} `,
		`{"response":{"request_id":"request","response":{"behavior":"allow"}}}`,
		`{"id":"request","method":"session/notify"}`,
		`{"id":"request"}`,
		`{malformed`,
	} {
		resolution := restoreControlResponseID(ControlResponseContext{
			RequestPayload: []byte(`{"id":7,"method":"session/request_permission"}`), ResponseContent: []byte(response),
		})
		require.False(t, resolution.Withhold, response)
		require.Equal(t, response, string(resolution.Content))
	}
}
