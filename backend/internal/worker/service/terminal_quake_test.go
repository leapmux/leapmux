package service

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/util/userid"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

// A QUAKE terminal is the shell behind the quake panel, and it belongs to a
// working DIRECTORY on this worker rather than to any one tab. These tests pin
// the properties every device and every tab depend on: at most one live quake
// terminal per directory, the same one handed to whoever asks second -- from
// another device OR from another tab in that directory -- and the shell
// outliving every tab but the last.

// openQuakeTerminal runs OpenTerminal with quake set and returns the terminal
// id and the stored title.
func openQuakeTerminal(t *testing.T, svc *Service, d testRPCDispatcher, workingDir string) (string, string) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: workingDir,
		Target:     &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
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

	quake, ok := svc.QuakeTerminals.Get(terminalID)
	require.True(t, ok, "quake terminal must live in QuakeTerminals")
	assert.Equal(t, dir, quake.WorkingDir, "the directory is the address, so it must be stored verbatim")
	_, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	assert.ErrorIs(t, err, sql.ErrNoRows, "a quake terminal must write no SQLite row")
}

func TestOpenTerminal_WithoutQuakeIsUnchanged(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	_, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.False(t, svc.QuakeTerminals.Has(terminalID), "an ordinary terminal tab is not a quake terminal")
}

// The second toggle, and the second DEVICE. Both must land on one PTY.
func TestOpenTerminal_SecondOpenForOneDirectoryReturnsTheSameTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	first, firstTitle := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, first, "spawn")

	second, secondTitle := openQuakeTerminal(t, svc, d, dir)

	assert.Equal(t, first, second, "one directory has at most one live quake terminal")
	assert.Equal(t, firstTitle, secondTitle, "the second caller reads the stored title")
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 1, "the duplicate open must create no second entry")
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

	// Neither call mentions an agent, which IS the change: the request
	// carries the directory and nothing else.
	first, _ := openQuakeTerminal(t, svc, d, dir)
	second, _ := openQuakeTerminal(t, svc, d, dir)

	assert.Equal(t, first, second, "one directory, one shell, however many tabs work there")
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 1)
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
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 2)
}

// Two devices toggling at the same instant. RegisterOrAdopt serializes
// per-directory ownership: one caller wins and the losers adopt the winner's id.
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
	assert.Len(t, svc.QuakeTerminals.ListOpen(), 1, "RegisterOrAdopt must leave exactly one live quake terminal")
}

// Deleting a closed quake entry frees its directory so the next open starts fresh.
func TestOpenTerminal_AfterAQuakeTerminalClosesANewOneCanOpen(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	first, _ := openQuakeTerminal(t, svc, d, dir)
	svc.closeQuakeTerminal(first)

	second, _ := openQuakeTerminal(t, svc, d, dir)

	assert.NotEqual(t, first, second, "the next open must start a fresh shell")
}

// A terminal TAB survives its shell -- the row stays open and Enter respawns it.
// A quake terminal has no such contract, so its exit removes it from QuakeTerminals.
func TestQuakeTerminal_ShellExitClosesTheRow(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		return !svc.QuakeTerminals.Has(terminalID)
	}, "the quake entry is deleted on exit")
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
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{dir}}, w)
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
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{t.TempDir()}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Empty(t, resp.GetTerminals())
	assert.Empty(t, resp.GetVerdicts())
}

// A directory-keyed lookup needs file:read ON TOP of this handler's
// terminal:read, and the browser holds both.
//
// A quake terminal has no CRDT tab, so its id never reaches the hub's tab list
// and no worker RPC enumerates terminals -- before this, a terminal:read caller
// could not name one at all. Keyed on a path it can guess, it could ask whether
// any absolute directory has a live shell and read that shell's screen.
// WatchWorkerPrivateEvents already requires file:read to deliver the same
// working_dir, so this is what stops the two surfaces pricing it differently.
func TestListTerminals_ResolvesAQuakeTerminalWithFileReadAndTerminalRead(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read file:read"), leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure,
		&leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{dir}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	require.Len(t, resp.GetTerminals(), 1)
	assert.Equal(t, terminalID, resp.GetTerminals()[0].GetTerminalId())
	assert.True(t, resp.GetTerminals()[0].GetQuake())
}

