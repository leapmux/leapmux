package service

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// dispatchRestart marshals + dispatches a RestartTerminalRequest with the
// standard payload used across these tests (workspace ws-1, 80x25). The
// terminal id is the only field that varies; tests that need to override
// other fields should call dispatch directly.
func dispatchRestart(d *channel.Dispatcher, terminalID string, w *testResponseWriter) {
	dispatch(d, "RestartTerminal", &leapmuxv1.RestartTerminalRequest{
		WorkspaceId: "ws-1",
		TerminalId:  terminalID,
		// Match openTerminalViaRPC's wide-cols workaround: cmd.exe's
		// long-path prompt + `exit 42` wraps at 80 cols and ConPTY's
		// cooked-mode editor then reads the line as just `exit`.
		Cols: 200,
		Rows: 25,
	}, w)
}

// fakeRemoteIPC records every TerminalSpawning call so tests can assert
// (a) it ran the expected number of times, (b) each call minted a fresh
// token, and (c) cleanups fired before the next spawn.
type fakeRemoteIPC struct {
	mu               sync.Mutex
	count            atomic.Int64
	tokens           []string
	cleanupsPending  int
	cleanupsExecuted int
}

func (f *fakeRemoteIPC) AgentSpawning(AgentSpawnInfo) ([]string, func(), error) {
	return nil, func() {}, nil
}

func (f *fakeRemoteIPC) TerminalSpawning(info TerminalSpawnInfo) ([]string, func(), error) {
	n := f.count.Add(1)
	token := fmt.Sprintf("token-%d", n)
	envs := []string{
		"LEAPMUX_REMOTE_TAB_ID=" + info.TabID,
		"LEAPMUX_REMOTE_TOKEN=" + token,
	}
	f.mu.Lock()
	f.tokens = append(f.tokens, token)
	f.cleanupsPending++
	f.mu.Unlock()
	cleanup := func() {
		f.mu.Lock()
		f.cleanupsExecuted++
		f.mu.Unlock()
	}
	return envs, cleanup, nil
}

func (f *fakeRemoteIPC) snapshot() (tokens []string, pending, executed int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.tokens...), f.cleanupsPending, f.cleanupsExecuted
}

// TestRestartTerminal_HappyPath drives the full OpenTerminal → exit →
// RestartTerminal → exit-again loop and verifies the exit notice carries
// the right exit codes ("0" both times for a clean `exit`).
func TestRestartTerminal_HappyPath(t *testing.T) {
	ctx := context.Background()
	ipc := &fakeRemoteIPC{}
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(ipc))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, "ws-1", t.TempDir())
	exitTerminalAndWait(t, svc, d, terminalID, "")
	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(ctx, terminalID)
		return err == nil && bytes.Contains(row.Screen, []byte("[Terminal process exited (0) - Press Enter to restart]"))
	}, "first exit notice in DB")

	require.Equal(t, int64(1), ipc.count.Load(), "TerminalSpawning should fire once on initial open")
	tokensBefore, _, _ := ipc.snapshot()
	require.Len(t, tokensBefore, 1)

	// Restart.
	w3 := newTestWriter()
	dispatchRestart(d, terminalID, w3)
	require.Empty(t, w3.errors)
	require.Len(t, w3.responses, 1, "RestartTerminal should return a response")

	// New PTY must come up.
	testutil.AssertEventually(t, func() bool {
		return svc.Terminals.HasTerminal(terminalID) && !svc.Terminals.IsExited(terminalID)
	}, "restart spawn")
	// Register cleanup AFTER t.TempDir() above so this t.Cleanup runs
	// first (LIFO): the respawned PTY must be stopped before the temp
	// working-dir is removed, or Windows' unlinkat fails because cmd.exe
	// still has the dir open as its CWD.
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)

	require.Equal(t, int64(2), ipc.count.Load(), "TerminalSpawning should fire again on restart")
	tokensAfter, _, cleanupsExecuted := ipc.snapshot()
	require.Len(t, tokensAfter, 2)
	assert.NotEqual(t, tokensAfter[0], tokensAfter[1], "restart must mint a token distinct from the previous spawn")
	assert.GreaterOrEqual(t, cleanupsExecuted, 1, "the previous spawn's cleanup must run before the new token is minted")

	// Exit the restarted shell with a non-zero code so we can assert the
	// new notice carries a different value. Skipped on Windows because
	// cmd.exe under ConPTY does not reliably propagate `exit N` (N != 0)
	// to its OS exit code — see the skip note on
	// TestShutdown_PreservesNaturalExitCode for details. The
	// per-exit-code formatting is covered by TestFormatTerminalExitedNotice
	// and the persist-on-exit path by TestPersistTerminalOnExit_Idempotent,
	// both of which run cross-platform.
	if runtime.GOOS != "windows" {
		exitTerminalAndWait(t, svc, d, terminalID, " 7")
		testutil.AssertEventually(t, func() bool {
			row, err := svc.Queries.GetTerminal(ctx, terminalID)
			return err == nil && bytes.Contains(row.Screen, []byte("[Terminal process exited (7) - Press Enter to restart]"))
		}, "second exit notice with code 7")
	}
}

