package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/hubtransport/hubtransporttest"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/util/version"
)

// clearRemoteEnv blanks LEAPMUX_HUB and the LEAPMUX_CONTROL_* variables that
// give a command its context. A test that runs inside a LeapMux tab would
// otherwise resolve "self" and the default worker from the tab that runs the
// test. It leaves the socket and its token alone: each test sets or blanks
// them itself.
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
	origValue, origCommit, origCommitTime, origBranch, origBuildTime :=
		version.Value, version.CommitHash, version.CommitTime, version.Branch, version.BuildTime
	t.Cleanup(func() {
		version.Value = origValue
		version.CommitHash = origCommit
		version.CommitTime = origCommitTime
		version.Branch = origBranch
		version.BuildTime = origBuildTime
	})
	version.Value = "1.2.3"
	version.CommitHash = "deadbeef"
	version.CommitTime = "2026-05-01T12:00:00Z"
	version.Branch = "feat/test"
	version.BuildTime = "2026-05-02T00:00:00Z"

	cliInfo := cliBuildInfo()
	assert.Equal(t, "2026-05-01T12:00:00Z", cliInfo.CommitTime)

	want, err := json.Marshal(cliInfo)
	require.NoError(t, err)
	got, err := json.Marshal(buildInfoOf(&leapmuxv1.BuildInfo{
		Version:    "1.2.3",
		CommitHash: "deadbeef",
		CommitTime: "2026-05-01T12:00:00Z",
		Branch:     "feat/test",
		BuildTime:  "2026-05-02T00:00:00Z",
	}))
	require.NoError(t, err)
	var wantMap, gotMap map[string]any
	require.NoError(t, json.Unmarshal(want, &wantMap))
	require.NoError(t, json.Unmarshal(got, &gotMap))
	assert.Equal(t, wantMap, gotMap)
	for _, key := range []string{"version", "commit", "commit_time", "branch", "build_time", "formatted"} {
		assert.Containsf(t, wantMap, key, "cliBuildInfo must emit %q", key)
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
// a parseable result while exiting zero.
//
// The hub_error path stays a successful envelope (exit 0) so a user
// with an unreachable hub can still see the CLI's own version without
// `leapmux version` failing.
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

func TestRunVersion_TabIPCProbesHubAndWorkerConcurrently(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	clearRemoteEnv(t)

	hubArrived := make(chan struct{})
	workerArrived := make(chan struct{})

	hub := &versionBarrierHub{
		onCall: func(ctx context.Context, proc string) ([]byte, error) {
			if proc != leapmuxv1connect.SystemServiceGetSystemInfoProcedure {
				return nil, nil
			}
			close(hubArrived)
			select {
			case <-workerArrived:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return proto.Marshal(&leapmuxv1.GetSystemInfoResponse{
				Build: &leapmuxv1.BuildInfo{Version: "2.0.0", CommitHash: "hubhash"},
			})
		},
	}
	worker := &versionBarrierWorker{
		onDispatch: func(ctx context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
			if !strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo") {
				_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
				return
			}
			close(workerArrived)
			select {
			case <-hubArrived:
			case <-ctx.Done():
				_ = rw.SendError(4, ctx.Err().Error())
				return
			}
			writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{
				Build: &leapmuxv1.BuildInfo{Version: "2.0.1", CommitHash: "wkhash"},
			})
		},
	}

	sockURL := shortIPCSocket(t)
	rawToken := controlipc.MintToken()
	srv, err := controlipc.Listen(controlipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: controlipc.TokenInfo{
			UserID:   userid.MustNew("u-spawn"),
			WorkerID: "worker-A",
			TabID:    "agent-1",
			TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
		Router: &controlipc.Router{
			WorkerID:        "worker-A",
			UserID:          userid.MustNew("u-spawn"),
			Hub:             hub,
			LocalDispatcher: worker,
			Sources:         spawnTabSources{},
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })
	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)

	data := runControl(t, "version").Data(t)
	require.Contains(t, data, "hub")
	require.Contains(t, data, "worker")
	assert.NotContains(t, data, "hub_error")
	assert.NotContains(t, data, "worker_error")
	assert.Equal(t, "2.0.0", data["hub"].(map[string]any)["version"])
	assert.Equal(t, "2.0.1", data["worker"].(map[string]any)["version"])
}

