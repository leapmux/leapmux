package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// A QUAKE terminal is the shell behind the quake panel, and it belongs to a
// working DIRECTORY on this worker rather than to any one tab. These tests pin
// the properties every device and every tab depend on: at most one live quake
// terminal per directory, the same one handed to whoever asks second -- from
// another device OR from another tab in that directory -- and the shell
// outliving every tab but the last.

// openQuakeTerminal runs OpenTerminal with quake set and returns the terminal
// id and the stored title.
func openQuakeTerminal(t *testing.T, svc *Service, d *channel.Dispatcher, workingDir string) (string, string) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: workingDir,
		Quake:      true,
		Cols:       200,
		Rows:       24,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotEmpty(t, resp.GetTerminalId())
	testutil.RegisterTerminalCleanup(t, svc.Terminals, resp.GetTerminalId())
	return resp.GetTerminalId(), resp.GetTitle()
}

func TestOpenTerminal_RecordsTheQuakeFlag(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	terminalID, _ := openQuakeTerminal(t, svc, d, dir)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.IsQuake)
	assert.Equal(t, dir, row.WorkingDir, "the directory is the address, so it must be stored verbatim")
}

func TestOpenTerminal_WithoutQuakeIsUnchanged(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.EqualValues(t, 0, row.IsQuake, "an ordinary terminal tab is not a quake terminal")
}

// The second toggle, and the second DEVICE. Both must land on one PTY.
func TestOpenTerminal_SecondOpenForOneDirectoryReturnsTheSameTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	first, firstTitle := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(first) }, "spawn")

	second, secondTitle := openQuakeTerminal(t, svc, d, dir)

	assert.Equal(t, first, second, "one directory has at most one live quake terminal")
	assert.Equal(t, firstTitle, secondTitle, "the second caller reads the stored title")

	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the duplicate open must create no second row")
}

// The headline property of the directory-keyed model: two AGENT TABS that work
// in one directory reach one shell. Keyed on the tab -- as this used to be --
// each of them would get its own PTY, and a command typed in one would be
// invisible in the other.
func TestOpenTerminal_TwoAgentsInOneDirectoryShareOneQuakeTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createAgentForPath(t, svc, "agent-2", dir)

	// Nothing in either call names an agent, which IS the change: the request
	// carries the directory and nothing else.
	first, _ := openQuakeTerminal(t, svc, d, dir)
	second, _ := openQuakeTerminal(t, svc, d, dir)

	assert.Equal(t, first, second, "one directory, one shell, however many tabs work there")
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1)
}

// The other half of the rule: two DIRECTORIES are two shells, even when the
// same user opens both from the same workspace.
func TestOpenTerminal_TwoDirectoriesGetTwoQuakeTerminals(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	root := t.TempDir()
	dirA := filepath.Join(root, "a")
	dirB := filepath.Join(root, "b")
	require.NoError(t, os.MkdirAll(dirA, 0o755))
	require.NoError(t, os.MkdirAll(dirB, 0o755))
	createAgentForPath(t, svc, "agent-a", dirA)
	createAgentForPath(t, svc, "agent-b", dirB)

	quakeA, _ := openQuakeTerminal(t, svc, d, dirA)
	quakeB, _ := openQuakeTerminal(t, svc, d, dirB)

	assert.NotEqual(t, quakeA, quakeB, "the directory is the address, so two of them are two shells")
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 2)
}

// Two devices toggling at the same instant. The lookup-then-insert has a
// window, and the unique partial index is what closes it: one insert wins, the
// loser re-reads and answers with the winner's id.
func TestOpenTerminal_ConcurrentOpensForOneDirectoryYieldOneTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	const callers = 4
	ids := make([]string, callers)
	var wg sync.WaitGroup
	var start sync.WaitGroup
	start.Add(1)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			start.Wait()
			id, _ := openQuakeTerminal(t, svc, d, dir)
			ids[i] = id
		}()
	}
	start.Done()
	wg.Wait()

	for _, id := range ids {
		assert.Equal(t, ids[0], id, "every caller must attach to one shell")
	}
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the unique index must leave exactly one live quake terminal")
}

// The index is scoped to OPEN rows, so a shell the user exited does not block
// the next one.
func TestOpenTerminal_AfterAQuakeTerminalClosesANewOneCanOpen(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	first, _ := openQuakeTerminal(t, svc, d, dir)
	_, err := svc.Queries.CloseTerminal(context.Background(), first)
	require.NoError(t, err)

	second, _ := openQuakeTerminal(t, svc, d, dir)

	assert.NotEqual(t, first, second, "the next open must start a fresh shell")
}