// The refusal half: terminal:read alone must not answer whether an arbitrary
// absolute path has a live shell.
func TestListTerminals_RefusesADirectoryProbeWithoutFileRead(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read"), leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure,
		&leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{dir}}, w)
	require.Empty(t, w.errors, "the request is answered, with the probe simply unanswered")
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Empty(t, resp.GetTerminals(),
		"a caller with no file:read learns nothing about the directory")
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
	require.Empty(t, w.errors)

	assert.False(t, svc.QuakeTerminals.Has(terminalID), "the last tab in the directory takes the shell with it")
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
	require.Empty(t, w.errors)

	assert.True(t, svc.QuakeTerminals.Has(terminalID), "agent-2 still works here, so the shell stays")
	assert.True(t, svc.Terminals.HasTerminal(terminalID), "and its PTY stays with it")
}

// A TERMINAL TAB is a reference to its directory too, so closing the last agent
// while one is open keeps the shell -- and closing that terminal tab afterwards
// is what finally ends it.
func TestCloseTerminalTab_ClosesTheQuakeTerminalOfTheLastTabInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	tabID := openTerminalViaRPC(t, svc, d, w, dir)
	quakeID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, quakeID, "spawn")

	closeAgent := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, closeAgent)
	require.Empty(t, closeAgent.errors)

	require.True(t, svc.QuakeTerminals.Has(quakeID), "the terminal tab still refers to the directory")

	closeTab := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: terminalTabIdent(tabID)}, closeTab)
	require.Empty(t, closeTab.errors)

	assert.False(t, svc.QuakeTerminals.Has(quakeID), "the last reference is gone, so the shell goes")
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", "agent-1")

	assert.False(t, svc.QuakeTerminals.Has(terminalID), "an offline close must reap the shell too")
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
	require.NoError(t, svc.Queries.CreateChildAgent(context.Background(), db.CreateChildAgentParams{AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "agent-1", Valid: true},
		WorkingDir:    dir,
	}))

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("child-1")}, w)
	require.Empty(t, w.errors)

	assert.True(t, svc.QuakeTerminals.Has(terminalID), "a subagent close must not end the directory's shell")
}

// A quake terminal is addressed by the directory it is IN, so a git mode that
// would move that directory is refused.
func TestOpenTerminal_RefusesAQuakeTerminalWithAGitMode(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell:      testutil.TestShell(),
		WorkingDir: dir,
		Target:     &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
		GitSetup:   createWorktreeSetup("feature/x", ""),
	}, w)

	require.Len(t, w.errors, 1, "a quake terminal must not create a worktree")
	assert.Empty(t, svc.QuakeTerminals.ListOpen(), "the refusal must create no entry and no shell")
}

// Adopting an in-memory quake entry whose shell is gone hands the user a dead
// panel; OpenTerminal replaces it with a fresh shell instead.
func TestOpenTerminal_DoesNotAdoptAQuakeTerminalWhoseShellIsGone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID:         "stale-quake",
		WorkingDir: dir,
		Shell:      testutil.TestShell(),
		Title:      "Terminal Ghost",
	})
	require.True(t, created)

	fresh, _ := openQuakeTerminal(t, svc, d, dir)

	assert.NotEqual(t, "stale-quake", fresh, "an entry with no live shell must not be adopted")
	assert.False(t, svc.QuakeTerminals.Has("stale-quake"), "the dead entry must be removed")
	assert.True(t, svc.QuakeTerminals.Has(fresh))
}