// TestRestartTerminal_StillRunning rejects restarts on a live terminal so
// the alive PTY isn't orphaned.
func TestRestartTerminal_StillRunning(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	const id = "tm-still-running"
	startTestTerminal(t, svc, ctx, id, "ws-1")
	// Wait for the manager to consider the PTY live so the
	// HasTerminal/!IsExited guard inside the handler fires reliably.
	testutil.AssertEventually(t, func() bool {
		return svc.Terminals.HasTerminal(id) && !svc.Terminals.IsExited(id)
	}, "spawn")

	w := newTestWriter()
	dispatchRestart(d, id, w)
	require.Empty(t, w.responses)
	require.Len(t, w.errors, 1)
	assert.Equal(t, codeFailedPrecondition, w.errors[0].code, "expected FailedPrecondition")
	assert.Contains(t, w.errors[0].message, "still running")
}

// TestRestartTerminal_AfterWorkerRestart exercises the path where the
// in-memory *Terminal is gone (e.g. worker process restarted) but the
// DB row is present. The restart RPC must recreate the entry with a
// ScreenBuffer whose cumulative counter starts at the persisted-screen
// length so future end_offset values stay ahead of the frontend's
// cached lastOffset.
func TestRestartTerminal_AfterWorkerRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, _ := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	const id = "tm-worker-restart"
	workingDir := t.TempDir()
	persistedScreen := []byte("old session output\r\n[Worker disconnected - Press Enter to restart]\r\n")
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID:          id,
		WorkspaceID: "ws-1",
		WorkingDir:  workingDir,
		HomeDir:     svc.HomeDir,
		Shell:       testutil.TestShell(),
		Cols:        80,
		Rows:        25,
		Screen:      persistedScreen,
		ExitCode:    int64(exitCodeUnknown),
	}))

	require.False(t, svc.Terminals.HasTerminal(id), "no in-memory entry pre-restart")

	w := newTestWriter()
	dispatchRestart(d, id, w)
	require.Empty(t, w.errors)

	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(id) }, "respawn")
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	// Drive a small bit of output so the ScreenBuffer's counter advances
	// past its seeded baseline.
	enter := testutil.TestShellEnter()
	require.NoError(t, svc.Terminals.SendInput(id, []byte("echo respawn"+enter)))

	// Query the cumulative offset directly: it should be >= the persisted
	// screen length, proving the seed worked. A regression here would
	// surface as a frontend snapshot replay on resubscribe (silently
	// wiping the existing xterm buffer).
	persistedLen := int64(len(persistedScreen))
	testutil.AssertEventually(t, func() bool {
		_, endOffset, _ := svc.Terminals.ScreenSnapshotSince(id, 0)
		return endOffset >= persistedLen
	}, "cumulative offset must start at >= persisted screen length")
}

// TestPersistTerminalOnExit_Idempotent verifies the suffix-based
// idempotency check inside persistTerminalOnExit: re-persisting an
// already-noticed terminal (even with a different exit code) must not
// re-append the notice, so a worker-shutdown-then-exit race doesn't
// double-stack notices.
func TestPersistTerminalOnExit_Idempotent(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	startTestTerminal(t, svc, ctx, "term-1", "ws-1")

	// Seed the live screen with some baseline content so the notice has
	// something to follow — the empty-screen path hits a different branch
	// (SnapshotTerminal returns ok=false).
	require.True(t, svc.Terminals.AppendOutput("term-1", []byte("hello")))

	require.True(t, svc.persistTerminalOnExit("term-1", 0))
	// Second call with a different exit code must NOT add another notice.
	require.True(t, svc.persistTerminalOnExit("term-1", 7))

	row, err := svc.Queries.GetTerminal(ctx, "term-1")
	require.NoError(t, err)
	assert.True(t, bytes.HasSuffix(row.Screen, terminalExitedNoticeSuffix))
	assert.Equal(t, 1, bytes.Count(row.Screen, []byte("[Terminal process exited")),
		"second persist call must not double-stack the exit notice")
	assert.Contains(t, string(row.Screen), "(0)", "first call's exit code wins")
	assert.NotContains(t, string(row.Screen), "(7)", "second call must not append its exit code")
}

