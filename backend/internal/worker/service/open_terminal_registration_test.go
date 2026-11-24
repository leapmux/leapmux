package service

import (
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
)

// A quake terminal is found by its directory, so a second device finds the entry
// of a first open before that open has any other trace. quakeShellIsLive answers
// for an entry from the PTY or from the startup registry. With neither, it reports
// a dead shell, and adoptExistingQuakeTerminal deletes the entry of a shell that
// is about to start.
func TestOpenTerminal_QuakeStartupIsRegisteredBeforeTheRowIsVisible(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	// The second device opens the quake terminal of the same directory at the
	// instant that the entry of the first open becomes visible.
	var secondID string
	opened := false
	svc.QuakeTerminals.afterRegister = func(string) {
		if opened {
			return
		}
		opened = true
		secondID, _ = openQuakeTerminal(t, svc, d, dir)
	}

	firstID, _ := openQuakeTerminal(t, svc, d, dir)

	require.True(t, opened, "the hook must see the registration of the first open")
	assert.Equal(t, firstID, secondID, "the second device must adopt the terminal that is starting")
	assert.True(t, svc.QuakeTerminals.Has(firstID), "the second device must not delete the terminal that is starting")
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 1, "one directory has one live quake terminal")
}

// A row that was never written owns no startup. The registry holds the startup
// from before the write, so a failed write must give it back.
func TestOpenTerminal_ReleasesTheStartupWhenTheRowCannotBeCreated(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	// A real trigger also applies to the query instance attached to the transaction.
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_terminal_creation BEFORE INSERT ON terminals BEGIN SELECT RAISE(ABORT, 'create terminal failed'); END`)
	require.NoError(t, err)
	// No drainAllInFlight: no startup goroutine runs, and a leaked startup must
	// fail the test at requireStartupsReleased. A drain would wait for it for ever.

	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: t.TempDir(),
	}, w)

	require.Len(t, w.errors, 1)
	assert.Equal(t, "failed to persist terminal", w.errors[0].message)
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	assert.Equal(t, 0, countTerminalRows(t, svc), "a failed write leaves no row")
	var pending int
	require.NoError(t, svc.DB.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM tab_creations`).Scan(&pending))
	assert.Zero(t, pending, "a failed creation transaction leaves no pending operation")
}

// A quake open that loses the registration race adopts the winner and starts
// nothing. It registered a startup before calling RegisterOrAdopt, so it must
// give that startup back when it adopts the winner, or its id reports STARTING
// for ever.
func TestOpenTerminal_ReleasesTheStartupOfAQuakeOpenThatLosesTheRace(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	winner, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, winner, "spawn")

	// Force the loser's initial GetByWorkingDir lookup to miss so it enters the
	// RegisterOrAdopt race and adopts the winner there.
	var hideWinner atomic.Bool
	hideWinner.Store(true)
	svc.QuakeTerminals.hideLookupOnce = func(workingDir string) bool {
		return workingDir == dir && hideWinner.CompareAndSwap(true, false)
	}
	loser, _ := openQuakeTerminal(t, svc, d, dir)

	assert.False(t, hideWinner.Load(), "the loser must have run its lookup by directory")
	assert.Equal(t, winner, loser, "the loser must adopt the winner")
	// The winner's startup goroutine is the only one that runs. The check waits
	// for it, and fails the test when the loser's startup is still counted.
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 1)
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

func TestRegisterQuakeTerminal_ReapsDeadWinnerAndConverges(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	dir := t.TempDir()
	w := newTestWriter()

	// Seed a dead quake winner with no PTY and no startup claim.
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID:         "dead-winner",
		WorkingDir: dir,
		Title:      "Dead Quake",
	})
	require.True(t, created)
	require.False(t, svc.quakeShellIsLive("dead-winner"))

	startupCtx, startupHandle := svc.beginTerminalStartup("fresh-quake", testutil.TestShell())
	require.NotNil(t, startupHandle)

	gotCtx, gotHandle, ok := svc.registerQuakeTerminal(w, QuakeTerminal{
		ID:         "fresh-quake",
		WorkingDir: dir,
		Shell:      testutil.TestShell(),
		Title:      "Fresh Quake",
	}, startupCtx, startupHandle)

	require.True(t, ok)
	require.NotNil(t, gotCtx)
	require.NotNil(t, gotHandle)
	assert.Empty(t, w.errors)
	assert.Empty(t, w.responses)
	assert.True(t, svc.quakeShellIsLive("fresh-quake"))
	assert.False(t, svc.QuakeTerminals.Has("dead-winner"))
	assert.True(t, svc.QuakeTerminals.IsKnownQuake("dead-winner"))
	assert.True(t, svc.QuakeTerminals.Has("fresh-quake"))

	registered, found := svc.QuakeTerminals.GetByWorkingDir(dir)
	require.True(t, found)
	assert.Equal(t, "fresh-quake", registered.ID)

	svc.TerminalStartup.abandon(gotHandle)
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
}