// A quake terminal has no restart contract, and the worker refuses RestartTerminal
// with FailedPrecondition.
func TestRestartTerminal_RefusesAQuakeTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID:         "exited-quake",
		WorkingDir: dir,
		Shell:      testutil.TestShell(),
	})
	require.True(t, created)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, &leapmuxv1.RestartTerminalRequest{TerminalId: "exited-quake"}, w)

	require.Len(t, w.errors, 1, "a quake terminal must refuse a restart")
	assert.Equal(t, codeFailedPrecondition, w.errors[0].code)
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	watchWriter := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: terminalID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, watchWriter)
	ack := waitWatchUpdateAck(t, watchWriter)
	require.Empty(t, ack.GetRejectedTerminals(), "WatchEvents must attach cleanly to the quake terminal")

	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		return !svc.QuakeTerminals.Has(terminalID)
	}, "the quake entry is deleted on exit")

	for _, s := range watchWriter.streamsSnapshot() {
		var ev leapmuxv1.WatchEventsResponse
		if err := proto.Unmarshal(s.GetPayload(), &ev); err != nil {
			continue
		}
		if out := ev.GetTerminalEvent().GetData(); out != nil {
			assert.NotContains(t, string(out.GetData()), "Press Enter to restart",
				"a quake terminal refuses that Enter, so it must not offer it")
		}
	}
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		testTabOwner,
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}},
	)
	require.NoError(t, err)

	assert.False(t, svc.QuakeTerminals.Has(terminalID),
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
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		testTabOwner,
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}},
	)
	require.NoError(t, err)

	assert.True(t, svc.QuakeTerminals.Has(terminalID),
		"agent-2 is still live in this directory, so its shell must survive")

	// Archive the second one too, and the directory loses its last reference.
	_, err = svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		testTabOwner,
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-2"}},
	)
	require.NoError(t, err)

	assert.False(t, svc.QuakeTerminals.Has(terminalID),
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
	dispatchScoped(d, mustScopes("worker:read terminal:read"), leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure,
		&leapmuxv1.ListTerminalsRequest{QuakeWorkingDirs: []string{""}}, reply)

	require.Empty(t, reply.errors)
	require.Len(t, reply.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(reply.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals(), "an empty directory addresses no quake terminal")
}

// SetQuakePanel specifies a DIRECTORY, not a tab, so terminal:write is the whole
// gate. It used to take agent:read too, because the request carried an agent id
// and the reply told a known one from an unknown one.
func TestSetQuakePanel_IsServedWithTerminalWriteAlone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	// A real tab there, because an OPENING command for a directory no tab works
	// in is refused on its own merits -- see the refusal test below.
	createAgentForPath(t, svc, "agent-1", dir)

	req := &leapmuxv1.SetQuakePanelRequest{
		WorkingDir: dir,
		Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE,
	}

	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:write"), leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, req, w)
	assert.Empty(t, w.errors, "a caller that writes terminals is served")
}

// The directory has to be spelled the way OpenTerminal stores it, or a frontend
// comparing the event against its focused tab's working dir would never match.
func TestSetQuakePanel_RefusesADirectoryItCannotNormalize(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, &leapmuxv1.SetQuakePanelRequest{
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
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, &leapmuxv1.SetQuakePanelRequest{WorkingDir: t.TempDir()}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "open, close or toggle")
}

// A FILE tab is a reference to its directory exactly as an agent or a terminal
// tab is: the panel opens over one, so the shell must outlive the agent that
// started it while a viewer is still open there.
//
// The worker used to count root agents and terminal tabs only, which made it
// disagree with the browser -- `quakeKeyForTab` accepts any tab carrying a
// worker and a directory -- and the reap closed a shell the user was typing in.
func TestCloseAgent_KeepsTheQuakeTerminalWhileAFileTabWorksInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createFileTabForPath(t, svc, "user-1", "file-1", dir, "open.txt")
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, w)
	require.Empty(t, w.errors)

	assert.True(t, svc.QuakeTerminals.Has(terminalID), "the file tab still works here, so the shell stays")
	assert.True(t, svc.Terminals.HasTerminal(terminalID), "and its PTY stays with it")
}

// The other half: closing the LAST viewer is what ends the shell. The payload
// close path had no reap at all, so this case waited for the orphan
// reconciler -- an hour by default.
func TestCloseTab_FileTabClosesTheQuakeTerminalOfTheLastTabInTheDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createFileTabForPath(t, svc, "user-1", "file-1", dir, "open.txt")
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	closeAgent := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: agentTabIdent("agent-1")}, closeAgent)
	require.Empty(t, closeAgent.errors)
	require.True(t, svc.QuakeTerminals.Has(terminalID), "the file tab still refers to the directory")

	revoke := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{Tab: fileTabIdent("file-1")}, revoke)
	require.Empty(t, revoke.errors)

	assert.False(t, svc.QuakeTerminals.Has(terminalID), "the last reference is gone, so the shell goes")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the PTY is reaped, not merely marked")
}