// TestPersistTerminalOnExit_ShutdownDoesNotClobberRealExitCode mirrors
// the race between the exit handler (writes the real exit code) and
// Shutdown (writes exitCodeUnknown): a Shutdown call landing AFTER the
// exit handler's persist must leave the real exit code intact.
func TestPersistTerminalOnExit_ShutdownDoesNotClobberRealExitCode(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := setupTestService(t, withWorkspaces("ws-1"))
	startTestTerminal(t, svc, ctx, "term-race", "ws-1")
	require.True(t, svc.Terminals.AppendOutput("term-race", []byte("hello")))

	// Exit handler arrives first with a real exit code.
	require.True(t, svc.persistTerminalOnExit("term-race", 42))
	// Shutdown's late call with exitCodeUnknown must be a no-op.
	require.True(t, svc.persistTerminalOnExit("term-race", exitCodeUnknown))

	row, err := svc.Queries.GetTerminal(ctx, "term-race")
	require.NoError(t, err)
	assert.Equal(t, int64(42), row.ExitCode, "Shutdown must not overwrite the real exit code with exitCodeUnknown")
	assert.Contains(t, string(row.Screen), "(42)", "exit handler's notice text must survive")
	assert.NotContains(t, string(row.Screen), "Worker disconnected",
		"Shutdown must not append the worker-disconnected notice when the exit handler already finished")
}

// TestFormatTerminalExitedNotice exercises the formatter directly so a
// regression in the rendered digits — the part the user actually reads —
// surfaces here and not via the brittle string-search in higher-level
// tests.
func TestFormatTerminalExitedNotice(t *testing.T) {
	cases := []struct {
		name string
		code int
		want string
	}{
		{name: "zero", code: 0, want: "[Terminal process exited (0) - Press Enter to restart]"},
		{name: "positive", code: 255, want: "[Terminal process exited (255) - Press Enter to restart]"},
		{name: "negative non-sentinel", code: -2, want: "[Terminal process exited (-2) - Press Enter to restart]"},
		{name: "unknown sentinel", code: exitCodeUnknown, want: "[Worker disconnected - Press Enter to restart]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := string(formatTerminalExitedNotice(tc.code))
			assert.Contains(t, got, tc.want, "rendered notice must contain the expected exit-code text")
			assert.True(t, bytes.HasSuffix([]byte(got), terminalExitedNoticeSuffix),
				"every notice must end with the idempotency suffix")
			// The notice is bounded above and below by CRLFs so xterm
			// renders it on its own line(s).
			assert.True(t, strings.HasPrefix(got, "\r\n\r\n"))
			assert.True(t, strings.HasSuffix(got, "\r\n"))
		})
	}
}

// TestRestartTerminal_StartupInProgress: a RestartTerminal that lands
// while a previous startup hasn't broadcast READY/FAILED yet must be
// rejected so two competing PTYs aren't both registered under the same
// id. The handler reads svc.TerminalStartup.status() — we seed a
// STARTING entry directly so we don't have to time a real startup.
func TestRestartTerminal_StartupInProgress(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"))
	defer drainAllInFlight(svc)

	const id = "tm-startup-busy"
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkspaceID: "ws-1", Cols: 80, Rows: 25, Shell: testutil.TestShell(), Screen: []byte{},
	}))

	svc.TerminalStartup.begin(id, func() {})
	// begin() bumps the in-flight WaitGroup. cancelAndClear only deletes
	// the entry and runs the cancel — finish() is the matching Done()
	// call. Without this, drainAllInFlight would block forever.
	defer func() {
		svc.TerminalStartup.cancelAndClear(id)
		svc.TerminalStartup.finish()
	}()

	dispatchRestart(d, id, w)
	require.Empty(t, w.responses)
	require.Len(t, w.errors, 1)
	assert.Equal(t, codeFailedPrecondition, w.errors[0].code, "expected FailedPrecondition")
	assert.Contains(t, w.errors[0].message, "startup in progress")
}