func TestRunVersion_TabIPCPartialPeerFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}

	startVersionIPC := func(t *testing.T, hub *versionBarrierHub, worker *versionBarrierWorker) {
		t.Helper()
		clearRemoteEnv(t)
		sockURL := shortIPCSocket(t)
		rawToken := controlipc.MintToken()
		srv, err := controlipc.Listen(controlipc.Options{
			SocketURL: sockURL,
			Token:     rawToken,
			TokenInfo: controlipc.TokenInfo{
				UserID:   userid.MustNew("u-spawn"),
				WorkerID: "worker-A",
				TabID:    "agent-1",
				TabType:  leapmuxv1.TabType_TAB_TYPE_AGENT,
			},
			Router: &controlipc.Router{
				WorkerID:        "worker-A",
				UserID:          userid.MustNew("u-spawn"),
				Hub:             hub,
				LocalDispatcher: worker,
				Sources:         spawnTabSources{},
			},
		})
		require.NoError(t, err)
		t.Cleanup(func() { _ = srv.Close() })
		t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
		t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)
	}

	t.Run("hub failure keeps worker build and exits ok", func(t *testing.T) {
		hub := &versionBarrierHub{
			onCall: func(_ context.Context, proc string) ([]byte, error) {
				if proc != leapmuxv1connect.SystemServiceGetSystemInfoProcedure {
					return nil, nil
				}
				return nil, connect.NewError(connect.CodeUnavailable, "hub offline")
			},
		}
		worker := &versionBarrierWorker{
			onDispatch: func(_ context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
				if !strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo") {
					_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
					return
				}
				writeProtoResponse(rw, &leapmuxv1.GetWorkerSystemInfoResponse{
					Build: &leapmuxv1.BuildInfo{Version: "2.0.1", CommitHash: "wkhash"},
				})
			},
		}
		startVersionIPC(t, hub, worker)

		out := runControl(t, "version")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		data := out.Data(t)
		require.Contains(t, data, "cli")
		assert.NotContains(t, data, "hub")
		assert.Contains(t, data["hub_error"], "hub offline")
		require.Contains(t, data, "worker")
		assert.NotContains(t, data, "worker_error")
		assert.Equal(t, "2.0.1", data["worker"].(map[string]any)["version"])
	})

	t.Run("worker failure keeps hub build and exits ok", func(t *testing.T) {
		hub := &versionBarrierHub{
			onCall: func(_ context.Context, proc string) ([]byte, error) {
				if proc != leapmuxv1connect.SystemServiceGetSystemInfoProcedure {
					return nil, nil
				}
				return proto.Marshal(&leapmuxv1.GetSystemInfoResponse{
					Build: &leapmuxv1.BuildInfo{Version: "2.0.0", CommitHash: "hubhash"},
				})
			},
		}
		worker := &versionBarrierWorker{
			onDispatch: func(_ context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
				if !strings.HasSuffix(req.GetMethod(), "/GetWorkerSystemInfo") {
					_ = rw.SendError(12, "unimplemented: "+req.GetMethod())
					return
				}
				_ = rw.SendError(14, "worker unavailable")
			},
		}
		startVersionIPC(t, hub, worker)

		out := runControl(t, "version")
		require.Equal(t, cli.ExitOK, out.Code, out.Stderr)
		data := out.Data(t)
		require.Contains(t, data, "cli")
		require.Contains(t, data, "hub")
		assert.NotContains(t, data, "hub_error")
		assert.Equal(t, "2.0.0", data["hub"].(map[string]any)["version"])
		assert.NotContains(t, data, "worker")
		assert.Contains(t, data["worker_error"], "worker unavailable")
	})
}

type versionBarrierHub struct {
	onCall func(ctx context.Context, proc string) ([]byte, error)
}

func (h *versionBarrierHub) CallInner(ctx context.Context, _ userid.UserID, procedure string, _ []byte) ([]byte, error) {
	return h.onCall(ctx, procedure)
}

type versionBarrierWorker struct {
	onDispatch func(ctx context.Context, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter)
}

func (w *versionBarrierWorker) DispatchWith(ctx context.Context, _ channel.Caller, req *leapmuxv1.InnerRpcRequest, rw channel.ResponseWriter) {
	w.onDispatch(ctx, req, rw)
}