// An ARCHIVED viewer is not a live reference, exactly as an archived agent tab
// is not. Nobody can reach the panel from a tab in an archived workspace, so a
// directory whose every tab is archived has nobody left to type into its shell.
//
// This is what worker_tab_payloads.workspace_archived buys, and the flag needs
// an OWNER: the table is keyed (user_id, tab_id) because a payload-backed tab
// id is minted client-side and unique only within one account. The owner
// argument is what carries it.
func TestApplyTabArchiveState_AnArchivedFileTabStopsHoldingTheQuakeTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	createFileTabForPath(t, svc, "user-1", "file-1", dir, "open.txt")
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		userid.MustNew("user-1"),
		[]*leapmuxv1.TabIdent{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-1"},
		},
	)
	require.NoError(t, err)

	payload, err := svc.Queries.GetWorkerTabPayload(ctx, db.GetWorkerTabPayloadParams{UserID: "user-1", TabID: "file-1"})
	require.NoError(t, err)
	assert.True(t, payload.WorkspaceArchived, "the flag is what makes the viewer stop counting")

	assert.False(t, svc.QuakeTerminals.Has(terminalID), "every tab in the directory is archived, so the shell ends")
}

// A payload-backed tab id is unique only within one account, so the archive
// REFUSES one with no owner rather than writing a blank user_id -- which would
// match no row on a write and another account's row on a read.
func TestApplyTabArchiveState_RefusesAPayloadTabWithNoOwner(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	_, err := svc.ApplyTabArchiveState(context.Background(),
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		userid.UserID{},
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "file-1"}},
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "needs an owner")
}

// The archive sweep must act on the directories THIS request changed and no
// others. An unscoped whole-worker sweep closed a shell in a directory the
// request never mentioned -- immediately, ahead of the grace window the orphan
// reconciler gives the same row.
func TestApplyTabArchiveState_LeavesAQuakeTerminalInAnUnrelatedDirectory(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	archivedDir := t.TempDir()
	otherDir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", archivedDir)
	// The other directory holds a quake terminal and NO counted tab, which is
	// exactly the shape the unscoped sweep reaped. The reconciler reaps it
	// later, after its grace window; this RPC must not.
	otherQuake, _ := openQuakeTerminal(t, svc, d, otherDir)
	waitForTerminalSpawn(t, svc, otherQuake, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		testTabOwner,
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-1"}},
	)
	require.NoError(t, err)

	assert.True(t, svc.QuakeTerminals.Has(otherQuake),
		"this request said nothing about that directory, so it must not reap its shell")
}

// A pass that flips no flag must do no work at all. The reconciler calls
// ApplyTabArchiveState on nearly every pass of a busy worker, so a sweep that
// ran regardless was both wasted and, before it was scoped, destructive.
func TestApplyTabArchiveState_NoFlagChangeSweepsNothing(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	dir := t.TempDir()
	quakeID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, quakeID, "spawn")

	// A tab id no row matches: persistArchiveFlags reports zero changes.
	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		testTabOwner,
		[]*leapmuxv1.TabIdent{{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "agent-absent"}},
	)
	require.NoError(t, err)

	assert.True(t, svc.QuakeTerminals.Has(quakeID), "nothing changed, so nothing is reaped")
}

// working_dir is the ADDRESS of a quake terminal, not a field with a sensible
// default. normalizeWorkingDir resolves an empty path to the worker's home
// directory, so an unset field silently claimed the singleton row of $HOME and
// held it against the real one.
func TestOpenTerminal_RefusesAQuakeTerminalWithNoWorkingDir(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceOpenTerminalProcedure, &leapmuxv1.OpenTerminalRequest{
		Shell:  testutil.TestShell(),
		Target: &leapmuxv1.OpenTerminalRequest_Quake{Quake: &leapmuxv1.QuakeTerminalTarget{}},
		Cols:   200,
		Rows:   24,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "working_dir")
	assert.Empty(t, w.responses, "no terminal is minted for an address that names nothing")
}

