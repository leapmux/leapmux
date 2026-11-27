package service

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// Restarting an exited terminal replaces its in-memory shell, so the new shell
// starts with no exit code until it ends.
func TestRestartTerminal_ForgetsTheExitCodeOfThePreviousShell(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const id = "tm-forget-exit"
	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 25, Screen: []byte{},
	}))
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 25,
	}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	exitTerminalAndWait(t, svc, d, id, " 3")
	_, ok := svc.Terminals.ExitCode(id)
	require.True(t, ok, "the exited shell records its exit code in memory")

	w := newTestWriter()
	dispatchRestart(d, id, w)
	require.Empty(t, w.errors)
	testutil.AssertEventually(t, func() bool {
		row, err := svc.terminalCatalog().get(ctx, id)
		return err == nil && svc.Terminals.HasTerminal(id) && !svc.Terminals.IsExited(id) && svc.deriveTerminalStatus(&row).status == leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY
	}, "respawn")

	_, ok = svc.Terminals.ExitCode(id)
	assert.False(t, ok, "the new shell has not exited yet")
	row, err := svc.terminalCatalog().get(ctx, id)
	require.NoError(t, err)
	state := svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, state.status)
	assert.Nil(t, state.exitCode)
}

// A terminal that the manager does not hold reads EXITED with no exit code:
// live exit codes stay in memory and are not persisted across Worker restarts.
// A clean exit (code 0) held by the manager reports 0 rather than nil.
func TestDeriveTerminalStatus_StatesNoExitCodeWithoutARecord(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cmd.exe under ConPTY does not reliably preserve exit 0 via stdin; see TestRestartTerminal_HappyPath")
	}
	t.Parallel()
	ctx := context.Background()
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const id = "unseen-exit"
	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	row, err := svc.terminalCatalog().get(ctx, id)
	require.NoError(t, err)

	state := svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, state.status)
	assert.Nil(t, state.exitCode, "no exit code is in memory, so none is reported")

	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
	exitTerminalAndWait(t, svc, d, id, " 0")

	row, err = svc.terminalCatalog().get(ctx, id)
	require.NoError(t, err)
	state = svc.deriveTerminalStatus(&row)
	require.NotNil(t, state.exitCode, "a recorded 0 is an exit code, not the absence of one")
	assert.Equal(t, int32(0), *state.exitCode)

	svc.Terminals.RemoveTerminal(id)
	state = svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, state.status)
	assert.Nil(t, state.exitCode, "after removal from the manager, no persisted exit code remains")
}

// An archive tells every watcher that the shell is gone. The closed event
// states the live exit code held by the manager, and none for a terminal that
// the manager does not hold: a 0 there would claim a clean exit that no shell
// returned.
func TestApplyTabArchiveState_StatesTheExitCodeOnRecordInTheClosedEvent(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"no exit in memory": false,
		"a live exit":       true,
	}
	for name, withLiveExit := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			svc, d, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			const id = "term-archived"
			dir := t.TempDir()
			require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
				ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 25, Screen: []byte{},
			}))
			var wantCode *int32
			if withLiveExit {
				require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
					ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 25,
				}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
				testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
				exitTerminalAndWait(t, svc, d, id, " 5")
				code, ok := svc.Terminals.ExitCode(id)
				require.True(t, ok)
				v := int32(code)
				wantCode = &v
			}
			w := newTestWriter()
			watchTerminal(svc, id, w)

			_, err := svc.ApplyTabArchiveState(ctx,
				leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
				testTabOwner,
				[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: id}},
			)
			require.NoError(t, err)

			var closed *leapmuxv1.TerminalClosed
			require.Eventually(t, func() bool {
				for _, frame := range w.streamsSnapshot() {
					var resp leapmuxv1.WatchEventsResponse
					if proto.Unmarshal(frame.GetPayload(), &resp) == nil && resp.GetTerminalEvent().GetClosed() != nil {
						closed = resp.GetTerminalEvent().GetClosed()
						return true
					}
				}
				return false
			}, 30*time.Second, 5*time.Millisecond, "the archive must announce that the shell is gone")
			assert.Equal(t, wantCode, closed.ExitCode)
		})
	}
}

// derivedTerminalStatusEvent with reportExit=false populates ExitCode on
// TerminalStatusChange when status is EXITED and the manager holds the exit
// code, and leaves ExitCode nil when the exit code is unknown or the terminal
// is not exited.
func TestDerivedTerminalStatusEvent_PopulatesExitCodeWhenKnown(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const id = "tm-status-exit-code"
	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	row, err := svc.terminalCatalog().get(ctx, id)
	require.NoError(t, err)

	// DB-only terminal (not held by manager): EXITED with nil ExitCode.
	ev := svc.derivedTerminalStatusEvent(&row, false)
	sc := ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, sc.GetStatus())
	assert.Nil(t, sc.ExitCode, "unknown exit code must remain nil")

	// Live running terminal: READY with nil ExitCode.
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	ev = svc.derivedTerminalStatusEvent(&row, false)
	sc = ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_READY, sc.GetStatus())
	assert.Nil(t, sc.ExitCode)

	// Exited terminal held in memory: EXITED with known non-zero ExitCode.
	exitTerminalAndWait(t, svc, d, id, " 7")
	ev = svc.derivedTerminalStatusEvent(&row, false)
	sc = ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, sc.GetStatus())
	require.NotNil(t, sc.ExitCode, "known exit code must be populated on TerminalStatusChange")
	assert.Equal(t, int32(7), *sc.ExitCode)

	// After removal from manager, ExitCode is unknown again and must be nil.
	svc.Terminals.RemoveTerminal(id)
	ev = svc.derivedTerminalStatusEvent(&row, false)
	sc = ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, sc.GetStatus())
	assert.Nil(t, sc.ExitCode)
}

