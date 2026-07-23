package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// noIdentityRemoteIPC fails every spawn with ErrMissingIdentity and records
// whether the caller ever took ownership of a token from it.
//
// It also records cleanup runs, which is how the restart test distinguishes a
// failed restart that retired the dead spawn's token from one that leaked it.
type noIdentityRemoteIPC struct {
	failFrom int // 1-based: fail this call and every later one
	calls    int
	cleanups int
}

func (f *noIdentityRemoteIPC) spawn(tabID string) ([]string, func(), error) {
	f.calls++
	if f.calls >= f.failFrom {
		return nil, nil, ErrMissingIdentity
	}
	return []string{"LEAPMUX_REMOTE_TAB_ID=" + tabID}, func() { f.cleanups++ }, nil
}

func (f *noIdentityRemoteIPC) AgentSpawning(info AgentSpawnInfo) ([]string, func(), error) {
	return f.spawn(info.TabID)
}

func (f *noIdentityRemoteIPC) TerminalSpawning(info TerminalSpawnInfo) ([]string, func(), error) {
	return f.spawn(info.TabID)
}

// An agent that cannot name its user must fail the way every other startup
// failure does -- and must not leak the in-flight startup count.
//
// The agent row is already committed by the time the identity check runs, so
// simply returning leaves it open forever with no startup_error and no
// STARTUP_FAILED broadcast: every reconnecting client keeps listing a tab that
// never started and never explains itself, which is the exact unexplained
// symptom ErrMissingIdentity was introduced to remove. Separately,
// AgentStartup.begin() has already added to the in-flight WaitGroup and only
// runAgentStartup's deferred finish() releases it -- and that goroutine never
// launches on this path -- so a bare return wedges Shutdown's WaitForInFlight
// forever.
func TestOpenAgent_MissingIdentityFailsStartupAndReleasesInFlight(t *testing.T) {
	ctx := context.Background()
	ipc := &noIdentityRemoteIPC{failFrom: 1}
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(ipc))

	dispatch(d, "OpenAgent", &leapmuxv1.OpenAgentRequest{
		WorkspaceId: "ws-1",
		WorkingDir:  t.TempDir(),
	}, w)

	// The caller is told, but that alone is not enough -- see below.
	require.NotEmpty(t, w.rejections(), "the calling RPC must report the failure")

	// The startup wait group must be releasable. Guard with a timeout rather
	// than calling WaitForInFlight directly, so a leak fails the test instead
	// of hanging the whole package.
	drained := make(chan struct{})
	go func() {
		svc.AgentStartup.WaitForInFlight()
		close(drained)
	}()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("AgentStartup in-flight count leaked: Shutdown's WaitForInFlight would block forever")
	}

	// The row must name its own cause, for every watcher and not just the
	// caller that happened to be connected.
	ids, err := svc.Queries.ListAllAgentIDsAndWorkspaces(ctx)
	require.NoError(t, err)
	require.Len(t, ids, 1, "the agent row was committed before the identity check")
	row, err := svc.Queries.GetAgentByID(ctx, ids[0].ID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupError,
		"a spawn refused for a missing identity must persist why, or the tab is a phantom")

	// ...and the row must be tombstoned. This is the only startup failure that
	// answers the RPC with an error instead of an OpenAgentResponse, so the
	// client never learns the agent id: it cannot list the tab and will never
	// send CloseAgent. An open row here is a tab nobody can name and nobody can
	// close, left for the hourly OrphanReconciler to notice.
	assert.True(t, row.ClosedAt.Valid,
		"nothing will ever close this row: the refusal must tombstone it itself")
}

// A restart that cannot name its user must fail the restart AND retire the dead
// spawn's token.
//
// Both halves matter, and the second is the one that is easy to get wrong. The
// restart path mints the fresh token before retiring the previous one -- they
// share a registry key, so the swap has to be ordered -- which makes it tempting
// to skip the retire when the mint fails, on the theory that the old shell still
// needs its socket. It does not: RestartTerminal refuses synchronously while
// IsRunning, so by the time this goroutine runs the old PTY has already exited.
// Skipping the retire leaks a listening unix socket and an unrevoked
// (user, workspace) delegation bearer for a process that is gone, and nothing
// later comes along to clean them up because the restart never happens.
//
// The fixture below exits the terminal first for exactly that reason -- it is
// the only state from which a restart is reachable at all.
func TestRestartTerminal_MissingIdentityFailsAndRetiresPreviousToken(t *testing.T) {
	// Fail the SECOND spawn: the initial open must succeed so there is a live
	// token for the restart to retire.
	ipc := &noIdentityRemoteIPC{failFrom: 2}
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(ipc))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, "ws-1", t.TempDir())
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)
	require.Equal(t, 1, ipc.calls, "the initial open mints a token")
	require.Zero(t, ipc.cleanups, "nothing has retired it yet")

	exitTerminalAndWait(t, svc, d, terminalID, "")

	w2 := newTestWriter()
	dispatchRestart(d, terminalID, w2)
	svc.TerminalStartup.WaitForInFlight()

	require.Equal(t, 2, ipc.calls, "the restart attempts a fresh mint")
	assert.Equal(t, 1, ipc.cleanups,
		"the exited shell's token must be retired: no restart is coming to retire it later")

	// The failure has to be visible, not just clean. Without the persisted
	// error the tab sits in STARTING with nothing naming the cause.
	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.NotEmpty(t, row.StartupError,
		"a restart refused for a missing identity must persist why")
}
