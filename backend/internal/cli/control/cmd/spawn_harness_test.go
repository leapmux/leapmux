package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
)

// Shared scaffolding for the "run this command from inside a
// worker-spawned agent" regression tests. A spawned agent talks to
// its worker over a per-agent unix socket (controlipc), and the
// worker proxies `hub.*` calls onward using a delegation bearer --
// a bearer the hub limits with `auth.CeilingFor(BearerKindDelegation)`.
// Any hub RPC needing a scope outside that ceiling comes back denied,
// so these tests stand up a hub stub that refuses everything but the
// workspace listing and assert the command still completes.
//
// The stub is DELIBERATELY narrower than the live ceiling, which admits
// worker:read. A test that mirrored the ceiling exactly would stop
// exercising the tolerance the moment the ceiling widened, and the
// tolerance is the behaviour under test: the CLI must complete whatever
// the bearer was granted.

// shortIPCSocket builds a unix-socket path under os.TempDir() short
// enough to fit the platform's sun_path limit (~104 chars on macOS).
// t.TempDir() routinely produces directories that exceed it.
func shortIPCSocket(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(os.TempDir(), "leapmux-spawn-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "ipc.sock")
}

// recordingHub is a controlipc.HubClient stub that serves
// ListWorkspaces and refuses everything else: the narrowest grant a
// worker-spawned agent's bearer could carry. A command that reaches for
// WorkerManagementService (GetWorker, ListWorkers) inside a spawn
// therefore gets PermissionDenied here, which is the case each test
// below asserts the command survives.
type recordingHub struct {
	// listWorkers, when non-nil, makes the stub answer ListWorkers
	// with these worker ids instead of denying it. A real delegation
	// bearer never gets that answer -- this stands in for the
	// session-bearer (laptop) transport, the only one where the CLI's
	// worker preflight can actually run, so a test can pin the
	// preflight's positive behaviour over the same harness.
	listWorkers []string

	// listWorkerPages, when non-nil, makes the stub answer ListWorkers one
	// page at a time: the cursor "" reads the first page, and the cursor
	// "page-N" reads page N. Each page but the last carries the next cursor.
	listWorkerPages [][]string

	// worker, when non-nil, answers GetWorker instead of the denial, the
	// way a session bearer that holds worker:read sees it.
	worker *leapmuxv1.Worker

	// materialized, when non-nil, answers GetMaterialized with this
	// snapshot. A command that bootstraps the CRDT (every `tab`
	// mutation) needs one before it can preflight its target, so a
	// test that drives such a command end to end supplies the tree
	// its tab lives in.
	materialized *leapmuxv1.UserMaterialized

	// locateTab, when non-nil, is the tab LocateTab answers with. It
	// stands alone from materialized, because a command can resolve a
	// tab without bootstrapping the CRDT: `tab rename` reads only
	// (tab id, tab type, worker id) and submits no op. A test of such
	// a command declares the tab here instead of supplying a tree it
	// never reads.
	locateTab *leapmuxv1.WorkspaceTab

	// relocatedTab, when non-nil, is what every LocateTab AFTER the first
	// answers with: a tab that moved between the resolver's lookup and the
	// command's own.
	relocatedTab *leapmuxv1.WorkspaceTab

	mu      sync.Mutex
	methods []string
	locates int
	// workerPageLimits records the page limit of each ListWorkers request
	// that listWorkerPages answers.
	workerPageLimits []int32
}

func (h *recordingHub) CallInner(_ context.Context, _ userid.UserID, method string, payload []byte) ([]byte, error) {
	h.mu.Lock()
	h.methods = append(h.methods, method)
	h.mu.Unlock()
	switch {
	case method == "ListWorkspaces":
		return proto.Marshal(&leapmuxv1.ListWorkspacesResponse{
			Workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", CreatedBy: "u-spawn", Title: "First"}},
		})
	// The three round-trips a `tab` mutation makes before it touches the tab:
	// the resolver's existence check and tab lookup, then the CRDT bootstrap.
	// All three answer only when a test supplies a snapshot, so a test that
	// wants the denial keeps getting it.
	case method == "GetWorkspace" && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.GetWorkspaceResponse{
			Workspace: &leapmuxv1.Workspace{Id: "ws-1", CreatedBy: "u-spawn", Title: "First"},
		})
	case method == "LocateTab" && (h.locateTab != nil || h.materialized != nil):
		h.mu.Lock()
		h.locates++
		relocated := h.locates > 1 && h.relocatedTab != nil
		h.mu.Unlock()
		if relocated {
			return proto.Marshal(&leapmuxv1.LocateTabResponse{Tab: h.relocatedTab})
		}
		tab := h.locateTab
		if tab == nil {
			tab = &leapmuxv1.WorkspaceTab{
				Tab:         &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"},
				TileId:      "root-1",
				WorkerId:    "worker-A",
				WorkspaceId: "ws-1",
			}
		}
		return proto.Marshal(&leapmuxv1.LocateTabResponse{Tab: tab})
	// The layout verbs resolve a TILE the same way `tab` verbs resolve a tab.
	// The snapshot already states which workspace the tile is in, so answering
	// from it keeps one source of truth for the tree in a test.
	case method == "LocateTile" && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.LocateTileResponse{WorkspaceId: "ws-1"})
	case method == "GetMaterialized" && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.GetMaterializedResponse{State: h.materialized})
	case method == "GetWorker" && h.worker != nil:
		return proto.Marshal(&leapmuxv1.GetWorkerResponse{Worker: h.worker})
	case method == "ListWorkers" && h.listWorkerPages != nil:
		return h.answerWorkerPage(payload)
	case method == "ListWorkers" && h.listWorkers != nil:
		workers := make([]*leapmuxv1.Worker, 0, len(h.listWorkers))
		for _, id := range h.listWorkers {
			workers = append(workers, &leapmuxv1.Worker{Id: id})
		}
		return proto.Marshal(&leapmuxv1.ListWorkersResponse{Workers: workers})
	default:
		return nil, connect.NewError(connect.CodePermissionDenied, "delegation token cannot call this procedure: "+method)
	}
}

