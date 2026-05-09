package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli/remote"
)

// fakeCmdCtx is the minimal Ctx implementation the command runners need
// for help text. The cmd package's Ctx interface only requires Path and
// Description.
type fakeCmdCtx struct{}

func (fakeCmdCtx) Path() string        { return "remote version" }
func (fakeCmdCtx) Description() string { return "print versions" }

// withCapturedStdout swaps `remote.Out` for a buffer for the duration of
// fn. Returns the buffered bytes so tests can decode the JSON envelope.
func withCapturedStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := remote.Out
	remote.Out = buf
	defer func() { remote.Out = prev }()
	fn()
	return buf.Bytes()
}

// TestRunVersion_NoHubPrintsCLIOnly is the happy-path case when no
// `--hub` is provided: the envelope's data carries only the cli
// fields, never a hub key.
func TestRunVersion_NoHubPrintsCLIOnly(t *testing.T) {
	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, nil)
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Contains(t, env.Data, "cli")
	require.NotContains(t, env.Data, "hub", "no --hub means no hub field in envelope")
	require.NotContains(t, env.Data, "hub_error")

	cli, ok := env.Data["cli"].(map[string]any)
	require.True(t, ok, "cli field must be an object")
	// Ensure the contract fields exist; values are build-time defaults.
	for _, k := range []string{"version", "commit", "branch", "build_time", "formatted"} {
		assert.Contains(t, cli, k)
	}
}

// TestRunVersion_WithHubFetchesAndIncludesHub asserts that when --hub
// points at a reachable /version endpoint, the envelope carries both
// fields, the "hub_error" field is absent, and the hub map echoes the
// JSON the server returned.
func TestRunVersion_WithHubFetchesAndIncludesHub(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"version":    "9.9.9",
			"commit":     "abcdef",
			"branch":     "main",
			"build_time": "2026-05-10T00:00:00Z",
			"formatted":  "9.9.9 · abcdef",
		})
	}))
	defer srv.Close()

	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, []string{"--hub", srv.URL})
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))

	require.Contains(t, env.Data, "cli")
	require.Contains(t, env.Data, "hub")
	require.NotContains(t, env.Data, "hub_error")

	hub := env.Data["hub"].(map[string]any)
	assert.Equal(t, "9.9.9", hub["version"])
	assert.Equal(t, "abcdef", hub["commit"])
	assert.Equal(t, "main", hub["branch"])
	assert.Equal(t, "9.9.9 · abcdef", hub["formatted"])
}

// TestRunVersion_HubUnreachableSurfacesHubError covers the partial
// failure path: the CLI's own version still appears, and the network
// error is surfaced under "hub_error" instead of "hub" so scripts get
// a non-zero-but-still-parseable result.
//
// The hub_error path stays a *successful* envelope (no exit-1) so a
// user with stale credentials can still see "the CLI is at version X"
// without `leapmux remote version` failing.
func TestRunVersion_HubUnreachableSurfacesHubError(t *testing.T) {
	// 127.0.0.1:1 is reliably unreachable across CI, dev boxes, and
	// containers — the kernel rejects it with ECONNREFUSED rather
	// than dialing somewhere unrelated.
	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, []string{"--hub", "http://127.0.0.1:1"})
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))

	require.Contains(t, env.Data, "cli")
	require.NotContains(t, env.Data, "hub")
	require.Contains(t, env.Data, "hub_error")
	assert.NotEmpty(t, env.Data["hub_error"])
}

// TestRunVersion_HubNon200SurfacesHubError covers the case where the
// hub responds with a non-2xx status (e.g. an older hub that doesn't
// expose /version yet). Behaviour matches the unreachable case: the
// envelope carries "hub_error" instead of "hub".
func TestRunVersion_HubNon200SurfacesHubError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, []string{"--hub", srv.URL})
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))

	require.Contains(t, env.Data, "hub_error")
	hubErr, _ := env.Data["hub_error"].(string)
	assert.Contains(t, hubErr, "404")
}

// TestRunVersion_HubMalformedJSONSurfacesHubError pins the
// decode-failure branch: a hub returning HTML or partial JSON should
// not crash the command; it should be reported under hub_error.
func TestRunVersion_HubMalformedJSONSurfacesHubError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, []string{"--hub", srv.URL})
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	assert.Contains(t, env.Data, "hub_error")
}

// TestRunVersion_TrailingSlashHubURL ensures the CLI normalises the
// hub URL when constructing /version, so users who paste the URL with
// a trailing slash (a common copy-paste outcome) don't get a 404 due
// to a doubled slash.
func TestRunVersion_TrailingSlashHubURL(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			hits++
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "ok"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	out := withCapturedStdout(t, func() {
		err := RunVersion(fakeCmdCtx{}, []string{"--hub", srv.URL + "/"})
		require.NoError(t, err)
	})

	var env struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Contains(t, env.Data, "hub")
	assert.Equal(t, 1, hits)
}
