package agent

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONRPCResponseResultPreservesApplicationFields(t *testing.T) {
	for _, result := range []string{
		`{"message":"A valid command result"}`,
		`{"code":-32603,"message":"Application data","error":{"message":"Nested data"}}`,
		`{"id":9007199254740993, "empty":""}`,
		`null`, `false`, `0`, `""`, `[]`,
	} {
		t.Run(result, func(t *testing.T) {
			raw := json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":` + result + `}`)
			value, err := decodeJSONRPCResponse(raw)
			require.NoError(t, err)
			require.Equal(t, result, string(value))
		})
	}
}

func TestJSONRPCResponseRejectsInvalidEnvelopes(t *testing.T) {
	for _, raw := range []string{
		``, `not JSON`, `null`, `false`, `[]`, `{}`,
		`{"error":null}`,
		`{"error":{"code":-32603}}`,
		`{"error":{"message":"No code"}}`,
		`{"error":"wrong type"}`,
		`{"result":{},"error":{"code":-32603,"message":"Both fields"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeJSONRPCResponse(json.RawMessage(raw))
			require.Error(t, err)
		})
	}
}

func TestJSONRPCResponseRetainsAnErrorWithAnEmptyMessage(t *testing.T) {
	_, err := decodeJSONRPCResponse(json.RawMessage(`{"error":{"code":0,"message":""}}`))
	var responseError *jsonRPCResponseError
	require.ErrorAs(t, err, &responseError)
	require.Equal(t, 0, responseError.Code)
	require.Empty(t, responseError.Message)
	require.True(t, hasJSONRPCErrorCode(err, 0))
}
