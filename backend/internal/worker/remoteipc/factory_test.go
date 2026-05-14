package remoteipc_test

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/remoteipc"
	"github.com/leapmux/leapmux/internal/worker/service"
	"github.com/leapmux/leapmux/locallisten"
)

// fakeDelegationLifecycle records every Acquire/Release call so the
// factory tests can assert the lifetime contract: every spawn pairs
// one Acquire on construction with exactly one Release at cleanup.
type fakeDelegationLifecycle struct {
	mu       sync.Mutex
	acquires []scopedKey
	releases []scopedKey
}

type scopedKey struct {
	UserID, WorkspaceID, TabID string
	TabType                    int32
}

func (f *fakeDelegationLifecycle) Acquire(userID, workspaceID, tabID string, tabType int32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires = append(f.acquires, scopedKey{UserID: userID, WorkspaceID: workspaceID, TabID: tabID, TabType: tabType})
}

func (f *fakeDelegationLifecycle) Release(_ context.Context, userID, workspaceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, scopedKey{UserID: userID, WorkspaceID: workspaceID})
	return nil
}

func (f *fakeDelegationLifecycle) snapshot() (acq, rel []scopedKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scopedKey(nil), f.acquires...), append([]scopedKey(nil), f.releases...)
}

// withTempSocketRoot redirects the per-agent socket factory to a
// directly-rooted per-test tempdir. DefaultSocketPath nests one extra
// level below the configured runtime dir (`lmx-<wid8>/`); anchoring
// under `/tmp` here keeps the full socket path well under macOS's
// 104-byte sun_path limit even on long $TMPDIR layouts. On Windows
// DefaultSocketPath ignores XDG_RUNTIME_DIR and emits an `npipe:` URL,
// so this is a no-op there — the named-pipe namespace is process-wide
// and doesn't need a per-test prefix beyond the spawn-id baked into
// the pipe name.
func withTempSocketRoot(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	dir, err := os.MkdirTemp("/tmp", "lmx-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
}

// TestFactory_AgentSpawnAcquiresAndCleanupReleases pins the lifecycle
// contract: AgentSpawning must Acquire the (user, workspace) slot
// before the listener is in service, and the returned cleanup must
// Release it on close. Without this pairing, agent close wouldn't
// trigger the hub-side delegation revoke that the plan requires.
func TestFactory_AgentSpawnAcquiresAndCleanupReleases(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &remoteipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:      "user-1",
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "agent-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	acq, rel := lifecycle.snapshot()
	require.Len(t, acq, 1)
	assert.Equal(t, scopedKey{
		UserID: "user-1", WorkspaceID: "ws-1",
		TabID: "agent-1", TabType: int32(leapmuxv1.TabType_TAB_TYPE_AGENT),
	}, acq[0], "agent spawn must register its tab identity with the delegation slot")
	require.Len(t, rel, 0, "Release must not run before cleanup is invoked")
	assert.NotEmpty(t, envs, "spawn must produce LEAPMUX_REMOTE_* env vars")

	cleanup()
	_, rel = lifecycle.snapshot()
	require.Len(t, rel, 1)
	assert.Equal(t, scopedKey{UserID: "user-1", WorkspaceID: "ws-1"}, rel[0])
}

// TestFactory_TerminalSpawnAcquiresAndCleanupReleases pins the lifecycle
// contract for terminal spawns: every terminal Acquires the (user,
// workspace) delegation slot on construction and Releases it on
// cleanup. Mirrors the agent-side TestFactory_AgentSpawnAcquiresAndCleanupReleases.
func TestFactory_TerminalSpawnAcquiresAndCleanupReleases(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &remoteipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	envs, cleanup, err := f.TerminalSpawning(service.TerminalSpawnInfo{
		UserID:      "user-1",
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "term-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NotEmpty(t, envs, "terminal spawn must produce LEAPMUX_REMOTE_* env vars")

	acq, rel := lifecycle.snapshot()
	require.Len(t, acq, 1)
	assert.Equal(t, scopedKey{
		UserID: "user-1", WorkspaceID: "ws-1",
		TabID: "term-1", TabType: int32(leapmuxv1.TabType_TAB_TYPE_TERMINAL),
	}, acq[0], "terminal spawn must register its tab identity with the delegation slot")
	require.Len(t, rel, 0, "Release must not run before cleanup is invoked")

	cleanup()
	_, rel = lifecycle.snapshot()
	require.Len(t, rel, 1)
	assert.Equal(t, scopedKey{UserID: "user-1", WorkspaceID: "ws-1"}, rel[0])
}

// TestFactory_AgentSpawnAdvertisesWorkspaceScope pins the
// scope-shape contract independent of the now-removed cross-worker
// FS knob. The bearer's Whoami must report exactly the spawn's
// workspace_id; widening the scope here would let a delegated agent
// reach workspaces the user might own but never opted into for this
// spawn.
func TestFactory_AgentSpawnAdvertisesWorkspaceScope(t *testing.T) {
	withTempSocketRoot(t)
	f := &remoteipc.Factory{WorkerID: "worker-A"}
	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:      "user-1",
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "agent-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	scope := dialAndWhoami(t, envs)
	assert.Equal(t, []string{"ws-1"}, scope.GetWorkspaceIds())
}

// TestFactory_NilDelegationIsTolerated documents that the Delegation
// field is optional. Tests / minimal configurations that don't wire a
// crossworker.DelegationStore must still get a working spawn.
func TestFactory_NilDelegationIsTolerated(t *testing.T) {
	withTempSocketRoot(t)
	f := &remoteipc.Factory{WorkerID: "worker-A"}

	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:      "user-1",
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "agent-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, envs)
	cleanup() // must not panic
}

// dialAndWhoami parses LEAPMUX_REMOTE_SOCK / LEAPMUX_REMOTE_TOKEN
// from a freshly-spawned envs slice, dials the per-agent IPC server,
// and returns the Whoami response's Scope. Used by the
// CrossWorkerFSDefault tests to verify the runtime knob actually
// reaches the bearer (the alternative — peeking at the in-memory
// TokenStore — couples tests to internals that may move).
func dialAndWhoami(t *testing.T, envs []string) *leapmuxv1.RemoteScope {
	t.Helper()
	var sock, token string
	for _, e := range envs {
		switch {
		case strings.HasPrefix(e, "LEAPMUX_REMOTE_SOCK="):
			sock = strings.TrimPrefix(e, "LEAPMUX_REMOTE_SOCK=")
		case strings.HasPrefix(e, "LEAPMUX_REMOTE_TOKEN="):
			token = strings.TrimPrefix(e, "LEAPMUX_REMOTE_TOKEN=")
		}
	}
	require.NotEmpty(t, sock, "factory must emit LEAPMUX_REMOTE_SOCK")
	require.NotEmpty(t, token, "factory must emit LEAPMUX_REMOTE_TOKEN")

	dial, err := locallisten.Dialer(sock)
	require.NoError(t, err)
	transport := locallisten.NewLocalH2CTransport(dial)
	httpClient := &http.Client{
		Transport: &authHeaderInjector{token: token, base: transport},
		Timeout:   5 * time.Second,
	}
	client := leapmuxv1connect.NewRemoteIPCServiceClient(httpClient, "http://leapmux-remote", connect.WithGRPC())

	resp, err := client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)
	require.NotNil(t, resp.Msg.GetScope())
	return resp.Msg.GetScope()
}

// authHeaderInjector mirrors the production CLI's
// X-Leapmux-Token-attaching transport. Kept private to factory_test so
// it doesn't drift from server_test's injectAuthHeader (different
// names, same behaviour — keeping them per-file avoids cross-test
// coupling on a small helper).
type authHeaderInjector struct {
	token string
	base  http.RoundTripper
}

func (i *authHeaderInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(remoteipc.AuthHeader, i.token)
	return i.base.RoundTrip(req)
}

// TestFactory_ConcurrentSpawnsRefcountCorrectly exercises the case
// where two agent spawns and a terminal spawn share one
// (user, workspace) pair: every Acquire must pair with exactly one
// Release after each cleanup runs. Without the lifecycle wiring, a
// stray Release / missing Acquire would corrupt the refcount and
// either leak the delegation row or revoke it prematurely.
func TestFactory_ConcurrentSpawnsRefcountCorrectly(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &remoteipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	var spawned int32
	var cleanups []func()
	for i := 0; i < 3; i++ {
		envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
			UserID:      "user-1",
			WorkspaceID: "ws-1",
			WorkerID:    "worker-A",
			TabID:       "agent-" + string(rune('A'+i)),
		})
		require.NoError(t, err)
		require.NotEmpty(t, envs)
		atomic.AddInt32(&spawned, 1)
		cleanups = append(cleanups, cleanup)
	}

	// One terminal in the same scope.
	envs, termCleanup, err := f.TerminalSpawning(service.TerminalSpawnInfo{
		UserID:      "user-1",
		WorkspaceID: "ws-1",
		WorkerID:    "worker-A",
		TabID:       "term-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, envs)
	cleanups = append(cleanups, termCleanup)

	acq, rel := lifecycle.snapshot()
	assert.Len(t, acq, 4, "every spawn must Acquire")
	assert.Len(t, rel, 0, "Release must wait for cleanup")

	for _, c := range cleanups {
		c()
	}
	_, rel = lifecycle.snapshot()
	assert.Len(t, rel, 4, "every cleanup must Release exactly once")
}
