package lifecycle

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestControlRejectsInvalidEnvelopesBeforeShutdown(t *testing.T) {
	const body = `{"instance_id":"instance","attempt_id":"attempt"}`
	for _, test := range []struct {
		name, method, path, authorization, body string

		code int
	}{
		{"oversized trailing whitespace", http.MethodPost, controlStopPath, "Bearer secret", body + strings.Repeat(" ", controlMaxBody), http.StatusBadRequest},
		{"second JSON value", http.MethodPost, controlStopPath, "Bearer secret", body + `{}`, http.StatusBadRequest},
		{"trailing non-JSON data", http.MethodPost, controlStopPath, "Bearer secret", body + "x", http.StatusBadRequest},
		{"stop GET", http.MethodGet, controlStopPath, "Bearer secret", body, http.StatusMethodNotAllowed},
		{"status POST", http.MethodPost, controlStatusPath, "Bearer secret", "", http.StatusMethodNotAllowed},
		{"bare token", http.MethodPost, controlStopPath, "secret", body, http.StatusUnauthorized},
		{"extra bearer value", http.MethodPost, controlStopPath, "Bearer secret extra", body, http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			reporter := &stopCountingReporter{}
			server := &controlServer{reporter: reporter, token: "secret", liveID: RuntimeRecord{InstanceID: "instance", AttemptID: "attempt"}}
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", test.authorization)
			response := httptest.NewRecorder()
			if test.path == controlStopPath {
				server.handleStop(response, request)
			} else {
				server.handleStatus(response, request)
			}
			assert.Equal(t, test.code, response.Code, response.Body.String())
			assert.Zero(t, reporter.stops.Load())
		})
	}
}

// stopCountingReporter counts the shutdown calls a stop request causes;
// every refusal in this file must leave it at zero.
type stopCountingReporter struct {
	runtime *Runtime
	stops   atomic.Int32
}

func (s *stopCountingReporter) Status() Status {
	if s.runtime == nil {
		return Status{}
	}
	return s.runtime.Status()
}

func (s *stopCountingReporter) RequestStop() {
	s.stops.Add(1)
	if s.runtime != nil {
		s.runtime.RequestStop()
	}
}

// The endpoint authenticates every request, limits every body, and
// matches the stop request's instance and attempt identities against
// the live runtime before any shutdown call happens. A stale client --
// one that inspected an earlier attempt -- is refused with zero
// shutdown calls.
func TestControlAuthenticationAndGeneration(t *testing.T) {
	runtimeEnv(t)
	root := t.TempDir()
	r := startTestRuntime(t, RoleWorker, root)
	reporter := &stopCountingReporter{runtime: r}
	token, err := ReadControlToken(r.Paths().ControlToken)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	// Swap the running server's reporter for the counting one through a
	// direct client: the served endpoint keeps its own reporter; this
	// test drives the HANDLER shapes through a second endpoint whose
	// reporter it owns.
	record := r.Record()
	record.ControlURL = ControlURL(InstanceID(hashID(record.InstanceID, "test-auth")))
	require.NotEqual(t, r.Record().ControlURL, record.ControlURL)
	server, err := serveControl(record, token, reporter)
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })

	client, err := newControlClient(record.ControlURL, token)
	require.NoError(t, err)
	ctx := context.Background()

	t.Run("a missing bearer is refused", func(t *testing.T) {
		unauthenticated, err := newControlClient(record.ControlURL, "")
		require.NoError(t, err)
		unauthenticated.token = ""
		_, err = unauthenticated.Status(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("a wrong bearer is refused", func(t *testing.T) {
		wrong, err := newControlClient(record.ControlURL, "wrong-token-value")
		require.NoError(t, err)
		_, err = wrong.Status(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "401")
	})

	t.Run("a stale attempt identity is refused with zero shutdown calls", func(t *testing.T) {
		_, err := client.RequestStop(ctx, StopRequest{InstanceID: record.InstanceID, AttemptID: "a9a0425886d148f68a8f94716ed47a78"})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "409")
		assert.Zero(t, reporter.stops.Load())
	})

	t.Run("a stale instance identity is refused", func(t *testing.T) {
		_, err := client.RequestStop(ctx, StopRequest{InstanceID: "0000000000000000", AttemptID: record.AttemptID})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "409")
		assert.Zero(t, reporter.stops.Load())
	})

	t.Run("an oversized body is refused", func(t *testing.T) {
		var resp StopResponse
		err := postRaw(ctx, client, strings.Repeat("x", controlMaxBody+1024), &resp)
		require.Error(t, err)
		assert.Zero(t, reporter.stops.Load())
	})

	t.Run("the live identities are accepted", func(t *testing.T) {
		resp, err := client.RequestStop(ctx, StopRequest{InstanceID: record.InstanceID, AttemptID: record.AttemptID})
		require.NoError(t, err)
		assert.True(t, resp.Accepted)
		assert.Equal(t, record.AttemptID, resp.AttemptID)
		assert.Equal(t, int32(1), reporter.stops.Load())
		assert.True(t, r.StopRequested())
	})

	t.Run("a repeated stop during the shutdown is accepted too", func(t *testing.T) {
		resp, err := client.RequestStop(ctx, StopRequest{InstanceID: record.InstanceID, AttemptID: record.AttemptID})
		require.NoError(t, err)
		assert.True(t, resp.Accepted)
		assert.Equal(t, record.AttemptID, resp.AttemptID)
		assert.Equal(t, int32(2), reporter.stops.Load())
		assert.True(t, r.StopRequested())
	})

	t.Run("status carries the model's shape", func(t *testing.T) {
		status, err := client.Status(ctx)
		require.NoError(t, err)
		assert.Equal(t, new(record.InstanceID), status.InstanceID)
		assert.Equal(t, new("worker"), status.Mode)
		assert.Equal(t, new(ExecutionForeground), status.Execution)
		assert.Equal(t, RegistrationNotApplicable, status.Registration.State)
		assert.Equal(t, ConnectionNotApplicable, status.Connection.State)
		assert.Empty(t, status.Diagnostics)
		raw, err := json.Marshal(status)
		require.NoError(t, err)
		assert.Contains(t, string(raw), `"diagnostics":[]`, "an absent diagnostic list is empty, not null")
	})
}

// postRaw posts an arbitrary body the handler must limit.
func postRaw(ctx context.Context, c *controlClient, body string, out *StopResponse) error {
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://leapmux.local"+controlStopPath, bytes.NewReader([]byte(body)))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return &httpStatusError{code: resp.StatusCode}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// httpStatusError carries a refusal's status for the assertions above.
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("refused with HTTP %d", e.code)
}