// The sibling refusal, and the reason ListTerminals drops an empty entry
// too: an unset directory must not become a command against $HOME's panel,
// broadcast to every frontend of the account.
func TestSetQuakePanel_RefusesAnEmptyWorkingDir(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, &leapmuxv1.SetQuakePanelRequest{
		Action: leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "working_dir")
}

// dirHasLiveTab excludes one (kind, ID) pair. A live tab of another kind
// with the same ID must continue to keep the directory in use.
func TestDirHasLiveTab_ExcludesOneTabRatherThanEveryTabSharingItsID(t *testing.T) {
	t.Parallel()

	// The FILE and AGENT rows share an ID. Only the closing FILE row may be excluded.
	tabs := []dirTabRef{
		{Kind: leapmuxv1.TabType_TAB_TYPE_FILE, ID: "file-1", WorkingDir: "/repo"},
		{Kind: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "file-1", WorkingDir: "/repo"},
	}

	closing := tabRefKey(leapmuxv1.TabType_TAB_TYPE_FILE, "file-1")
	assert.True(t, dirHasLiveTab(tabs, closing),
		"the agent row shares the id but is a different tab, so the directory is still in use")

	assert.False(t, dirHasLiveTab(tabs[:1], closing),
		"and the closing tab itself is still excluded")
}

// An archived tab is not a live reference, whatever its kind.
func TestDirHasLiveTab_AnArchivedTabIsNotAReference(t *testing.T) {
	t.Parallel()

	tabs := []dirTabRef{
		{Kind: leapmuxv1.TabType_TAB_TYPE_AGENT, ID: "a1", WorkingDir: "/repo", WorkspaceArchived: true},
	}
	assert.False(t, dirHasLiveTab(tabs, ""), "nobody can reach the panel from an archived tab")

	tabs = append(tabs, dirTabRef{Kind: leapmuxv1.TabType_TAB_TYPE_FILE, ID: "f1", WorkingDir: "/repo"})
	assert.True(t, dirHasLiveTab(tabs, ""), "the live viewer beside it still counts")
}

// An OPENING command for a directory no tab works in is refused, because no
// client could act on it: every frontend resolves the event back to a tab and
// needs one to decide the workspace a cold open is refused in. Reporting
// success made a typo'd --working-dir indistinguishable from a real invocation.
func TestSetQuakePanel_RefusesAnOpeningCommandForADirectoryWithNoTab(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, &leapmuxv1.SetQuakePanelRequest{
		WorkingDir: t.TempDir(),
		Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_OPEN,
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "no tab works in that directory")
}

// CLOSE is exempt, and deliberately: it needs only the key, and it is the one
// action that can act on a directory whose tabs the calling client cannot see.
// On a phone it is the only way to dismiss a panel covering the centre area.
func TestSetQuakePanel_AllowsACloseForADirectoryWithNoTab(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerPrivateServiceSetQuakePanelProcedure, &leapmuxv1.SetQuakePanelRequest{
		WorkingDir: t.TempDir(),
		Action:     leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_CLOSE,
	}, w)

	assert.Empty(t, w.errors, "a close must never be refused for want of a tab")
}

// The payload row read is owner-guarded, and the miss returns EARLY.
//
// worker_tab_payloads is keyed (user_id, tab_id), so a zero owner unwraps to ""
// and would MATCH every blank-owner row instead of none. "" is the honest
// answer here: the quake reap treats it as "no directory to ask about", and the
// orphan reconciler re-asks on its next pass.
func TestPayloadTabWorkingDir_RefusesAnUnmintableOwner(t *testing.T) {
	t.Parallel()

	svc, _, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createFileTabForPath(t, svc, "user-1", "file-1", dir, "open.txt")

	assert.Equal(t, dir, svc.payloadTabWorkingDir("user-1", "file-1"),
		"the owner that holds the row reads it")
	assert.Empty(t, svc.payloadTabWorkingDir("", "file-1"),
		"a blank owner must match nothing, not every blank-owner row")
}

// The quake close runs on a counted goroutine: the service cleanup join must
// return only after it finished, so the caller's next read sees the row
// closed without polling.
func TestQuakeCloseIsJoinedByServiceCleanup(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	svc.closeQuakeTerminalOnShellExit(terminalID)
	svc.Output.SetShuttingDown()
	<-svc.Cleanup.DoneChan()

	assert.False(t, svc.QuakeTerminals.Has(terminalID),
		"the joined close must have removed the quake entry before the service cleanup join returned")
}

