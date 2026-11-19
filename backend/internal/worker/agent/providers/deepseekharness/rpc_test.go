package deepseekharness

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type lostResponseRequest struct {
	id     string
	key    string
	method string
	cookie string
}

// responseLossRPC returns a real HTTP peer that drops its second response after
// reading the request. The first call establishes the connection that is reused.
func responseLossRPC(t *testing.T) (remoteRPC, func() []lostResponseRequest) {
	t.Helper()
	var mu sync.Mutex
	var requests []lostResponseRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var envelope struct {
			ID string `json:"rpcId"`
		}
		require.NoError(t, json.NewDecoder(request.Body).Decode(&envelope))
		mu.Lock()
		requests = append(requests, lostResponseRequest{
			id: envelope.ID, key: request.Header.Get("Idempotency-Key"),
			method: strings.TrimPrefix(request.URL.Path, "/api/"), cookie: request.Header.Get("Cookie"),
		})
		count := len(requests)
		mu.Unlock()
		if count == 2 {
			connection, _, err := w.(http.Hijacker).Hijack()
			require.NoError(t, err)
			require.NoError(t, connection.Close())
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": envelope.ID, "result": map[string]any{"ok": true, "value": "read completed"}}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return remoteRPC{endpoint: endpoint.WithHeader("Cookie", "native-cookie")}, func() []lostResponseRequest {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(requests)
	}
}

func TestRemoteRPCReadReplaysAReusedConnectionAfterResponseLoss(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"goals/get", "session/attachment", "session/projections", "session/modelCatalog", "permissionPresets/catalog", "session/page"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rpc, requests := responseLossRPC(t)
			_, err := rpc.value(t.Context(), method, nil)
			require.NoError(t, err)
			_, err = rpc.value(t.Context(), method, nil)
			require.NoError(t, err, "a native read can replay after its reused connection closes before a response")
			got := requests()
			require.Len(t, got, 3)
			assert.Equal(t, got[1], got[2], "replay preserves the native request, credential and idempotency key")
			assert.NotEqual(t, got[0].id, got[1].id, "separate reads keep separate identities")
			for _, request := range got {
				assert.NotEmpty(t, request.id)
				assert.Equal(t, request.id, request.key)
				assert.Equal(t, method, request.method)
				assert.Equal(t, "native-cookie", request.cookie)
			}
		})
	}
}

func TestRemoteRPCMutationDoesNotReplayAfterResponseLoss(t *testing.T) {
	t.Parallel()
	for _, method := range []string{"goals/create", "goals/edit", "goals/clear", "goals/pause", "goals/resume", "session/prompt", "session/create", "commands/execute", "fileUploads/upload", "session/selectModel", "session/cancel", "session/updateQueue", "subagents/prompt", "subagents/interruptByParent", "$events/result", "unknown/query"} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()
			rpc, requests := responseLossRPC(t)
			_, err := rpc.value(t.Context(), "goals/get", nil)
			require.NoError(t, err)
			_, err = rpc.value(t.Context(), method, nil)
			require.ErrorIs(t, err, io.EOF)
			got := requests()
			require.Len(t, got, 2, "an uncertain native mutation must never run twice")
			assert.Empty(t, got[1].key)
			assert.Equal(t, method, got[1].method)
		})
	}
}

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

// serveNativeReplies answers each native RPC method with its result envelope, records each call, and fails the test
// for a method that results does not list.
func serveNativeReplies(t *testing.T, a *Agent, results map[string]map[string]any) *nativeCalls {
	t.Helper()
	calls := &nativeCalls{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			ID      string `json:"rpcId"`
			Payload struct {
				Args map[string]any `json:"args"`
			} `json:"payload"`
		}
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&request)) {
			return
		}
		method := strings.TrimPrefix(r.URL.Path, "/api/")
		calls.mu.Lock()
		calls.calls = append(calls.calls, nativeCall{method: method, args: request.Payload.Args})
		calls.mu.Unlock()
		result, ok := results[method]
		if !ok {
			t.Errorf("unexpected native method %s", method)
			result = map[string]any{"ok": false, "error": map[string]any{"code": "test/unexpected", "message": method}}
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"type": "server-response", "rpcId": request.ID, "result": result}))
	}))
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, nil)
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	a.rpc = remoteRPC{endpoint: endpoint}
	return calls
}
