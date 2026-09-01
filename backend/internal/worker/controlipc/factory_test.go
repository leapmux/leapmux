package controlipc_test

import (
	"context"
	"net/http"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/controlipc"
	"github.com/leapmux/leapmux/internal/worker/service"
)

// fakeDelegationLifecycle records every Acquire/Release call so the
// factory tests can assert the lifetime contract: every spawn pairs
// one Acquire on construction with exactly one Release at cleanup.
type fakeDelegationLifecycle struct {
	mu       sync.Mutex
	acquires []scopedKey
	releases []scopedKey
}

// scopedKey records who a lifecycle call named. It is user-only: the mint's
// provenance tab is read from the worker's live inventory, so Acquire/Release
// carry no tab and only balance a per-user refcount.
type scopedKey struct {
	UserID string
}

func (f *fakeDelegationLifecycle) Acquire(userID userid.UserID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquires = append(f.acquires, scopedKey{UserID: userID.String()})
}

func (f *fakeDelegationLifecycle) Release(_ context.Context, caller userid.UserID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = append(f.releases, scopedKey{UserID: caller.String()})
	return nil
}

func (f *fakeDelegationLifecycle) snapshot() (acq, rel []scopedKey) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]scopedKey(nil), f.acquires...), append([]scopedKey(nil), f.releases...)
}

// withTempSocketRoot redirects the per-agent socket factory to a
// directly-rooted per-test tempdir. DefaultSocketPath nests one extra
// level below the configured runtime dir (`leapmux-<wid8>/`); anchoring
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
	dir, err := os.MkdirTemp("/tmp", "leapmux-")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("XDG_RUNTIME_DIR", dir)
}

// TestFactory_AgentSpawnAcquiresAndCleanupReleases pins the lifecycle
// contract: AgentSpawning must Acquire the user's delegation slot
// before the listener is in service, and the returned cleanup must
// Release it on close. Without this pairing, agent close wouldn't
// trigger the hub-side delegation revoke that the plan requires.
func TestFactory_AgentSpawnAcquiresAndCleanupReleases(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &controlipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "agent-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)

	acq, rel := lifecycle.snapshot()
	require.Len(t, acq, 1)
	assert.Equal(t, scopedKey{UserID: "user-1"}, acq[0],
		"agent spawn must take a reference on its user's delegation slot")
	require.Len(t, rel, 0, "Release must not run before cleanup is invoked")
	assert.NotEmpty(t, envs, "spawn must produce LEAPMUX_CONTROL_* env vars")

	cleanup()
	_, rel = lifecycle.snapshot()
	require.Len(t, rel, 1)
	assert.Equal(t, scopedKey{UserID: "user-1"}, rel[0],
		"cleanup must drop this spawn's reference; the mint's provenance tab comes from the live inventory, not from here")
}

// TestFactory_TerminalSpawnAcquiresAndCleanupReleases pins the lifecycle
// contract for terminal spawns: every terminal Acquires the user's
// delegation slot on construction and Releases it on cleanup. Mirrors the
// agent-side TestFactory_AgentSpawnAcquiresAndCleanupReleases.
func TestFactory_TerminalSpawnAcquiresAndCleanupReleases(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &controlipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	envs, cleanup, err := f.TerminalSpawning(service.TerminalSpawnInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	require.NotEmpty(t, envs, "terminal spawn must produce LEAPMUX_CONTROL_* env vars")

	acq, rel := lifecycle.snapshot()
	require.Len(t, acq, 1)
	assert.Equal(t, scopedKey{UserID: "user-1"}, acq[0],
		"terminal spawn must take a reference on its user's delegation slot")
	require.Len(t, rel, 0, "Release must not run before cleanup is invoked")

	cleanup()
	_, rel = lifecycle.snapshot()
	require.Len(t, rel, 1)
	assert.Equal(t, scopedKey{UserID: "user-1"}, rel[0],
		"cleanup must release THIS spawn's tab, mirroring the agent side")
}

// TestFactory_AgentSpawnAdvertisesItsTabContext pins what the bearer reports
// about itself. There is no workspace and no scope list: the tab id is the
// anchor everything else is derived from (a single hub LocateTab call), so it
// stays correct across a cross-workspace move where a baked-in workspace id
// would have gone stale.
func TestFactory_AgentSpawnAdvertisesItsTabContext(t *testing.T) {
	withTempSocketRoot(t)
	f := &controlipc.Factory{WorkerID: "worker-A"}
	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "agent-1",
	})
	require.NoError(t, err)
	t.Cleanup(cleanup)
	who := dialAndWhoami(t, envs)
	assert.Equal(t, "user-1", who.GetUserId())
	assert.Equal(t, "worker-A", who.GetWorkerId())
	assert.Equal(t, "agent-1", who.GetTabId())
	assert.Equal(t, leapmuxv1.TabType_TAB_TYPE_AGENT, who.GetTabType())
}

