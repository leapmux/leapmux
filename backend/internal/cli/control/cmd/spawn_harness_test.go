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
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	"github.com/leapmux/leapmux/channelwire"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
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
	// listWorkerPages, when non-nil, makes the stub answer ListWorkers one
	// page at a time: the cursor "" reads the first page, and the cursor
	// "page-N" reads page N. Each page but the last carries the next cursor.
	listWorkerPages [][]string

	// stallLastWorkerPage makes the last page of listWorkerPages answer its
	// own cursor as the next one, the way a hub with a pagination fault
	// would, instead of the empty cursor that ends the listing.
	stallLastWorkerPage bool

	// workers, when non-nil, makes the stub answer GetWorker instead of
	// denying it, the way the hub answers a bearer that holds worker:read:
	// the row whose id the request carries, and NotFound for any other id.
	// The CLI's worker preflight asks GetWorker, so a test can pin the
	// preflight's positive behaviour over the same harness.
	workers []*leapmuxv1.Worker

	// getWorkerErr, when non-nil, is what GetWorker answers instead of a
	// row or the denial: a failure of the lookup itself.
	getWorkerErr error

	// refuseChannelOpen, when non-nil, is what the hub of a hubChannelHarness
	// answers to a channel open, the way a hub answers for an offline worker.
	refuseChannelOpen error

	// unlocatableTab makes LocateTab answer NotFound even when materialized
	// is set, the way the hub answers for a quake terminal, which has no CRDT
	// tab.
	unlocatableTab bool

	// onSubmitOps, when non-nil, answers SubmitOps. A test that needs the
	// CRDT write to fail in a particular way, or to block, supplies it.
	onSubmitOps func(ctx context.Context) ([]byte, error)

	// onGetMaterialized, when non-nil, answers GetMaterialized instead of the
	// materialized snapshot. A test that needs the CRDT bootstrap to fail, or to
	// stay in flight, supplies it.
	onGetMaterialized func(ctx context.Context) ([]byte, error)

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
	// answers with: a tab that moved after the first lookup. A command that
	// looks the tab up once never reads it.
	relocatedTab *leapmuxv1.WorkspaceTab

	mu sync.Mutex
	// procedures records the procedure path of each call, in order.
	procedures []string
	locates    int
	// workerPageLimits records the page limit of each ListWorkers request
	// that listWorkerPages answers.
	workerPageLimits []int32
}

func (h *recordingHub) CallInner(ctx context.Context, _ userid.UserID, procedure string, payload []byte) ([]byte, error) {
	h.mu.Lock()
	h.procedures = append(h.procedures, procedure)
	h.mu.Unlock()
	switch {
	case procedure == leapmuxv1connect.WorkspaceServiceListWorkspacesProcedure:
		return proto.Marshal(&leapmuxv1.ListWorkspacesResponse{
			Workspaces: []*leapmuxv1.Workspace{{Id: "ws-1", CreatedBy: "u-spawn", Title: "First"}},
		})
	// The three round-trips a `tab` mutation makes before it touches the tab:
	// the resolver's existence check and tab lookup, then the CRDT bootstrap.
	// All three answer only when a test supplies a snapshot, so a test that
	// wants the denial keeps getting it.
	case procedure == leapmuxv1connect.WorkspaceServiceGetWorkspaceProcedure && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.GetWorkspaceResponse{
			Workspace: &leapmuxv1.Workspace{Id: "ws-1", CreatedBy: "u-spawn", Title: "First"},
		})
	case procedure == leapmuxv1connect.WorkspaceServiceLocateTabProcedure && h.unlocatableTab:
		return nil, connect.NewError(connect.CodeNotFound, "tab not found")
	case procedure == leapmuxv1connect.WorkspaceServiceLocateTabProcedure && (h.locateTab != nil || h.materialized != nil):
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
	case procedure == leapmuxv1connect.WorkspaceServiceLocateTileProcedure && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.LocateTileResponse{WorkspaceId: "ws-1"})
	case procedure == leapmuxv1connect.UserCRDTGetMaterializedProcedure && h.onGetMaterialized != nil:
		return h.onGetMaterialized(ctx)
	case procedure == leapmuxv1connect.UserCRDTGetMaterializedProcedure && h.materialized != nil:
		return proto.Marshal(&leapmuxv1.GetMaterializedResponse{State: h.materialized})
	case procedure == leapmuxv1connect.UserCRDTSubmitOpsProcedure && h.onSubmitOps != nil:
		return h.onSubmitOps(ctx)
	case procedure == leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure && h.getWorkerErr != nil:
		return nil, h.getWorkerErr
	case procedure == leapmuxv1connect.WorkerManagementServiceGetWorkerProcedure && h.workers != nil:
		return h.answerGetWorker(payload)
	case procedure == leapmuxv1connect.WorkerManagementServiceListWorkersProcedure && h.listWorkerPages != nil:
		return h.answerWorkerPage(payload)
	default:
		return nil, connect.NewError(connect.CodePermissionDenied, "delegation token cannot call this procedure: "+procedure)
	}
}

// answerGetWorker serves the row of workers whose id the request carries, and
// NotFound for any other id, as the hub's GetWorker does.
func (h *recordingHub) answerGetWorker(payload []byte) ([]byte, error) {
	var req leapmuxv1.GetWorkerRequest
	if err := proto.Unmarshal(payload, &req); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err.Error())
	}
	for _, w := range h.workers {
		if w.GetId() == req.GetWorkerId() {
			return proto.Marshal(&leapmuxv1.GetWorkerResponse{Worker: w})
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, "worker not found")
}

// workerRows builds the minimal GetWorker rows for ids, for a test that needs
// the preflight to find a worker and reads nothing else from the row.
func workerRows(ids ...string) []*leapmuxv1.Worker {
	rows := make([]*leapmuxv1.Worker, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, &leapmuxv1.Worker{Id: id})
	}
	return rows
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
	switch {
	case index+1 < len(h.listWorkerPages):
		next = "page-" + strconv.Itoa(index+1)
	case h.stallLastWorkerPage:
		next = req.GetPage().GetCursor()
	}
	return proto.Marshal(&leapmuxv1.ListWorkersResponse{Workers: workers, Page: &leapmuxv1.PageResponse{NextCursor: next}})
}

func (h *recordingHub) called() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.procedures...)
}

func (h *recordingHub) requestedWorkerPageLimits() []int32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]int32(nil), h.workerPageLimits...)
}

// writeProtoResponse answers one inner RPC of a stub dispatcher with resp.
func writeProtoResponse(w channel.ResponseWriter, resp proto.Message) {
	payload, err := proto.Marshal(resp)
	if err != nil {
		_ = w.SendError(int32(codes.Internal), err.Error())
		return
	}
	_ = w.SendResponse(channelwire.NewPayloadResponse(payload))
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
//
// Each configure function adjusts the router before the server starts, for a
// test that needs a part of it the arguments do not state.
func startSpawnIPC(t *testing.T, hub *recordingHub, disp controlipc.LocalDispatcher, configure ...func(*controlipc.Router)) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses unix sockets; npipe variant exercised elsewhere")
	}
	clearRemoteEnv(t)

	router := &controlipc.Router{
		WorkerID:        "worker-A",
		UserID:          userid.MustNew("u-spawn"),
		Hub:             hub,
		LocalDispatcher: disp,
	}
	for _, apply := range configure {
		apply(router)
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
		Router: router,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = srv.Close() })

	t.Setenv("LEAPMUX_CONTROL_SOCK", sockURL)
	t.Setenv("LEAPMUX_CONTROL_TOKEN", rawToken)
}