// A terminal TAB survives its shell -- the row stays open and Enter respawns it.
// A quake terminal has no such contract, so its exit closes the row. Without
// this the unique index would refuse the next one and the next toggle would
// adopt a dead PTY.
func TestQuakeTerminal_ShellExitClosesTheRow(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	// NOT through exitTerminalAndWait: that waits for the manager to report the
	// terminal EXITED, and closing a quake terminal REMOVES it from the
	// manager. The row is the thing to wait on, and it is also the thing under
	// test.
	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
		return err == nil && row.ClosedAt.Valid
	}, "the quake row closes on exit")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the quake terminal leaves the manager too")
}

// The mirror, and the reason the branch is keyed on is_quake: an ordinary
// terminal tab must keep its "press Enter to restart" contract.
func TestTerminalTab_ShellExitLeavesTheRowOpen(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	exitTerminalAndWait(t, svc, d, terminalID, "")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "an ordinary terminal survives its shell")
}

func TestListTerminals_ResolvesAQuakeTerminalByItsWorkingDir(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)

	w := newTestWriter()
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{dir}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	require.Len(t, resp.GetTerminals(), 1)
	assert.Equal(t, terminalID, resp.GetTerminals()[0].GetTerminalId())
	assert.True(t, resp.GetTerminals()[0].GetQuake())
	// A directory with no quake terminal is an ABSENCE, not a failed hydration
	// -- the client asks precisely to find out whether one exists.
	assert.Empty(t, resp.GetVerdicts(), "a directory lookup answers no verdicts")
}

func TestListTerminals_ADirectoryWithNoQuakeTerminalAnswersEmpty(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{t.TempDir()}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Empty(t, resp.GetTerminals())
	assert.Empty(t, resp.GetVerdicts())
}

// A quake terminal is TERMINAL data now, so terminal:read alone resolves one.
// It used to carry the owning agent's id, which is why the lookup took
// agent:read as well; there is no agent id on this reply any more.
func TestListTerminals_ResolvesAQuakeTerminalWithTerminalReadAlone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read"), "ListTerminals",
		&leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{dir}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	require.Len(t, resp.GetTerminals(), 1)
	assert.Equal(t, terminalID, resp.GetTerminals()[0].GetTerminalId())
	assert.True(t, resp.GetTerminals()[0].GetQuake())
}

// Closing the last tab in a directory ends its quake terminal, and it happens
// in the SHARED teardown so every close path carries it -- the online RPC, the
// reconciler's reap, and the deleted-workspace sweep.
func TestCloseAgent_ClosesTheQuakeTerminalOfTheLastTabInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the last tab in the directory takes the shell with it")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the PTY must be reaped, not merely marked")
}

// The counterpart, and the whole point of keying on the directory: a shell that
// two tabs share must survive the first of them closing. Keyed on the tab this
// close would have killed a terminal the other tab is still typing in.
func TestCloseAgent_KeepsTheQuakeTerminalWhileAnotherTabWorksInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createAgentForPath(t, svc, "agent-2", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "agent-2 still works here, so the shell stays")
	assert.True(t, svc.Terminals.HasTerminal(terminalID), "and its PTY stays with it")
}

// A TERMINAL TAB is a reference to its directory too, so closing the last agent
// while one is open keeps the shell -- and closing that terminal tab afterwards
// is what finally ends it. Both halves run through closeTerminalTabCommon,
// which is also what closes the quake terminal itself: the is_quake guard there
// is what stops that recursing.
func TestCloseTerminalTab_ClosesTheQuakeTerminalOfTheLastTabInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	tabID := openTerminalViaRPC(t, svc, d, w, dir)
	quakeID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(quakeID) }, "spawn")

	closeAgent := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-1"}, closeAgent)
	require.Empty(t, closeAgent.errors)

	kept, err := svc.Queries.GetTerminal(context.Background(), quakeID)
	require.NoError(t, err)
	require.False(t, kept.ClosedAt.Valid, "the terminal tab still references the directory")

	closeTab := newTestWriter()
	dispatch(d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{TerminalId: tabID}, closeTab)
	require.Empty(t, closeTab.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), quakeID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the last reference is gone, so the shell goes")
}

