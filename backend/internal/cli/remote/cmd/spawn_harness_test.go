package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
)

// Shared scaffolding for the "run this command from inside a
// worker-spawned agent" regression tests. A spawned agent talks to
// its worker over a per-agent unix socket (remoteipc), and the
// worker proxies `hub.*` calls onward using a delegation bearer --
// a bearer the hub restricts to `auth.delegationAllowedProcedures`.
// Any hub RPC the CLI makes outside that allowlist comes back denied,
// so these tests stand up a hub stub that mirrors the allowlist and
// assert the command still completes.

// shortIPCSocket builds a unix-socket path under os.TempDir() short
// enough to fit the platform's sun_path limit (~104 chars on macOS).
// t.TempDir() routinely produces directories that exceed it.
func shortIPCSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "lmx-spawn-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "ipc.sock")
}

// recordingHub is a remoteipc.HubClient stub that serves
// ListWorkspaces and refuses everything else, mimicking the hub's
// delegation-bearer allowlist (auth.delegationAllowedProcedures):
// a worker-spawned agent's bearer may list workspaces and little
// else. Notably absent from that allowlist is every
// WorkerManagementService procedure (GetWorker, ListWorkers), so a
// command that reaches for one inside a spawn gets PermissionDenied.
type recordingHub struct {
	// listWorkers, when non-nil, makes the stub answer ListWorkers
	// with these worker ids instead of denying it. A real delegation
	// bearer never gets that answer -- this stands in for the
	// session-bearer (laptop) transport, the only one where the CLI's
	// worker preflight can actually run, so a test can pin the
	// preflight's positive behaviour over the same harness.
	listWorkers []string

	mu      sync.Mutex
	methods []string
}

func (h *recordingHub) CallInner(_ context.Context, _ userid.UserID, _, method string, _ []byte) ([]byte, error) {
	h.mu.Lock()
	h.methods = append(h.methods, method)
	h.mu.Unlock()
	switch {
	case method == "ListWorkspaces":
		return proto.Marshal(&leapmuxv1.ListWorkspacesResponse{
			Workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", CreatedBy: "u-spawn", Title: "First"}},
		})
	case method == "ListWorkers" && h.listWorkers != nil:
		workers := make([]*leapmuxv1.Worker, 0, len(h.listWorkers))
		for _, id := range h.listWorkers {
			workers = append(workers, &leapmuxv1.Worker{Id: id})
		}
		return proto.Marshal(&leapmuxv1.ListWorkersResponse{Workers: workers})
	default:
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("delegation token cannot call this procedure: "+method))
	}
}

func (h *recordingHub) called() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.methods...)
}

// startSpawnIPC stands up the per-agent local-IPC server a worker
// gives every process it spawns and points LEAPMUX_REMOTE_SOCK /
// LEAPMUX_REMOTE_TOKEN at it. `hub` receives the `hub.*` calls the
// router proxies onward; `disp` answers `worker.*` calls routed back
// to this same worker (pass nil for commands that make none).
//
// Every LEAPMUX_REMOTE_*_ID variable is cleared first so the test
// declares exactly which ones the spawn injects; the worker's real
// injection list lives in remoteipc.EnvVars.
func startSpawnIPC(t *testing.T, hub *recordingHub, disp remoteipc.LocalDispatcher) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	clearRemoteEnv(t)

	sockURL := shortIPCSocket(t)
	rawToken := remoteipc.MintToken()
	srv, err := remoteipc.Listen(remoteipc.Options{
		SocketURL: sockURL,
		Token:     rawToken,
		TokenInfo: remoteipc.TokenInfo{
			UserID:      userid.MustNew("u-spawn"),
			WorkerID:    "worker-A",
			WorkspaceID: "ws-1",
			TabID:       "agent-1",
			TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		},
		Router: &remoteipc.Router{
			WorkerID:        "worker-A",
			UserID:          userid.MustNew("u-spawn"),
			WorkspaceIDs:    []string{"ws-1"},
			Hub:             hub,
			LocalDispatcher: disp,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_REMOTE_SOCK", sockURL)
	t.Setenv("LEAPMUX_REMOTE_TOKEN", rawToken)
}
