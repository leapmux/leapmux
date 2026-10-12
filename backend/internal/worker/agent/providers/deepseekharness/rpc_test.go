package deepseekharness

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoteRPCUsesExactNativeEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/session/create", r.URL.Path)
		assert.Equal(t, "POST", r.Method)
		assert.Equal(t, "private-cookie", r.Header.Get("Cookie"))
		var request map[string]json.RawMessage
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		assert.JSONEq(t, `"client-request"`, string(request["type"]))
		assert.JSONEq(t, `"session/create"`, string(request["method"]))
		assert.JSONEq(t, `{"args":{"request":{"cwd":"/workspace"}}}`, string(request["payload"]))
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request["rpcId"], "result": map[string]any{"ok": true, "value": map[string]string{"sessionId": "native-session"}}}))
	}))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	endpoint = endpoint.WithHeader("Cookie", "private-cookie")
	var value struct {
		SessionID string `json:"sessionId"`
	}
	require.NoError(t, (remoteRPC{endpoint: endpoint}).request(context.Background(), "session/create", map[string]string{"cwd": "/workspace"}, &value))
	assert.Equal(t, "native-session", value.SessionID)
}

func TestRemoteRPCRejectsInvalidResponses(t *testing.T) {
	for _, tc := range []struct {
		name         string
		result       any
		wrongID      bool
		responseType string
		want         string
	}{
		{name: "missing result", want: "invalid fields"},
		{name: "missing ok", result: map[string]any{}, want: "invalid fields"},
		{name: "wrong id", result: map[string]any{"ok": true, "value": 0}, wrongID: true, want: "correlation"},
		{name: "wrong type", result: map[string]any{"ok": true, "value": 0}, responseType: "other", want: "invalid fields"},
		{name: "failure without cause", result: map[string]any{"ok": false}, want: "no cause"},
		{name: "native failure", result: map[string]any{"ok": false, "error": map[string]string{"code": "session/conflict", "message": "wrong workspace"}}, want: "session/conflict: wrong workspace"},
		{name: "success with error", result: map[string]any{"ok": true, "error": map[string]string{"code": "x", "message": "y"}}, want: "carries an error"},
		{name: "missing value", result: map[string]any{"ok": true}, want: "no value"},
		{name: "bad value", result: map[string]any{"ok": true, "value": "not a number"}, want: "decode"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID string `json:"rpcId"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				id := request.ID
				if tc.wrongID {
					id = "another-call"
				}
				kind := tc.responseType
				if kind == "" {
					kind = "server-response"
				}
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": kind, "rpcId": id, "result": tc.result}))
			}))
			defer server.Close()
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			defer endpoint.Close()
			var number int
			err = (remoteRPC{endpoint: endpoint}).call(context.Background(), "session/value", nil, &number)
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestRemoteRPCPreservesZeroAndEmptyValues(t *testing.T) {
	for _, value := range []any{0, "", false, []any{}, map[string]any{}} {
		t.Run(stringMustJSON(t, value), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					ID string `json:"rpcId"`
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
				require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": map[string]any{"ok": true, "value": value}}))
			}))
			defer server.Close()
			endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
			require.NoError(t, err)
			defer endpoint.Close()
			var actual any
			require.NoError(t, (remoteRPC{endpoint: endpoint}).call(context.Background(), "session/value", nil, &actual))
			assert.JSONEq(t, stringMustJSON(t, value), stringMustJSON(t, actual))
		})
	}
}

func TestRemoteRPCRefusesDisconnectedAndInvalidMethods(t *testing.T) {
	require.ErrorContains(t, (remoteRPC{}).call(context.Background(), "session/create", nil, nil), "no connected endpoint")
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("invalid RPC reached the server") }))
	defer server.Close()
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	defer endpoint.Close()
	for _, method := range []string{"", "session/create?secret=x", "session/create\x00", "session/create#x"} {
		require.ErrorContains(t, (remoteRPC{endpoint: endpoint}).call(context.Background(), method, nil, nil), "method is invalid")
	}
}

func stringMustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}
