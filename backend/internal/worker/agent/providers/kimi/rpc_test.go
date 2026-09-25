package kimi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
)

func newKimiTestClient(t *testing.T, handler http.HandlerFunc) *kimiClient {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	endpoint, err := providerkit.NewHTTPEndpoint(server.URL, providerkit.BearerAuth(fakeKapToken))
	require.NoError(t, err)
	t.Cleanup(endpoint.Close)
	return &kimiClient{endpoint: endpoint, timeout: 5 * time.Second}
}

func TestKimiClientCall(t *testing.T) {
	t.Parallel()

	t.Run("decodes the data of a successful reply", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(t, "Bearer "+fakeKapToken, r.Header.Get("Authorization"))
			writeFakeKapEnvelope(w, http.StatusOK, 0, "success", map[string]any{"id": "session_1"})
		})
		var out kimiSessionReply
		require.NoError(t, client.get(context.Background(), kimiRouteSessions, &out))
		assert.Equal(t, "session_1", out.ID)
	})

	t.Run("refuses a non-zero code on a 200 reply", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeFakeKapEnvelope(w, http.StatusOK, kimiCodeGoalExists, " a goal exists ", nil)
		})
		err := client.post(context.Background(), "/api/v1/x", nil, nil)
		var apiErr *kimiAPIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, kimiCodeGoalExists, apiErr.Code)
		assert.Equal(t, "a goal exists", apiErr.Msg)
		assert.Zero(t, apiErr.Status)
		code, stated := kimiErrorCode(err)
		assert.True(t, stated)
		assert.Equal(t, kimiCodeGoalExists, code)
		assert.Contains(t, err.Error(), "POST /api/v1/x")
	})

	t.Run("accepts a code the caller states", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeFakeKapEnvelope(w, http.StatusOK, kimiCodeQuestionDismissed, "dismissed", nil)
		})
		assert.NoError(t, client.post(context.Background(), "/api/v1/x", nil, nil, kimiCodeQuestionDismissed))
	})

	t.Run("reads the envelope of a non-2xx reply", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeFakeKapEnvelope(w, http.StatusConflict, kimiCodeAlreadyResolved, "already resolved", nil)
		})
		err := client.post(context.Background(), "/api/v1/x", nil, nil)
		var apiErr *kimiAPIError
		require.ErrorAs(t, err, &apiErr)
		assert.Equal(t, kimiCodeAlreadyResolved, apiErr.Code)
		assert.Equal(t, http.StatusConflict, apiErr.Status)
	})

	t.Run("reports the status of a non-2xx reply with no envelope", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "gateway down", http.StatusBadGateway)
		})
		err := client.get(context.Background(), "/api/v1/x", nil)
		_, stated := kimiErrorCode(err)
		assert.False(t, stated)
		assert.True(t, providerkit.IsHTTPStatus(err, http.StatusBadGateway))
	})

	// An envelope that states success on a failed reply explains nothing, so the
	// status line is the reason.
	t.Run("reports the status of a non-2xx reply whose envelope states success", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeFakeKapEnvelope(w, http.StatusInternalServerError, 0, "success", nil)
		})
		err := client.get(context.Background(), "/api/v1/x", nil)
		_, stated := kimiErrorCode(err)
		assert.False(t, stated)
		assert.True(t, providerkit.IsHTTPStatus(err, http.StatusInternalServerError))
	})

	t.Run("a caller's own deadline wins over the client's timeout", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			writeFakeKapEnvelope(w, http.StatusOK, 0, "success", map[string]any{"id": "session_1"})
		})
		// A timeout this short ends any request that takes it.
		client.timeout = time.Nanosecond
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var out kimiSessionReply
		require.NoError(t, client.get(ctx, "/api/v1/x", &out))
		assert.Equal(t, "session_1", out.ID)
	})

	t.Run("takes a null or absent data as nothing", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"msg":"success","data":null}`))
		})
		out := kimiSessionReply{ID: "kept"}
		require.NoError(t, client.get(context.Background(), "/api/v1/x", &out))
		assert.Equal(t, "kept", out.ID)
	})

	t.Run("reports data that does not decode", func(t *testing.T) {
		t.Parallel()
		client := newKimiTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"code":0,"data":"not an object"}`))
		})
		var out kimiSessionReply
		err := client.get(context.Background(), "/api/v1/x", &out)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "decode the reply data")
	})

	t.Run("sends an empty object for a nil body", func(t *testing.T) {
		t.Parallel()
		got := make(chan string, 1)
		client := newKimiTestClient(t, func(w http.ResponseWriter, r *http.Request) {
			var body json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&body)
			got <- string(body)
			writeFakeKapEnvelope(w, http.StatusOK, 0, "success", nil)
		})
		require.NoError(t, client.post(context.Background(), "/api/v1/sessions/s:abort", nil, nil))
		assert.JSONEq(t, `{}`, <-got)
	})

	t.Run("limits a request that states no deadline", func(t *testing.T) {
		t.Parallel()
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		client := newKimiTestClient(t, func(_ http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		})
		client.timeout = 50 * time.Millisecond
		err := client.get(context.Background(), "/api/v1/x", nil)
		require.ErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestClassifyKimiDeliveryError(t *testing.T) {
	t.Parallel()

	stated := &kimiAPIError{Code: 40001}
	assert.Same(t, error(stated), classifyKimiDeliveryError(stated), "a refusal the server stated is a clear failure")

	status := &providerkit.HTTPStatusError{StatusCode: http.StatusBadGateway}
	assert.Same(t, error(status), classifyKimiDeliveryError(status))

	lost := classifyKimiDeliveryError(context.DeadlineExceeded)
	assert.ErrorContains(t, lost, "did not confirm")
	assert.ErrorIs(t, lost, agent.ErrDeliveryUncertain, "the queue must not send a prompt the server may have taken")
	assert.NotErrorIs(t, stated, agent.ErrDeliveryUncertain)
}

func TestKimiAPIErrorMessage(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "POST /api/v1/x: a goal exists (code 40913)",
		(&kimiAPIError{Method: "POST", Path: "/api/v1/x", Code: kimiCodeGoalExists, Msg: "a goal exists"}).Error())
	assert.Equal(t, "GET /api/v1/y: the request was refused (code 50000)",
		(&kimiAPIError{Method: "GET", Path: "/api/v1/y", Code: 50000}).Error(), "a refusal with no message still states one")
}
