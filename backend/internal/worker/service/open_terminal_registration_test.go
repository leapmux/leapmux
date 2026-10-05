package service

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// execObserver runs afterExec with the text of each statement that completed
// without an error. The other three methods pass through unchanged.
type execObserver struct {
	inner     db.DBTX
	afterExec func(query string)
}

func (o execObserver) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	result, err := o.inner.ExecContext(ctx, query, args...)
	if err == nil {
		o.afterExec(query)
	}
	return result, err
}

func (o execObserver) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return o.inner.PrepareContext(ctx, query)
}

func (o execObserver) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return o.inner.QueryContext(ctx, query, args...)
}

func (o execObserver) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return o.inner.QueryRowContext(ctx, query, args...)
}

// A quake terminal is found by its directory, so a second device finds the row of
// a first open before that open has any other trace. quakeShellIsLive answers for
// a row from the PTY or from the startup registry. With neither, it reports a dead
// shell, and adoptExistingQuakeTerminal closes the row of a shell that is about to
// start. The second device then opens a second terminal, or fails with "failed to
// persist terminal" when it loses the unique index.
func TestOpenTerminal_QuakeStartupIsRegisteredBeforeTheRowIsVisible(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	// The second device opens the quake terminal of the same directory at the
	// instant that the row of the first open becomes visible.
	var secondID string
	opened := false
	svc.Queries = db.New(execObserver{inner: svc.DB, afterExec: func(query string) {
		if opened || !strings.Contains(query, "-- name: UpsertTerminal") {
			return
		}
		opened = true
		secondID, _ = openQuakeTerminal(t, svc, d, dir)
	}})

	firstID, _ := openQuakeTerminal(t, svc, d, dir)

	require.True(t, opened, "the observer must see the row of the first open")
	assert.Equal(t, firstID, secondID, "the second device must adopt the terminal that is starting")
	row, err := svc.Queries.GetTerminal(context.Background(), firstID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "the second device must not close the terminal that is starting")
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "one directory has one live quake terminal")
}

// A row that was never written owns no startup. The registry holds the startup
// from before the write, so a failed write must give it back.
func TestOpenTerminal_ReleasesTheStartupWhenTheRowCannotBeCreated(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}), withQueryRewrite(func(query string) string {
		if strings.Contains(query, "-- name: UpsertTerminal") {
			return "INSERT INTO no_such_table VALUES (1)"
		}
		return query
	}))
	// No drainAllInFlight: no startup goroutine runs, and a leaked startup must
	// fail the test at requireStartupsReleased. A drain would wait for it for ever.

	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, "failed to persist terminal", w.errors[0].message)
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	assert.Equal(t, 0, countTerminalRows(t, svc), "a failed write leaves no row")
}

// A quake open that loses the unique index adopts the winner and starts nothing.
// It registered a startup before its insert, so it must give that startup back
// when it adopts the winner, or its id reports STARTING for ever.
func TestOpenTerminal_ReleasesTheStartupOfAQuakeOpenThatLosesTheRace(t *testing.T) {
	t.Parallel()

	// The lookup by directory of the loser runs before the winner's insert and
	// misses it. The unique index then refuses the loser's insert.
	var hideWinner atomic.Bool
	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}), withQueryRewrite(func(query string) string {
		if strings.Contains(query, "-- name: GetOpenQuakeTerminalByWorkingDir") && hideWinner.CompareAndSwap(true, false) {
			return strings.Replace(query, "WHERE", "WHERE 1 = 0 AND", 1)
		}
		return query
	}))
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	winner, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(winner) }, "spawn")

	hideWinner.Store(true)
	loser, _ := openQuakeTerminal(t, svc, d, dir)

	assert.False(t, hideWinner.Load(), "the loser must have run its lookup by directory")
	assert.Equal(t, winner, loser, "the loser must adopt the winner")
	// The winner's startup goroutine is the only one that runs. The check waits
	// for it, and fails the test when the loser's startup is still counted.
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1)
	drainAllInFlight(svc)
}

// A startup ends in this order: the Manager registers the PTY, then the registry
// drops the entry. shellIsLive reads the registry first and the shell second, so
// the answer finds the entry, or the shell, or both. Read in the other order, it
// can find neither: the shell is absent at the early read, and the entry is gone
// at the late read. The caller then closes the row of a shell that runs.
//
// The probe stands for the read of the shell. It states what the Manager held at
// its own read. When the case says so, the startup ends as the probe returns.
func TestShellIsLive_ReadsTheRegistryBeforeTheShell(t *testing.T) {
	t.Parallel()

	const id = "terminal-1"
	for _, tc := range []struct {
		name string
		// startup says what the registry holds when the answer begins.
		startup startupKind
		// endsAfterProbe says whether the startup ends as the probe returns.
		endsAfterProbe bool
		// shellAtProbe is the answer of the Manager at the read of the shell.
		shellAtProbe bool
		want         bool
	}{
		{"a startup in flight with no shell yet", startupInFlight, false, false, true},
		{"a startup that ends with its shell right after the shell read", startupInFlight, true, false, true},
		{"a startup that ends with its shell before the shell read", startupInFlight, true, true, true},
		{"no startup and a running shell", startupNone, false, true, true},
		{"no startup and no shell", startupNone, false, false, false},
		{"a failed startup and no shell", startupFailed, false, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			svc, _, _ := setupTestService(t)
			var handle *startupEntry
			switch tc.startup {
			case startupInFlight:
				handle = svc.TerminalStartup.begin(id, func() {})
				require.NotNil(t, handle)
			case startupFailed:
				handle = svc.TerminalStartup.begin(id, func() {})
				require.NotNil(t, handle)
				svc.TerminalStartup.fail(handle, "spawn failed")
			case startupNone:
			}
			probes := 0
			live := svc.shellIsLive(id, func() bool {
				probes++
				if tc.endsAfterProbe {
					defer svc.TerminalStartup.succeed(id, handle)
				}
				return tc.shellAtProbe
			})

			assert.LessOrEqual(t, probes, 1, "the answer must ask for the shell at most once")
			assert.Equal(t, tc.want, live)
			if handle != nil {
				svc.TerminalStartup.abandon(handle)
				svc.TerminalStartup.cancelAndClear(id, keepWorktreeOnClose)
			}
			requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
		})
	}
}

// startupKind says what the startup registry holds for one id.
type startupKind int

const (
	startupNone startupKind = iota
	startupInFlight
	startupFailed
)