// The convergence path, which is what a reconciler reap and a deleted workspace
// both take. Putting the reap in the RPC handler instead would have left these
// weaker than the online close.
func TestCloseAgentForConvergence_ClosesTheQuakeTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", "agent-1")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "an offline close must reap the shell too")
}

// A subagent transcript owns no process and no tab of its own. Its close is
// UI-only and must run no teardown -- and it is not a reference to the
// directory either, so the root's quake terminal is untouched twice over.
func TestCloseChildAgent_LeavesTheQuakeTerminalAlone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	require.NoError(t, svc.Queries.CreateChildAgent(context.Background(), db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "agent-1", Valid: true},
		WorkingDir:    dir,
		HomeDir:       dir,
	}))

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "child-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "a subagent close must not end the directory's shell")
}

// A quake terminal is addressed by the directory it is IN, so a git mode that
// would move that directory is refused at the one write point of is_quake.
// Without it the row would store the worktree path and the panel that asked for
// the shell could never find it again.
func TestOpenTerminal_RefusesAQuakeTerminalWithAGitMode(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()

	w := newTestWriter()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:          testutil.TestShell(),
		WorkingDir:     dir,
		Quake:          true,
		CreateWorktree: true,
		WorktreeBranch: "feature/x",
	}, w)

	require.Len(t, w.errors, 1, "a quake terminal must not create a worktree")
	rows, err := svc.Queries.ListOpenQuakeTerminals(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rows, "the refusal must create no row and no shell")
}

// A worker RESTART leaves a quake row open with no PTY behind it. Adopting that
// row hands the user a panel that paints nothing and swallows every keystroke,
// and a quake terminal refuses the Enter that restarts a terminal TAB -- so the
// panel would stay dead for as long as a tab works in that directory, across
// reloads. The reconciler cannot reap it either: it measures a quake terminal by
// whether any open tab still works there, and one does.
func TestOpenTerminal_DoesNotAdoptAQuakeTerminalWhoseShellIsGone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	// The shape a restart leaves behind: an open row this process never hosted.
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         "stale-quake",
		WorkingDir: dir,
		HomeDir:    dir,
		Shell:      testutil.TestShell(),
		Title:      "Terminal Ghost",
		Screen:     []byte{},
		IsQuake:    1,
	}))

	fresh, _ := openQuakeTerminal(t, svc, d, dir)

	assert.NotEqual(t, "stale-quake", fresh, "a row with no live shell must not be adopted")
	stale, err := svc.Queries.GetTerminal(context.Background(), "stale-quake")
	require.NoError(t, err)
	assert.True(t, stale.ClosedAt.Valid,
		"the corpse must be closed, or the unique index refuses every replacement")
}

// The boot sweep is what makes the property above hold WITHOUT waiting for a
// user to toggle the panel: a quake row is valid only while this process hosts
// its PTY.
func TestCloseOrphanedQuakeTerminals_ClosesARowWithNoLiveShell(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	root := t.TempDir()
	orphanDir := filepath.Join(root, "orphan")
	liveDir := filepath.Join(root, "live")
	require.NoError(t, os.MkdirAll(orphanDir, 0o755))
	require.NoError(t, os.MkdirAll(liveDir, 0o755))
	createAgentForPath(t, svc, "agent-1", orphanDir)
	createAgentForPath(t, svc, "agent-2", liveDir)

	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         "orphan-quake",
		WorkingDir: orphanDir,
		HomeDir:    orphanDir,
		Shell:      testutil.TestShell(),
		Screen:     []byte{},
		IsQuake:    1,
	}))
	live, _ := openQuakeTerminal(t, svc, d, liveDir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(live) }, "spawn")
	plainTab := openTerminalViaRPC(t, svc, d, newTestWriter(), liveDir)

	svc.CloseOrphanedQuakeTerminals(context.Background())

	orphan, err := svc.Queries.GetTerminal(context.Background(), "orphan-quake")
	require.NoError(t, err)
	assert.True(t, orphan.ClosedAt.Valid, "a quake terminal this process does not host must close")

	liveRow, err := svc.Queries.GetTerminal(context.Background(), live)
	require.NoError(t, err)
	assert.False(t, liveRow.ClosedAt.Valid, "a quake terminal whose shell is running must survive")

	tabRow, err := svc.Queries.GetTerminal(context.Background(), plainTab)
	require.NoError(t, err)
	assert.False(t, tabRow.ClosedAt.Valid, "an ordinary terminal tab is not a quake terminal and is untouched")
}