// answerWorkerPage serves one page of listWorkerPages, chosen by the
// request's cursor.
func (h *recordingHub) answerWorkerPage(payload []byte) ([]byte, error) {
	var req leapmuxv1.ListWorkersRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	h.mu.Lock()
	h.workerPageLimits = append(h.workerPageLimits, req.GetPage().GetLimit())
	h.mu.Unlock()
	index := 0
	if cursor := req.GetPage().GetCursor(); cursor != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(cursor, "page-"))
		if err != nil || n <= 0 || n >= len(h.listWorkerPages) {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("bad cursor %q", cursor))
		}
		index = n
	}
	workers := make([]*leapmuxv1.Worker, 0, len(h.listWorkerPages[index]))
	for _, id := range h.listWorkerPages[index] {
		workers = append(workers, &leapmuxv1.Worker{Id: id})
	}
	next := ""
	if index+1 < len(h.listWorkerPages) {
		next = "page-" + strconv.Itoa(index+1)
	}
	return proto.Marshal(&leapmuxv1.ListWorkersResponse{Workers: workers, Page: &leapmuxv1.PageResponse{NextCursor: next}})
}

func (h *recordingHub) called() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.methods...)
}

func (h *recordingHub) requestedWorkerPageLimits() []int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int32(nil), h.workerPageLimits...)
}

// startSpawnIPC stands up the per-agent local-IPC server a worker
// gives every process it spawns and points LEAPMUX_CONTROL_SOCK /
// LEAPMUX_CONTROL_TOKEN at it. `hub` receives the `hub.*` calls the
// router proxies onward; `disp` answers `worker.*` calls routed back
// to this same worker (pass nil for commands that make none).
//
// Every LEAPMUX_CONTROL_*_ID variable is cleared first so the test
// declares exactly which ones the spawn injects; the worker's real
// injection list lives in controlipc.EnvVars.
func startSpawnIPC(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
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
			LocalDispatcher: disp,
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)
}
