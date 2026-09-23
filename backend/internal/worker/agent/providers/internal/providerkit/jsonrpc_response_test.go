package providerkit

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
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

// A provider that reports an error can spell the unused result member as an explicit
// null. The response states no result, so the caller must still read the error CODE --
// ClassifyJSONRPCDeliveryError reads it to tell a refusal from an unconfirmed delivery.
func TestJSONRPCResponseReadsAnErrorBesideANullResult(t *testing.T) {
	for _, raw := range []string{
		`{"jsonrpc":"2.0","id":1,"result":null,"error":{"code":-32600,"message":"No active turn"}}`,
		`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"No active turn"},"result":null}`,
		`{"jsonrpc":"2.0","id":1,"result": null ,"error":{"code":-32600,"message":"No active turn"}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := decodeJSONRPCResponse(json.RawMessage(raw))
			var responseError *JSONRPCResponseError
			require.ErrorAs(t, err, &responseError)
			require.Equal(t, -32600, responseError.Code)
			require.True(t, HasJSONRPCErrorCode(err, -32600, -32602))
			require.False(t, errors.Is(ClassifyJSONRPCDeliveryError("steer", err), agent.ErrDeliveryUncertain),
				"an error the provider reported is a refusal, not an unconfirmed delivery")
		})
	}
}

// A result that is genuinely present beside an error is still a broken envelope.
func TestJSONRPCResponseRejectsARealResultBesideAnError(t *testing.T) {
	_, err := decodeJSONRPCResponse(json.RawMessage(`{"result":0,"error":{"code":-32603,"message":"Both"}}`))
	require.ErrorContains(t, err, "both a result and an error")
}

func TestJSONRPCResponseRetainsAnErrorWithAnEmptyMessage(t *testing.T) {
	_, err := decodeJSONRPCResponse(json.RawMessage(`{"error":{"code":0,"message":""}}`))
	var responseError *JSONRPCResponseError
	require.ErrorAs(t, err, &responseError)
	require.Equal(t, 0, responseError.Code)
	require.Empty(t, responseError.Message)
	require.True(t, HasJSONRPCErrorCode(err, 0))
}