// A shutdown that already began admits no async quake close; the in-memory entry
// disappears automatically when a fresh Service starts on the same database.
func TestCloseQuakeTerminalLeavesEntryAfterShutdownBeginsAndRestartStartsClean(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)
	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")
	svc.lifecycleMu.Lock()
	svc.shuttingDown.Store(true)
	svc.lifecycleMu.Unlock()
	svc.Output.SetShuttingDown()
	svc.Terminals.StopTerminal(terminalID)
	require.NoError(t, svc.Terminals.WaitForExitContext(testutil.DeadlineContext(t), terminalID))
	require.True(t, svc.Terminals.IsExited(terminalID))
	<-svc.Cleanup.DoneChan()

	assert.True(t, svc.QuakeTerminals.Has(terminalID), "a refused close leaves the in-memory entry in the dying process")

	restarted := New(svc.Config)
	t.Cleanup(restarted.InputQueue.StopAndWait)
	assert.Empty(t, restarted.QuakeTerminals.ListOpen(), "a restarted Worker starts with zero quake terminals")
}

func TestQuakeCleanupAdmissionAndShutdownJoin(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		svc, _, _ := setupTestService(t)
		const terminalID = "held-quake-cleanup"
		_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: terminalID, WorkingDir: t.TempDir(), Cols: 80, Rows: 24,
		})
		require.True(t, created)
		entered, release := make(chan struct{}), make(chan struct{})
		finish := sync.OnceFunc(func() { close(release) })
		defer finish()
		svc.terminalCleanups.register(terminalID, func() { close(entered); <-release })
		svc.closeQuakeTerminalOnShellExit(terminalID)
		<-entered
		done := make(chan struct{})
		go func() { svc.Shutdown(); close(done) }()
		synctest.Wait()
		admitted := svc.beginCleanup()
		if admitted {
			svc.Cleanup.Done()
		}
		assert.False(t, admitted, "shutdown must refuse later cleanup admission")
		select {
		case <-done:
			t.Fatal("shutdown returned before admitted cleanup finished")
		default:
		}
		assert.True(t, svc.QuakeTerminals.Has(terminalID), "in-memory entry stays until cleanup completes")
		finish()
		<-done
		assert.False(t, svc.QuakeTerminals.Has(terminalID))
	})
}

