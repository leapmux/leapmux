package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type antigravityACPTestWriter func([]byte) (int, error)

func (write antigravityACPTestWriter) Write(data []byte) (int, error) { return write(data) }

func TestAntigravityACPBootstrapLeavesCascadesToNativeServer(t *testing.T) {
	a, _ := testAgent(t)
	requests := make(chan agenttest.RecordedRequest, 4)
	a.SetStdinForTest(agenttest.NopStdin(antigravityACPTestWriter(func(data []byte) (int, error) {
		var request struct {
			ID     int64          `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(data, &request); err != nil {
			return 0, err
		}
		requests <- agenttest.RecordedRequest{Method: request.Method, Params: request.Params}
		reply := agenttest.JSONRPCResponse(request.ID, agenttest.RPCReply{Result: json.RawMessage(`{"sessions":[]}`)})
		if !a.Deliver(request.ID, reply) {
			return 0, errors.New("no request waits for the test ACP reply")
		}
		return len(data), nil
	})))

	require.NoError(t, a.bootstrapBackend(testutil.DeadlineContext(t)))

	assert.Equal(t, agenttest.RecordedRequest{
		Method: "session/list", Params: map[string]any{"cwd": a.workingDir},
	}, receiveTestValue(t, requests))
	assert.Empty(t, requests)
}

func TestAntigravityPortPatternMatchesRealLogLine(t *testing.T) {
	line := "2026-11-04T12:00:00Z Language server listening on random port at 54321 for HTTP"
	match := antigravityPortPattern.FindStringSubmatch(line)
	require.NotNil(t, match)
	assert.Equal(t, "54321", match[1])
	assert.Nil(t, antigravityPortPattern.FindStringSubmatch("Language server listening on random port at 54321 for HTTPS (gRPC)"))
}

func TestAntigravityLiveLSPortUsesLastHTTPLine(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	var port int
	_, err := fmt.Sscanf(server.URL, "http://127.0.0.1:%d", &port)
	require.NoError(t, err)
	a, _ := testAgent(t)
	a.logPath = filepath.Join(t.TempDir(), "acp.log")
	content := fmt.Sprintf("Language server listening on random port at 1 for HTTP\nLanguage server listening on random port at 2 for HTTPS (gRPC)\nLanguage server listening on random port at %d for HTTP\n", port)
	require.NoError(t, os.WriteFile(a.logPath, []byte(content), 0o600))
	live, err := a.liveLSEndpoint(t.Context())
	require.NoError(t, err)
	defer live.Close()
	assert.Equal(t, server.URL, live.BaseURL())
}

func TestAntigravityLastLogLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.log")
	require.NoError(t, os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600))
	assert.Equal(t, "two\nthree", lastLogLines(path, 2))
}

func TestAntigravityDiscoveryRetainsVerifiedConnection(t *testing.T) {
	addresses := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { addresses <- r.RemoteAddr; _, _ = w.Write([]byte(`{}`)) }))
	defer server.Close()
	var port int
	_, err := fmt.Sscanf(server.URL, "http://127.0.0.1:%d", &port)
	require.NoError(t, err)
	a, _ := testAgent(t)
	a.logPath = filepath.Join(t.TempDir(), "acp.log")
	require.NoError(t, os.WriteFile(a.logPath, fmt.Appendf(nil, "Language server listening on random port at %d for HTTP\n", port), 0o600))
	require.NoError(t, a.discoverLSPort(t.Context(), time.Second))
	defer a.rpc.endpoint.Close()
	require.NoError(t, a.rpc.status(t.Context()))
	assert.Equal(t, receiveTestValue(t, addresses), receiveTestValue(t, addresses))
}
