package antigravity

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	live, err := a.liveLSPort(t.Context())
	require.NoError(t, err)
	assert.Equal(t, port, live)
}

func TestAntigravityLastLogLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "acp.log")
	require.NoError(t, os.WriteFile(path, []byte("one\ntwo\nthree\n"), 0o600))
	assert.Equal(t, "two\nthree", lastLogLines(path, 2))
}