// A quake terminal has no restart contract, and the worker is where that rule
// has to live: a respawn would resurrect a row the exit path already closed, and
// the unique index would then refuse every replacement.
func TestRestartTerminal_RefusesAQuakeTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	// A row with no live shell, which is the only state a restart could ever
	// reach: the quake terminal's own exit closes the row a moment later, and
	// this is the window in between.
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:         "exited-quake",
		WorkingDir: dir,
		HomeDir:    dir,
		Shell:      testutil.TestShell(),
		Screen:     []byte{},
		IsQuake:    1,
	}))

	w := newTestWriter()
	dispatch(d, "RestartTerminal", &leapmuxv1.RestartTerminalRequest{TerminalId: "exited-quake"}, w)

	require.Len(t, w.errors, 1, "a quake terminal must refuse a restart")
	assert.Contains(t, w.errors[0].message, "quake terminal")
	assert.False(t, svc.Terminals.HasTerminal("exited-quake"), "the refusal must spawn nothing")
}

// The "[Terminal process exited - Press Enter to restart]" notice reaches an
// open panel as ordinary terminal data, and both the browser and
// RestartTerminal then decline the Enter it invites. A quake terminal therefore
// gets no notice, so the panel retracts on a clean screen rather than on a dead
// offer.
func TestQuakeTerminal_ShellExitPaintsNoRestartNotice(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
		return err == nil && row.ClosedAt.Valid
	}, "the quake row closes on exit")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.Screen), "Press Enter to restart",
		"a quake terminal refuses that Enter, so it must not offer it")
}

// A quake terminal takes NO archive flag. It is not a tab, so the Hub never
// lists one, and it has no restart contract -- so there is no state an
// unarchive could restore. Archiving the last live tab in its directory
// therefore CLOSES it, and the next open there spawns a fresh shell.
func TestApplyTabArchiveState_ClosesTheQuakeTerminalOfAnArchivedDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabId: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	)
	require.NoError(t, err)

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid,
		"nobody can reach the panel of a directory whose every tab is archived")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the PTY goes with it")
}

// The case that made the archive expansion complicated, now answered by the
// reference count instead. A shell keyed on the DIRECTORY can be reached from
// tabs in several workspaces, so archiving one workspace must not stop the
// shell another one is still typing into. An archived tab simply stops counting
// as a reference; a live one anywhere keeps the shell.
func TestApplyTabArchiveState_KeepsTheQuakeTerminalWhileALiveTabRemains(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createAgentForPath(t, svc, "agent-2", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabId: "agent-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	)
	require.NoError(t, err)

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid,
		"agent-2 is still live in this directory, so its shell must survive")

	// Archive the second one too, and the directory loses its last reference.
	_, err = svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabId: "agent-2", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	)
	require.NoError(t, err)

	row, err = svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid,
		"the last live tab in the directory took the shell with it")
}

// An EMPTY directory must not act as a wildcard, and it must not resolve to the
// worker's HOME either: normalizeWorkingDir turns "" into the home dir, so the
// empty entry has to be dropped before it is normalized.
func TestListTerminals_IgnoresAnEmptyQuakeWorkingDir(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	openTerminalViaRPC(t, svc, d, w, dir)

	reply := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read"), "ListTerminals",
		&leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{""}}, reply)

	require.Empty(t, reply.errors)
	require.Len(t, reply.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(reply.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals(), "an empty directory names no quake terminal")
}

// SetQuakePanel names a DIRECTORY, not a tab, so terminal:write is the whole
// gate. It used to take agent:read too, because the request carried an agent id
// and the reply told a known one from an unknown one.
func TestSetQuakePanel_IsServedWithTerminalWriteAlone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	req := &leapmuxv1.SetQuakePanelRequest{
		WorkingDir: t.TempDir(),
		Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE,
	}

	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:write"), "SetQuakePanel", req, w)
	assert.Empty(t, w.errors, "a caller that writes terminals is served")
}

// The directory has to be spelled the way OpenTerminal stores it, or a frontend
// comparing the event against its focused tab's working dir would never match.
func TestSetQuakePanel_RefusesADirectoryItCannotNormalize(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, "SetQuakePanel", &leapmuxv1.SetQuakePanelRequest{
		WorkingDir: "relative/path",
		Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "absolute")
}

func TestSetQuakePanel_RefusesAnUnspecifiedAction(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, "SetQuakePanel", &leapmuxv1.SetQuakePanelRequest{WorkingDir: t.TempDir()}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "open, close or toggle")
}
