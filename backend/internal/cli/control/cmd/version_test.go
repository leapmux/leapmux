package cmd

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
)

// clearRemoteEnv blanks every LEAPMUX_CONTROL_* env var a worker spawn emits,
// plus LEAPMUX_HUB. A test that runs inside a LeapMux tab would otherwise
// resolve "self" and the default worker from the tab that runs the test.
func clearRemoteEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LEAPMUX_HUB",
		"LEAPMUX_CONTROL_TAB_ID",
		"LEAPMUX_CONTROL_TAB_TYPE",
		"LEAPMUX_CONTROL_TERMINAL_ID",
		"LEAPMUX_CONTROL_WORKING_DIR",
		"LEAPMUX_CONTROL_AGENT_PROVIDER",
		"LEAPMUX_CONTROL_WORKER_ID",
		"LEAPMUX_CONTROL_WORKSPACE_ID",
		"LEAPMUX_CONTROL_TILE_ID",
		"LEAPMUX_CONTROL_USER_ID",
	} {
		t.Setenv(key, "")
	}
}

// A build that another binary reports prints with the key names of this
// binary's own build, so a script reads one spelling.
func TestBuildInfoOfUsesTheKeysOfTheCLIBuild(t *testing.T) {
	want, err := json.Marshal(cliBuildInfo())
	require.NoError(t, err)
	got, err := json.Marshal(buildInfoOf(&leapmuxv1.BuildInfo{Version: "1", CommitHash: "c", CommitTime: "t", Branch: "b", BuildTime: "bt"}))
	require.NoError(t, err)
	var wantKeys, gotKeys map[string]any
	require.NoError(t, json.Unmarshal(want, &wantKeys))
	require.NoError(t, json.Unmarshal(got, &gotKeys))
	for key := range wantKeys {
		assert.Containsf(t, gotKeys, key, "the key %q of `control version`", key)
	}
}

// The formatted line describes the build that the row renders, not this binary.
func TestBuildInfoOfFormatsTheReportedBuild(t *testing.T) {
	got := buildInfoOf(&leapmuxv1.BuildInfo{Version: "9.9.9", CommitHash: "abc1234"})
	assert.Equal(t, "9.9.9 · abc1234", got.Formatted)
	assert.Empty(t, buildInfoOf(nil).Formatted, "a nil build formats as nothing")
}

// TestRunVersion_NoHubPrintsCLIOnly is the happy-path case when no hub is
// selected: the envelope's data carries only the cli fields, never a hub
// key.
func TestRunVersion_NoHubPrintsCLIOnly(t *testing.T) {
	clearRemoteEnv(t)
	t.Setenv("LEAPMUX_CONTROL_SOCK", "")
	t.Setenv("LEAPMUX_CONTROL_CONFIG_DIR", t.TempDir())
	env := struct{ Data map[string]any }{runControl(t, "version").Data(t)}
	require.Contains(t, env.Data, "cli")
	require.NotContains(t, env.Data, "hub", "no --hub means no hub field in envelope")
	require.NotContains(t, env.Data, "hub_error")

	cliBuild, ok := env.Data["cli"].(map[string]any)
	require.True(t, ok, "cli field must be an object")
	// The fields that a build always has; the rest are empty in a test
	// binary, and omitempty drops them.
	for _, k := range []string{"version", "formatted"} {
		assert.Contains(t, cliBuild, k)
	}
}

// TestRunVersion_WithHubFetchesAndIncludesHub asserts that when --hub
// points at a reachable /version endpoint, the envelope carries both
// fields, the "hub_error" field is absent, and the hub map echoes the
// JSON the server returned.
func TestRunVersion_WithHubFetchesAndIncludesHub(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", srv.URL).Data(t)}

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
// without `leapmux control version` failing.
func TestRunVersion_HubUnreachableSurfacesHubError(t *testing.T) {
	// 127.0.0.1:1 is reliably unreachable across CI, dev boxes, and
	// containers — the kernel rejects it with ECONNREFUSED rather
	// than dialing somewhere unrelated.
	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", "http://127.0.0.1:1").Data(t)}

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
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", srv.URL).Data(t)}

	require.Contains(t, env.Data, "hub_error")
	hubErr, _ := env.Data["hub_error"].(string)
	assert.Contains(t, hubErr, "404")
}

// TestRunVersion_HubMalformedJSONSurfacesHubError pins the
// decode-failure branch: a hub returning HTML or partial JSON should
// not crash the command; it should be reported under hub_error.
func TestRunVersion_HubMalformedJSONSurfacesHubError(t *testing.T) {
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not json"))
	}))
	defer srv.Close()

	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", srv.URL).Data(t)}
	assert.Contains(t, env.Data, "hub_error")
}

// TestRunVersion_TrailingSlashHubURL ensures the CLI normalises the
// hub URL when constructing /version, so users who paste the URL with
// a trailing slash (a common copy-paste outcome) don't get a 404 due
// to a doubled slash.
func TestRunVersion_TrailingSlashHubURL(t *testing.T) {
	hits := 0
	srv := hubtransporttest.NewServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/version" {
			hits++
			_ = json.NewEncoder(w).Encode(map[string]string{"version": "ok"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", srv.URL+"/").Data(t)}
	require.Contains(t, env.Data, "hub")
	assert.Equal(t, 1, hits)
}

// TestRunVersion_ReachesAHubOverASocket covers a hub addressed by `unix:` or
// `npipe:`.
//
// This command built its own http.Client, which cannot dial a socket, so it
// answered "unsupported protocol scheme" against a hub reached over its IPC
// listener -- the deployment `--local-listen` exists for. Routing it through
// hubtransport, as every other hub call goes, is what gives it the dialer.
func TestRunVersion_ReachesAHubOverASocket(t *testing.T) {
	socketURL := serveVersionOverSocket(t)

	env := struct{ Data map[string]any }{runControl(t, "version", "--hub", socketURL).Data(t)}
	require.NotContains(t, env.Data, "hub_error", "the socket hub must be reachable")
	require.Contains(t, env.Data, "hub")
	assert.Equal(t, "9.9.9", env.Data["hub"].(map[string]any)["version"])
}

// serveVersionOverSocket serves /version on a unix socket or a Windows named
// pipe, over both protocols, as the hub's own local listener does.
func serveVersionOverSocket(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": "9.9.9", "formatted": "9.9.9"})
	})
	return hubtransporttest.NewSocketServer(t, "cliversion", mux)
}
