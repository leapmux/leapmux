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

// An agent can state the cause of an error only in the JSON-RPC `data` member.
// The probed shapes: the ACP TypeScript SDK wraps an unexpected failure of
// Qwen Code 0.24.7 as `{"details": ...}`, and Grok Build 1.0.46 writes
// `{"message": ..., "http_status": ...}`. The error keeps the member and states
// it, so a failure note shows the cause and not only "Internal error".
func TestJSONRPCResponseErrorStatesItsData(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     string
		wantText string
	}{
		{name: "an ACP SDK object", data: `{"details":"400 NATIVEERRORprobe"}`, wantText: `json-rpc error -32603: Internal error: {"details":"400 NATIVEERRORprobe"}`},
		{name: "an object with spaces", data: `{ "message" : "API error (status 400): NATIVEERRORprobe", "http_status" : 400 }`, wantText: `json-rpc error -32603: Internal error: {"message":"API error (status 400): NATIVEERRORprobe","http_status":400}`},
		{name: "a string", data: `"400 NATIVEERRORprobe"`, wantText: `json-rpc error -32603: Internal error: 400 NATIVEERRORprobe`},
		{name: "a string with outer spaces", data: `"  400 NATIVEERRORprobe  "`, wantText: `json-rpc error -32603: Internal error: 400 NATIVEERRORprobe`},
		{name: "a number", data: `7`, wantText: `json-rpc error -32603: Internal error: 7`},
		{name: "an array", data: `["first", 2]`, wantText: `json-rpc error -32603: Internal error: ["first",2]`},
		{name: "null", data: `null`, wantText: `json-rpc error -32603: Internal error`},
		{name: "an empty string", data: `""`, wantText: `json-rpc error -32603: Internal error`},
		{name: "a blank string", data: `"   "`, wantText: `json-rpc error -32603: Internal error`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := json.RawMessage(`{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"Internal error","data":` + tc.data + `}}`)
			_, err := decodeJSONRPCResponse(raw)
			var responseError *JSONRPCResponseError
			require.ErrorAs(t, err, &responseError)
			require.Equal(t, -32603, responseError.Code)
			require.Equal(t, "Internal error", responseError.Message)
			if tc.data == "null" {
				require.Nil(t, responseError.Data, "a null member is no data")
			} else {
				require.JSONEq(t, tc.data, string(responseError.Data), "the error keeps the data as the agent sent it")
			}
			require.Equal(t, tc.wantText, err.Error())
			require.True(t, HasJSONRPCErrorCode(err, -32603), "the data changes no code")
		})
	}
}

// An error with no data reads as it did, and an error whose message is empty
// still states its data.
func TestJSONRPCResponseErrorTextWithoutDataOrMessage(t *testing.T) {
	_, err := decodeJSONRPCResponse(json.RawMessage(`{"error":{"code":-32603,"message":"Internal error"}}`))
	var responseError *JSONRPCResponseError
	require.ErrorAs(t, err, &responseError)
	require.Nil(t, responseError.Data)
	require.Equal(t, "json-rpc error -32603: Internal error", err.Error())

	_, err = decodeJSONRPCResponse(json.RawMessage(`{"error":{"code":-32000,"message":"","data":{"details":"cause"}}}`))
	require.Equal(t, `json-rpc error -32000: {"details":"cause"}`, err.Error())
}

func TestJSONRPCResponseRetainsAnErrorWithAnEmptyMessage(t *testing.T) {
	_, err := decodeJSONRPCResponse(json.RawMessage(`{"error":{"code":0,"message":""}}`))
	var responseError *JSONRPCResponseError
	require.ErrorAs(t, err, &responseError)
	require.Equal(t, 0, responseError.Code)
	require.Empty(t, responseError.Message)
	require.True(t, HasJSONRPCErrorCode(err, 0))
}