// derivedTerminalStatusEvent with reportExit=false omits ExitCode when an
// exited shell remains held in svc.Terminals while a restart is STARTING or
// STARTUP_FAILED (either in memory or persisted on the row).
func TestDerivedTerminalStatusEvent_OmitsExitCodeDuringRestartAndFailure(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const id = "tm-status-exit-suppressed"
	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: id, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(id), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)
	exitTerminalAndWait(t, svc, d, id, " 7")

	code, ok := svc.Terminals.ExitCode(id)
	require.True(t, ok, "precondition: exited shell remains held in the manager")
	require.Equal(t, 7, code)

	row, err := svc.terminalCatalog().get(ctx, id)
	require.NoError(t, err)

	// In-flight restart: STARTING must leave ExitCode nil.
	h := svc.TerminalStartup.begin(id, func() {})
	require.NotNil(t, h)
	defer func() {
		svc.TerminalStartup.cancelAndClear(id, keepWorktreeOnClose)
		svc.TerminalStartup.finishEntry(h)
	}()
	svc.TerminalStartup.setMessage(id, "Restarting shell…")
	ev := svc.derivedTerminalStatusEvent(&row, false)
	sc := ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, sc.GetStatus())
	assert.Equal(t, "Restarting shell…", sc.GetStartupMessage())
	assert.Nil(t, sc.ExitCode, "STARTING must omit the previous shell's exit code")

	// In-memory registry failure: STARTUP_FAILED must leave ExitCode nil.
	svc.TerminalStartup.fail(h, "restart spawn failed")
	ev = svc.derivedTerminalStatusEvent(&row, false)
	sc = ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, sc.GetStatus())
	assert.Equal(t, "restart spawn failed", sc.GetStartupError())
	assert.Nil(t, sc.ExitCode, "in-memory STARTUP_FAILED must omit the previous shell's exit code")

	// Persisted StartupError after registry clear: STARTUP_FAILED must leave ExitCode nil.
	svc.TerminalStartup.cancelAndClear(id, keepWorktreeOnClose)
	rowWithErr := row
	rowWithErr.StartupError = "persisted restart failure"
	ev = svc.derivedTerminalStatusEvent(&rowWithErr, false)
	sc = ev.GetStatusChange()
	require.NotNil(t, sc)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTUP_FAILED, sc.GetStatus())
	assert.Equal(t, "persisted restart failure", sc.GetStartupError())
	assert.Nil(t, sc.ExitCode, "persisted STARTUP_FAILED must omit the previous shell's exit code")

	code, ok = svc.Terminals.ExitCode(id)
	assert.True(t, ok, "the exited shell remained held in the manager throughout")
	assert.Equal(t, 7, code)
}

// broadcastTerminalStatusAfterStartup delivers TerminalStatusChange.ExitCode
// when the shell already exited and is held by the manager, and leaves it nil
// when the terminal is not held by the manager.
func TestBroadcastTerminalStatusAfterStartup_PopulatesExitCodeWhenKnown(t *testing.T) {
	t.Parallel()
	ctx := testutil.DeadlineContext(t)
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const heldID = "tm-broadcast-exit-known"
	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: heldID, WorkingDir: dir, Shell: testutil.TestShell(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	require.NoError(t, svc.Terminals.StartTerminal(ctx, terminal.Options{
		ID: heldID, Shell: testutil.TestShell(), WorkingDir: dir, Cols: 80, Rows: 24,
	}, svc.makeTerminalOutputFn(heldID), svc.makeTerminalExitFn()))
	testutil.RegisterTerminalCleanup(t, svc.Terminals, heldID)
	exitTerminalAndWait(t, svc, d, heldID, " 9")

	wHeld := newTestWriter()
	watchTerminal(svc, heldID, wHeld)
	svc.broadcastTerminalStatusAfterStartup(heldID)

	heldChange := lastTerminalStatusChange(wHeld)
	require.NotNil(t, heldChange)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, heldChange.GetStatus())
	require.NotNil(t, heldChange.ExitCode)
	assert.Equal(t, int32(9), *heldChange.ExitCode)

	const unknownID = "tm-broadcast-exit-unknown"
	wUnknown := newTestWriter()
	watchTerminal(svc, unknownID, wUnknown)
	svc.broadcastTerminalStatusAfterStartup(unknownID)

	unknownChange := lastTerminalStatusChange(wUnknown)
	require.NotNil(t, unknownChange)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, unknownChange.GetStatus())
	assert.Nil(t, unknownChange.ExitCode)
}

func lastTerminalStatusChange(w *testResponseWriter) *leapmuxv1.TerminalStatusChange {
	var last *leapmuxv1.TerminalStatusChange
	for _, frame := range w.streamsSnapshot() {
		var resp leapmuxv1.WatchEventsResponse
		if proto.Unmarshal(frame.GetPayload(), &resp) == nil && resp.GetTerminalEvent().GetStatusChange() != nil {
			last = resp.GetTerminalEvent().GetStatusChange()
		}
	}
	return last
}