// TestRestartTerminal_PreservesTitle: after the user renames the tab
// via UpdateTerminalTitle, the title must survive a restart. The
// Manager replaces a defined subset of meta fields on restart; title
// must stay in the preserved set.
func TestRestartTerminal_PreservesTitle(t *testing.T) {
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, "ws-1", t.TempDir())

	const newTitle = "user-renamed: ~/dir"
	w2 := newTestWriter()
	dispatch(d, "UpdateTerminalTitle", &leapmuxv1.UpdateTerminalTitleRequest{
		WorkspaceId: "ws-1", TerminalId: terminalID, Title: newTitle,
	}, w2)
	require.Empty(t, w2.errors)

	exitTerminalAndWait(t, svc, d, terminalID, "")

	w4 := newTestWriter()
	dispatchRestart(d, terminalID, w4)
	require.Empty(t, w4.errors)
	testutil.AssertEventually(t, func() bool {
		return svc.Terminals.HasTerminal(terminalID) && !svc.Terminals.IsExited(terminalID)
	}, "respawn")
	testutil.RegisterTerminalCleanup(t, svc.Terminals, terminalID)

	// In-memory meta and DB row should both still carry the user title.
	meta, ok := svc.Terminals.GetMeta(terminalID)
	require.True(t, ok)
	assert.Equal(t, newTitle, meta.Title, "title must survive Manager.RestartTerminal's meta overwrite")
}

// TestRestartTerminal_ShellResolution pins the single-tier resolution:
// the handler reads dbTerm.Shell, and a row with an empty shell is
// rejected (the OpenTerminal write path resolves the default shell
// up front, so an empty value here is a real bug we surface rather
// than mask).
func TestRestartTerminal_ShellResolution(t *testing.T) {
	ctx := context.Background()

	t.Run("uses db shell when no in-memory entry exists", func(t *testing.T) {
		svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(&fakeRemoteIPC{}))
		defer drainAllInFlight(svc)
		const id = "tm-db-shell"
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkspaceID: "ws-1", WorkingDir: t.TempDir(),
			Shell: testutil.TestShell(),
			Cols:  80, Rows: 25, Screen: []byte{},
		}))
		require.False(t, svc.Terminals.HasTerminal(id), "no in-memory entry pre-restart")

		dispatchRestart(d, id, w)
		require.Empty(t, w.errors)
		testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(id) }, "respawn")
		testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

		// A successful respawn proves dbTerm.Shell propagated through to
		// the spawn — startWithScreenBuffer would have failed otherwise.
	})

	t.Run("rejects when db shell is empty", func(t *testing.T) {
		svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(&fakeRemoteIPC{}))
		defer drainAllInFlight(svc)
		const id = "tm-no-shell"
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: id, WorkspaceID: "ws-1", WorkingDir: t.TempDir(),
			// Shell intentionally empty to mimic a row written by code
			// that forgot to plumb shell through. The handler must
			// reject rather than silently swap in a different binary.
			Cols: 80, Rows: 25, Screen: []byte{},
		}))

		dispatchRestart(d, id, w)
		require.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, codeFailedPrecondition, w.errors[0].code)
		assert.Contains(t, w.errors[0].message, "no shell")
		assert.False(t, svc.Terminals.HasTerminal(id), "no PTY should be spawned when shell is unresolved")
	})
}

// TestRestartTerminal_CloseDuringRestart exercises the closed_at guard
// in runTerminalRestart. We force the race deterministically by setting
// closed_at on the DB row *before* dispatching RestartTerminal:
// requireAccessibleTerminal doesn't check closed_at, so the spawn
// proceeds, and the post-spawn re-fetch inside runTerminalRestart sees
// the closed row and tears the freshly-spawned PTY back down.
//
// (The Manager-level RestartTerminal doesn't go through the
// startTerminalFn indirection that OpenTerminal uses, so we can't
// inject a callback there. Pre-seeding closed_at is the simplest way
// to drive the same code path the real CloseTerminal-during-restart
// race produces.)
func TestRestartTerminal_CloseDuringRestart(t *testing.T) {
	ctx := context.Background()
	svc, d, w := setupTestService(t, withWorkspaces("ws-1"), withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	const id = "tm-close-race"
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkspaceID: "ws-1", WorkingDir: t.TempDir(),
		Shell: testutil.TestShell(),
		Cols:  80, Rows: 25, Screen: []byte{},
	}))
	// Mimic a CloseTerminal that won the race: closed_at is already set
	// when the RestartTerminal request lands.
	require.NoError(t, svc.Queries.CloseTerminal(ctx, id))

	dispatchRestart(d, id, w)
	require.Empty(t, w.errors)

	// Wait for runTerminalRestart to finish its trailing work, then poll
	// — the goroutine removes the manager entry asynchronously after
	// startWithScreenBuffer returns.
	svc.TerminalStartup.WaitForInFlight()
	testutil.AssertEventually(t, func() bool { return !svc.Terminals.HasTerminal(id) },
		"runTerminalRestart must remove the PTY when closed_at is set between spawn and post-spawn fetch")
}