func TestRegisterQuakeTerminal_RetryStartupRefusedOnAttemptOne(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t)
	dir := t.TempDir()
	w := newTestWriter()

	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID:         "dead-0",
		WorkingDir: dir,
		Title:      "Dead 0",
	})
	require.True(t, created)
	svc.terminalCleanups.claim("dead-0")
	require.True(t, svc.terminalCleanups.register("dead-0", func() {
		svc.TerminalStartup.closeClaims()
	}))

	startupCtx, startupHandle := svc.beginTerminalStartup("fresh-quake", testutil.TestShell())
	require.NotNil(t, startupHandle)

	gotCtx, gotHandle, ok := svc.registerQuakeTerminal(w, QuakeTerminal{
		ID:         "fresh-quake",
		WorkingDir: dir,
		Shell:      testutil.TestShell(),
		Title:      "Fresh Quake",
	}, startupCtx, startupHandle)

	assert.False(t, ok)
	assert.Nil(t, gotCtx)
	assert.Nil(t, gotHandle)
	require.Len(t, w.errors, 1)
	assert.Equal(t, int32(codes.FailedPrecondition), w.errors[0].code)
	requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
}

func TestRegisterQuakeTerminal_ExhaustsAttemptsAcrossDeadWinners(t *testing.T) {
	t.Parallel()

	t.Run("no live terminal at final state returns internal error", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()
		w := newTestWriter()

		seedDead := func(id string) {
			_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
				ID:         id,
				WorkingDir: dir,
				Title:      id,
			})
			require.True(t, created)
		}

		seedDead("dead-0")
		for i := range maxQuakeRegisterAttempts - 1 {
			currID := fmt.Sprintf("dead-%d", i)
			nextID := fmt.Sprintf("dead-%d", i+1)
			svc.terminalCleanups.claim(currID)
			require.True(t, svc.terminalCleanups.register(currID, func() {
				// Delete the reaped entry before seeding the next dead winner because
				// closeQuakeTerminal runs terminalCleanups before QuakeTerminals.Delete.
				svc.QuakeTerminals.Delete(currID)
				seedDead(nextID)
			}))
		}

		startupCtx, startupHandle := svc.beginTerminalStartup("fresh-quake", testutil.TestShell())
		require.NotNil(t, startupHandle)

		gotCtx, gotHandle, ok := svc.registerQuakeTerminal(w, QuakeTerminal{
			ID:         "fresh-quake",
			WorkingDir: dir,
			Shell:      testutil.TestShell(),
			Title:      "Fresh Quake",
		}, startupCtx, startupHandle)

		assert.False(t, ok)
		assert.Nil(t, gotCtx)
		assert.Nil(t, gotHandle)
		assert.Empty(t, w.responses)
		require.Len(t, w.errors, 1)
		assert.Equal(t, int32(codes.Internal), w.errors[0].code)
		assert.Equal(t, "failed to register quake terminal", w.errors[0].message)
		requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	})

	t.Run("live terminal at final state is adopted", func(t *testing.T) {
		t.Parallel()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()
		w := newTestWriter()

		seedDead := func(id string) {
			_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
				ID:         id,
				WorkingDir: dir,
				Title:      id,
			})
			require.True(t, created)
		}

		seedDead("dead-0")
		for i := range maxQuakeRegisterAttempts - 1 {
			currID := fmt.Sprintf("dead-%d", i)
			nextID := fmt.Sprintf("dead-%d", i+1)
			svc.terminalCleanups.claim(currID)
			require.True(t, svc.terminalCleanups.register(currID, func() {
				svc.QuakeTerminals.Delete(currID)
				seedDead(nextID)
			}))
		}
		var liveHandle *startupEntry
		lastDeadID := fmt.Sprintf("dead-%d", maxQuakeRegisterAttempts-1)
		svc.terminalCleanups.claim(lastDeadID)
		require.True(t, svc.terminalCleanups.register(lastDeadID, func() {
			svc.QuakeTerminals.Delete(lastDeadID)
			liveHandle = svc.TerminalStartup.begin("live-final", func() {})
			require.NotNil(t, liveHandle)
			_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
				ID:         "live-final",
				WorkingDir: dir,
				Title:      "Live Final Quake",
			})
			require.True(t, created)
		}))

		startupCtx, startupHandle := svc.beginTerminalStartup("fresh-quake", testutil.TestShell())
		require.NotNil(t, startupHandle)

		gotCtx, gotHandle, ok := svc.registerQuakeTerminal(w, QuakeTerminal{
			ID:         "fresh-quake",
			WorkingDir: dir,
			Shell:      testutil.TestShell(),
			Title:      "Fresh Quake",
		}, startupCtx, startupHandle)

		assert.False(t, ok)
		assert.Nil(t, gotCtx)
		assert.Nil(t, gotHandle)
		assert.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.OpenTerminalResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		assert.Equal(t, "live-final", resp.GetTerminalId())
		assert.Equal(t, "Live Final Quake", resp.GetTitle())

		require.NotNil(t, liveHandle)
		svc.TerminalStartup.abandon(liveHandle)
		requireStartupsReleased(t, &svc.TerminalStartup.startupCore)
	})
}
