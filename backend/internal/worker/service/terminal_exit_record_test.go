package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/ptrconv"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// recordedExit is the exit_code of a row whose last shell's exit, code, is on
// record.
func recordedExit(code int64) sql.NullInt64 {
	return sql.NullInt64{Int64: code, Valid: true}
}

// recordTerminalExit stores code as the exit of terminal id's last shell, as
// the exit handler does.
func recordTerminalExit(t *testing.T, svc *Service, id string, code int64) {
	t.Helper()
	require.NoError(t, svc.Queries.SaveTerminalExit(t.Context(), db.SaveTerminalExitParams{
		ID: id, Cols: 80, Rows: 25, Screen: []byte{}, ExitCode: recordedExit(code),
	}))
}

// The Worker clears the code of the previous shell before a restart spawns
// the next one. A Worker that is killed while the new shell runs then holds
// no record of an exit, instead of reporting the old shell's code for a shell
// that it never saw end.
func TestRestartTerminal_ForgetsTheExitCodeOfThePreviousShell(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	const id = "tm-forget-exit"
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: id, WorkingDir: t.TempDir(), Shell: testutil.TestShell(), Cols: 80, Rows: 25, Screen: []byte{},
	}))
	recordTerminalExit(t, svc, id, 3)

	w := newTestWriter()
	dispatchRestart(d, id, w)
	require.Empty(t, w.errors)
	waitForTerminalSpawn(t, svc, id, "respawn")
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	code, err := svc.Queries.GetTerminalExitCode(ctx, id)
	require.NoError(t, err)
	assert.False(t, code.Valid, "the new shell's exit is not on record yet; got %v", code)
}

// The open path clears the code too, just before its spawn: a creation that
// resumes a terminal whose row holds an earlier shell's exit must not report
// that exit for the shell that it starts.
func TestOpenTerminal_ForgetsAnExitCodeOnRecordBeforeItSpawns(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, d, w := setupTestService(t, withClock(clock))
	ctx := testutil.DeadlineContext(t)
	resizeStart, resizeStop := testutil.NewTimerTraps(t, clock, startupPendingResizeTimerTag)

	atSpawn := make(chan sql.NullInt64, 1)
	svc.startTerminalFn = func(ctx context.Context, opts terminal.Options, outFn terminal.OutputHandler, exitFn terminal.ExitHandler) error {
		code, err := svc.Queries.GetTerminalExitCode(ctx, opts.ID)
		if err != nil {
			return err
		}
		atSpawn <- code
		return svc.Terminals.StartTerminal(ctx, opts, outFn, exitFn)
	}

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		WorkingDir: t.TempDir(), Shell: testutil.TestShell(), Cols: 80, Rows: 24,
	}, w)
	require.Empty(t, w.errors)
	var opened leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &opened))
	id := opened.GetTerminalId()
	testutil.RegisterTerminalCleanup(t, svc.Terminals, id)

	// The startup waits for the first resize. An exit on record now stands
	// for the row of a terminal that an earlier shell left behind.
	delay := testutil.WaitForTimer(t, ctx, resizeStart)
	recordTerminalExit(t, svc, id, 3)
	testutil.AdvanceAndAwaitStop(t, ctx, clock, delay, resizeStop)

	select {
	case code := <-atSpawn:
		assert.False(t, code.Valid, "the spawn begins with no exit on record; got %v", code)
	case <-ctx.Done():
		t.Fatal("the startup never spawned the shell")
	}
	drainAllInFlight(svc)
}

// A terminal that the manager does not hold and whose exit is not on record
// reads EXITED with no exit code: the Worker never saw that shell end.
func TestDeriveTerminalStatus_StatesNoExitCodeWithoutARecord(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	svc, _, _ := setupTestService(t)
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "unseen-exit", WorkingDir: t.TempDir(), Cols: 80, Rows: 24, Screen: []byte{},
	}))
	row, err := svc.Queries.GetTerminal(ctx, "unseen-exit")
	require.NoError(t, err)

	state := svc.deriveTerminalStatus(&row)
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_EXITED, state.status)
	assert.Nil(t, state.exitCode, "no exit code is on record, so none is reported")

	recordTerminalExit(t, svc, "unseen-exit", 0)
	row, err = svc.Queries.GetTerminal(ctx, "unseen-exit")
	require.NoError(t, err)
	state = svc.deriveTerminalStatus(&row)
	require.NotNil(t, state.exitCode, "a recorded 0 is an exit code, not the absence of one")
	assert.Equal(t, int32(0), *state.exitCode)
}

// An archive tells every watcher that the shell is gone. The closed event
// states the exit code on record, and none for a terminal whose exit is not on
// record: a 0 there would claim a clean exit that no shell returned.
func TestApplyTabArchiveState_StatesTheExitCodeOnRecordInTheClosedEvent(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		recorded *int64
		want     *int32
	}{
		"no exit on record": {},
		"a recorded exit":   {recorded: ptrconv.Ptr(int64(5)), want: ptrconv.Ptr(int32(5))},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			defer drainAllInFlight(svc)
			const id = "term-archived"
			seedTerminal(t, svc, id)
			if tc.recorded != nil {
				recordTerminalExit(t, svc, id, *tc.recorded)
			}
			w := newTestWriter()
			watchTerminal(svc, id, w)

			_, err := svc.ApplyTabArchiveState(t.Context(),
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
			assert.Equal(t, tc.want, closed.ExitCode)
		})
	}
}