// TestFactory_NilDelegationIsTolerated documents that the Delegation
// field is optional. Tests / minimal configurations that don't wire a
// crossworker.DelegationStore must still get a working spawn.
func TestFactory_NilDelegationIsTolerated(t *testing.T) {
	withTempSocketRoot(t)
	f := &controlipc.Factory{WorkerID: "worker-A"}

	envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "agent-1",
	})
	require.NoError(t, err)
	require.NotEmpty(t, envs)
	cleanup() // must not panic
}

// dialAndWhoami parses LEAPMUX_CONTROL_SOCK / LEAPMUX_CONTROL_TOKEN
// from a freshly-spawned envs slice, dials the per-agent IPC server,
// and returns the Whoami response -- the spawn context the bearer
// carries. Going through the socket rather than peeking at the in-memory
// TokenStore keeps the assertion on the observable contract.
func dialAndWhoami(t *testing.T, envs []string) *leapmuxv1.WhoamiResponse {
	t.Helper()
	var sock, token string
	for _, e := range envs {
		switch {
		case strings.HasPrefix(e, "LEAPMUX_CONTROL_SOCK="):
			sock = strings.TrimPrefix(e, "LEAPMUX_CONTROL_SOCK=")
		case strings.HasPrefix(e, "LEAPMUX_CONTROL_TOKEN="):
			token = strings.TrimPrefix(e, "LEAPMUX_CONTROL_TOKEN=")
		}
	}
	require.NotEmpty(t, sock, "factory must emit LEAPMUX_CONTROL_SOCK")
	require.NotEmpty(t, token, "factory must emit LEAPMUX_CONTROL_TOKEN")

	httpClient, baseURL := ipcClient(t, sock)
	httpClient.Transport = &authHeaderInjector{token: token, base: httpClient.Transport}
	client := leapmuxv1connect.NewControlIPCServiceClient(httpClient, baseURL, connect.WithGRPC())

	resp, err := client.Whoami(context.Background(), connect.NewRequest(&leapmuxv1.WhoamiRequest{}))
	require.NoError(t, err)
	return resp.Msg
}

// authHeaderInjector mirrors the production CLI's
// X-LeapMux-Token-attaching transport. Kept private to factory_test so
// it doesn't drift from server_test's injectAuthHeader (different
// names, same behaviour — keeping them per-file avoids cross-test
// coupling on a small helper).
type authHeaderInjector struct {
	token string
	base  http.RoundTripper
}

func (i *authHeaderInjector) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set(controlipc.AuthHeader, i.token)
	return i.base.RoundTrip(req)
}

// TestFactory_ConcurrentSpawnsRefcountCorrectly exercises the case
// where two agent spawns and a terminal spawn share one user: every
// Acquire must pair with exactly one
// Release after each cleanup runs. Without the lifecycle wiring, a
// stray Release / missing Acquire would corrupt the refcount and
// either leak the delegation row or revoke it prematurely.
func TestFactory_ConcurrentSpawnsRefcountCorrectly(t *testing.T) {
	withTempSocketRoot(t)
	lifecycle := &fakeDelegationLifecycle{}
	f := &controlipc.Factory{
		WorkerID:   "worker-A",
		Delegation: lifecycle,
	}

	var spawned int32
	var cleanups []func()
	for i := 0; i < 3; i++ {
		envs, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
			UserID:   userid.MustNew("user-1"),
			WorkerID: "worker-A",
			TabID:    "agent-" + string(rune('A'+i)),
		})
		require.NoError(t, err)
		require.NotEmpty(t, envs)
		atomic.AddInt32(&spawned, 1)
		cleanups = append(cleanups, cleanup)
	}

	// One terminal in the same scope.
	envs, termCleanup, err := f.TerminalSpawning(service.TerminalSpawnInfo{
		UserID:   userid.MustNew("user-1"),
		WorkerID: "worker-A",
		TabID:    "term-1",
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

// TestFactory_SpawnRefusesEmptyUserID pins the local-IPC minting boundary:
// a zero user id must fail the spawn rather than building a Router as nobody
// (the same fail-closed rule ChannelOpen applies on the E2EE path).
//
// It must return service.ErrMissingIdentity specifically, not just any error:
// spawnControlIPC keys on that sentinel to decide FATAL-vs-degrade, so a plain
// errors.New here would silently make the spawn start without remote control
// instead of failing the tab.
func TestFactory_SpawnRefusesEmptyUserID(t *testing.T) {
	withTempSocketRoot(t)
	f := &controlipc.Factory{WorkerID: "worker-A"}

	_, cleanup, err := f.AgentSpawning(service.AgentSpawnInfo{
		UserID:   userid.UserID{},
		WorkerID: "worker-A",
		TabID:    "agent-1",
	})
	require.Error(t, err, "spawn with a zero user id must fail")
	assert.Nil(t, cleanup)
	assert.ErrorIs(t, err, service.ErrMissingIdentity,
		"the sentinel is what makes the caller treat this as fatal rather than degradable")

	_, cleanup, err = f.TerminalSpawning(service.TerminalSpawnInfo{
		UserID:   userid.UserID{},
		WorkerID: "worker-A",
		TabID:    "term-1",
	})
	require.Error(t, err, "terminal spawn with a zero user id must fail")
	assert.Nil(t, cleanup)
	assert.ErrorIs(t, err, service.ErrMissingIdentity)
}
