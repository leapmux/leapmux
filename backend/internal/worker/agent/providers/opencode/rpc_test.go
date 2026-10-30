package opencode

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenCodeNativeProbeAuthenticatesAndRetainsOwnerIdentity(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		state   openCodeServiceState
		success bool
	}{
		{name: "ready", status: 200, body: `{"version":"2.2.1","pid":42}`, success: true},
		{name: "starting", status: 503, body: `{"version":"2.2.1","pid":42}`, state: openCodeServiceStarting, success: true},
		{name: "failed", status: 500, body: `{"version":"2.2.1","pid":42}`},
		{name: "wrong owner", status: 200, body: `{"version":"2.2.1","pid":43}`},
		{name: "wrong version", status: 200, body: `{"version":"other","pid":42}`},
		{name: "fractional PID", status: 200, body: `{"version":"2.2.1","pid":42.5}`},
		{name: "null PID", status: 200, body: `{"version":"2.2.1","pid":null}`},
		{name: "empty response", status: 200},
		{name: "trailing response", status: 200, body: `{"version":"2.2.1","pid":42} {}`},
		{name: "null response", status: 200, body: `null`},
		{name: "authentication failure", status: 401, body: `{"version":"2.2.1","pid":42}`},
		{name: "incompatible endpoint", status: 404, body: `{"version":"2.2.1","pid":42}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/api/info", r.URL.Path)
				username, password, ok := r.BasicAuth()
				assert.True(t, ok)
				assert.Equal(t, "opencode", username)
				assert.Equal(t, "private-credential", password)
				w.WriteHeader(tc.status)
				_, err := fmt.Fprint(w, tc.body)
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			rpc := newOpenCodeRPC(openCodeServiceRegistration{ID: "instance", Version: openCodeNativeVersion, PID: 42, URL: server.URL, Password: "private-credential"})
			t.Cleanup(rpc.close)
			state, err := rpc.probe(t.Context())
			if tc.success {
				require.NoError(t, err)
				assert.Equal(t, tc.state, state)
			} else {
				require.Error(t, err)
				assert.NotContains(t, err.Error(), "private-credential")
			}
		})
	}
}

func TestOpenCodeNativeProbeRefusesRedirectsWithoutForwardingCredentials(t *testing.T) {
	t.Parallel()
	var received atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { received.Add(1) }))
	t.Cleanup(target.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)
	rpc := newOpenCodeRPC(openCodeServiceRegistration{Version: openCodeNativeVersion, PID: 42, URL: server.URL, Password: "private-credential"})
	t.Cleanup(rpc.close)
	_, err := rpc.probe(t.Context())
	require.Error(t, err)
	assert.Zero(t, received.Load())
	assert.NotContains(t, err.Error(), "private-credential")
}

func TestOpenCodeNativeRequestRetainsContextCancellation(t *testing.T) {
	t.Parallel()
	rpc := newOpenCodeRPC(openCodeServiceRegistration{URL: "http://127.0.0.1:1", Password: "private-credential"})
	t.Cleanup(rpc.close)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := rpc.probe(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.NotContains(t, err.Error(), "private-credential")
}

func TestOpenCodeNativeProbeUsesTheRootPathForATrailingSlashRegistration(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/info", r.URL.Path)
		_, err := fmt.Fprint(w, `{"version":"2.2.1","pid":42}`)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	rpc := newOpenCodeRPC(openCodeServiceRegistration{Version: openCodeNativeVersion, PID: 42, URL: server.URL + "/", Password: "private-credential"})
	t.Cleanup(rpc.close)
	_, err := rpc.probe(t.Context())
	require.NoError(t, err)
}

func TestOpenCodeNativeResponseClosesTheBodyAndEnforcesItsLimit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		size      int
		readError bool
		wantError bool
	}{
		{name: "empty"}, {name: "at limit", size: contracts.MaxMessageSize}, {name: "over limit", size: contracts.MaxMessageSize + 1, wantError: true}, {name: "read failure", readError: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			body := &openCodeProbeBody{Reader: io.LimitReader(infiniteOpenCodeProbeReader{}, int64(tc.size)), fail: tc.readError}
			raw, err := readOpenCodeResponse(&http.Response{Body: body})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Len(t, raw, tc.size)
			}
			assert.True(t, body.closed)
		})
	}
}

type infiniteOpenCodeProbeReader struct{}

func (infiniteOpenCodeProbeReader) Read(data []byte) (int, error) {
	for index := range data {
		data[index] = 'x'
	}
	return len(data), nil
}

type openCodeProbeBody struct {
	io.Reader
	fail, closed bool
}

func (body *openCodeProbeBody) Read(data []byte) (int, error) {
	if body.fail {
		return 0, assert.AnError
	}
	return body.Reader.Read(data)
}
func (body *openCodeProbeBody) Close() error { body.closed = true; return nil }

func TestOpenCodeNativeJSONKeepsTheExactRequestAndErrorEvidence(t *testing.T) {
	t.Parallel()
	const nativeFailure = `{ "error": {"message":"native private evidence", "statusCode":429} }`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		raw, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"msg_explicit","text":"payload 日本語"}`, string(raw))
		w.WriteHeader(http.StatusTooManyRequests)
		_, err = io.WriteString(w, nativeFailure)
		assert.NoError(t, err)
	}))
	t.Cleanup(server.Close)
	rpc := newOpenCodeRPC(openCodeServiceRegistration{URL: server.URL, Password: "private-credential"})
	t.Cleanup(rpc.close)
	_, err := rpc.json(t.Context(), http.MethodPost, "/api/session/ses_target/prompt", map[string]string{"id": "msg_explicit", "text": "payload 日本語"})
	var failure *openCodeHTTPError
	require.ErrorAs(t, err, &failure)
	assert.Equal(t, http.StatusTooManyRequests, failure.StatusCode)
	assert.Equal(t, nativeFailure, string(failure.Body))
	assert.NotContains(t, err.Error(), "native private evidence")
	assert.NotContains(t, err.Error(), "private-credential")
}

func TestOpenCodeNativeJSONHandlesEmptyAdministrativeAndMalformedResponses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		status int
		body   string
		valid  bool
	}{
		{name: "administrative", status: 204, valid: true},
		{name: "data", status: 200, body: `{"data":{"id":"native"}}`, valid: true},
		{name: "empty success", status: 200},
		{name: "malformed", status: 200, body: `{`},
		{name: "trailing", status: 200, body: `{} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, err := io.WriteString(w, tc.body)
				assert.NoError(t, err)
			}))
			t.Cleanup(server.Close)
			rpc := newOpenCodeRPC(openCodeServiceRegistration{URL: server.URL})
			t.Cleanup(rpc.close)
			raw, err := rpc.json(t.Context(), http.MethodGet, "/api/session", nil)
			if tc.valid {
				require.NoError(t, err)
				assert.Equal(t, tc.body, string(raw))
			} else {
				require.Error(t, err)
			}
		})
	}
}