func TestQuakeTerminal_ZeroSQLiteRowsAndCleanRestart(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	// Hold spawn so we can exercise RenameTerminal and ListTerminals while STARTING.
	spawnStarted := make(chan struct{})
	releaseSpawn := make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(releaseSpawn) })
	defer releaseOnce()
	svc.startTerminalFn = func(startCtx context.Context, opts terminal.Options, onOutput terminal.OutputHandler, onExit terminal.ExitHandler) error {
		close(spawnStarted)
		<-releaseSpawn
		return svc.Terminals.StartTerminal(startCtx, opts, onOutput, onExit)
	}

	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	<-spawnStarted

	// Rename while starting and verify ListTerminals reports STARTING + renamed title.
	wRename := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceRenameTerminalProcedure, &leapmuxv1.RenameTerminalRequest{
		TerminalId: terminalID,
		Title:      "Quake Renamed",
	}, wRename)
	require.Empty(t, wRename.errors)

	wListStarting := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{
		QuakeWorkingDirs: []string{dir},
	}, wListStarting)
	require.Empty(t, wListStarting.errors)
	require.Len(t, wListStarting.responses, 1)
	var startingResp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(wListStarting.responses[0].GetPayload(), &startingResp))
	require.Len(t, startingResp.GetTerminals(), 1)
	assert.Equal(t, terminalID, startingResp.GetTerminals()[0].GetTerminalId())
	assert.True(t, startingResp.GetTerminals()[0].GetQuake())
	assert.Equal(t, "Quake Renamed", startingResp.GetTerminals()[0].GetTitle())
	assert.Equal(t, leapmuxv1.TerminalStatus_TERMINAL_STATUS_STARTING, startingResp.GetTerminals()[0].GetStatus())

	releaseOnce()
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	// WatchEvents attaches cleanly to the in-memory quake terminal.
	wWatch := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerWatchServiceWatchEventsProcedure, &leapmuxv1.WatchEventsRequest{
		UpdateId:  1,
		Terminals: []*leapmuxv1.WatchTerminalEntry{{TerminalId: terminalID, Mode: leapmuxv1.WatchMode_WATCH_MODE_FULL}},
	}, wWatch)
	ack := waitWatchUpdateAck(t, wWatch)
	require.Empty(t, ack.GetRejectedTerminals(), "WatchEvents must attach cleanly to the in-memory quake terminal")
	assert.Equal(t, uint64(1), ack.GetUpdateId())
	require.Empty(t, wWatch.errors)

	// ListTerminals returns Quake: true with the renamed title after startup finishes.
	wList := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceListTerminalsProcedure, &leapmuxv1.ListTerminalsRequest{
		QuakeWorkingDirs: []string{dir},
	}, wList)
	require.Empty(t, wList.errors)
	require.Len(t, wList.responses, 1)
	var listResp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(wList.responses[0].GetPayload(), &listResp))
	require.Len(t, listResp.GetTerminals(), 1)
	assert.True(t, listResp.GetTerminals()[0].GetQuake())
	assert.Equal(t, "Quake Renamed", listResp.GetTerminals()[0].GetTitle())

	// RestartTerminal returns FailedPrecondition for quake terminals.
	wRestart := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, &leapmuxv1.RestartTerminalRequest{
		TerminalId: terminalID,
	}, wRestart)
	require.Len(t, wRestart.errors, 1)
	assert.Equal(t, codeFailedPrecondition, wRestart.errors[0].code)

	// Zero rows in terminals and worktree_tabs throughout the quake lifecycle.
	var termCount, wtTabCount int
	require.NoError(t, svc.DB.QueryRowContext(ctx, "SELECT count(*) FROM terminals").Scan(&termCount))
	require.NoError(t, svc.DB.QueryRowContext(ctx, "SELECT count(*) FROM worktree_tabs").Scan(&wtTabCount))
	assert.Zero(t, termCount, "quake terminals must never write to SQLite terminals")
	assert.Zero(t, wtTabCount, "quake terminals must never write to SQLite worktree_tabs")

	// A fresh Service over the same DB sees zero quake terminals.
	restarted := New(svc.Config)
	t.Cleanup(restarted.InputQueue.StopAndWait)
	assert.Empty(t, restarted.QuakeTerminals.ListOpen())

	// Closing via CloseTab removes the quake terminal cleanly and leaves zero SQLite rows.
	wClose := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTabServiceCloseTabProcedure, &leapmuxv1.CloseTabRequest{
		Tab: terminalTabIdent(terminalID),
	}, wClose)
	require.Empty(t, wClose.errors)
	assert.False(t, svc.QuakeTerminals.Has(terminalID))
	assert.False(t, svc.Terminals.HasTerminal(terminalID))
	require.NoError(t, svc.DB.QueryRowContext(ctx, "SELECT count(*) FROM terminals").Scan(&termCount))
	assert.Zero(t, termCount)
}

func TestRestartTerminal_ExitedQuakeTerminalReturnsFailedPrecondition(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "agent-1", dir)

	terminalID, _ := openQuakeTerminal(t, svc, d, dir)
	waitForTerminalSpawn(t, svc, terminalID, "spawn")

	// Simulate the shell exiting so closeQuakeTerminalOnShellExit removes the active entry.
	svc.closeQuakeTerminalOnShellExit(terminalID)
	require.Eventually(t, func() bool {
		return !svc.QuakeTerminals.Has(terminalID)
	}, 30*time.Second, 10*time.Millisecond)

	wRestart := newTestWriter()
	dispatch(d, leapmuxv1connect.WorkerTerminalServiceRestartTerminalProcedure, &leapmuxv1.RestartTerminalRequest{
		TerminalId: terminalID,
	}, wRestart)
	require.Len(t, wRestart.errors, 1)
	assert.Equal(t, codeFailedPrecondition, wRestart.errors[0].code,
		"restarting an exited quake terminal must return FailedPrecondition, not NotFound")
}
