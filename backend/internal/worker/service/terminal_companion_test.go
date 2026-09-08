package service

import (
	"context"
	"database/sql"
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

// A COMPANION terminal is the shell behind one agent tab's quake panel. The
// worker owns the link, and these tests pin the three properties every device
// depends on: at most one live companion per agent, the same one handed to
// whoever asks second, and the whole thing going away with its owner.

// openCompanion runs OpenTerminal with an owner and returns the terminal id.
func openCompanion(t *testing.T, svc *Service, d *channel.Dispatcher, ownerAgentID, workingDir string) (string, string) {
	t.Helper()
	w := newTestWriter()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:        testutil.TestShell(),
		WorkingDir:   workingDir,
		OwnerAgentId: ownerAgentID,
		Cols:         200,
		Rows:         24,
	}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.OpenTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	require.NotEmpty(t, resp.GetTerminalId())
	testutil.RegisterTerminalCleanup(t, svc.Terminals, resp.GetTerminalId())
	return resp.GetTerminalId(), resp.GetTitle()
}

func TestOpenTerminal_RecordsTheOwningAgent(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)

	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.Equal(t, "owner-1", row.OwnerAgentID)
}

func TestOpenTerminal_WithoutAnOwnerIsUnchanged(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	terminalID := openTerminalViaRPC(t, svc, d, w, t.TempDir())

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.Empty(t, row.OwnerAgentID, "an ordinary terminal tab owns nothing")
}

// The second toggle, and the second DEVICE. Both must land on one PTY.
func TestOpenTerminal_SecondOpenForOneOwnerReturnsTheSameTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)

	first, firstTitle := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(first) }, "spawn")

	second, secondTitle := openCompanion(t, svc, d, "owner-1", dir)

	assert.Equal(t, first, second, "one agent has at most one live companion")
	assert.Equal(t, firstTitle, secondTitle, "the second caller reads the stored title")

	rows, err := svc.Queries.ListAllOpenTerminalIDsWithOwner(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the duplicate open must create no second row")
}

// Two devices toggling at the same instant. The lookup-then-insert has a
// window, and the unique partial index is what closes it: one insert wins, the
// loser re-reads and answers with the winner's id.
func TestOpenTerminal_ConcurrentOpensForOneOwnerYieldOneTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)

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
			id, _ := openCompanion(t, svc, d, "owner-1", dir)
			ids[i] = id
		}()
	}
	start.Done()
	wg.Wait()

	for _, id := range ids {
		assert.Equal(t, ids[0], id, "every caller must attach to one shell")
	}
	rows, err := svc.Queries.ListAllOpenTerminalIDsWithOwner(context.Background())
	require.NoError(t, err)
	assert.Len(t, rows, 1, "the unique index must leave exactly one live companion")
}

// The index is scoped to OPEN rows, so a shell the user exited does not block
// the next one.
func TestOpenTerminal_AfterACompanionClosesANewOneCanOpen(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)

	first, _ := openCompanion(t, svc, d, "owner-1", dir)
	_, err := svc.Queries.CloseTerminal(context.Background(), first)
	require.NoError(t, err)

	second, _ := openCompanion(t, svc, d, "owner-1", dir)

	assert.NotEqual(t, first, second, "the next open must start a fresh shell")
}

// A terminal TAB survives its shell -- the row stays open and Enter respawns it.
// A companion has no such contract, so its exit closes the row. Without this the
// unique index would refuse the next companion and the next toggle would adopt a
// dead PTY.
func TestCompanionTerminal_ShellExitClosesTheRow(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	// NOT through exitTerminalAndWait: that waits for the manager to report the
	// terminal EXITED, and closing a companion REMOVES it from the manager. The
	// row is the thing to wait on, and it is also the thing under test.
	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
		return err == nil && row.ClosedAt.Valid
	}, "the companion row closes on exit")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the companion leaves the manager too")
}

// The mirror, and the reason the branch is keyed on the owner: an ordinary
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

func TestListTerminals_ResolvesACompanionByItsOwner(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)

	w := newTestWriter()
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{OwnerAgentIds: []string{"owner-1"}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	require.Len(t, resp.GetTerminals(), 1)
	assert.Equal(t, terminalID, resp.GetTerminals()[0].GetTerminalId())
	assert.Equal(t, "owner-1", resp.GetTerminals()[0].GetOwnerAgentId())
	// An owner with no companion is an ABSENCE, not a failed hydration -- the
	// client asks precisely to find out whether one exists.
	assert.Empty(t, resp.GetVerdicts(), "an owner lookup answers no verdicts")
}

func TestListTerminals_AnOwnerWithNoCompanionAnswersEmpty(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t)
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, "ListTerminals", &leapmuxv1.ListTerminalsRequest{OwnerAgentIds: []string{"nobody"}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))

	assert.Empty(t, resp.GetTerminals())
	assert.Empty(t, resp.GetVerdicts())
}

// Closing the agent ends its companion, and it happens in the SHARED teardown
// so every close path carries it -- the online RPC, the reconciler's reap, and
// the deleted-workspace sweep.
func TestCloseAgent_ClosesItsCompanionTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "owner-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "the companion must close with its owner")
	assert.False(t, svc.Terminals.HasTerminal(terminalID), "the PTY must be reaped, not merely marked")
}

// The convergence path, which is what a reconciler reap and a deleted workspace
// both take. Putting the companion close in the RPC handler instead would have
// left these weaker than the online close.
func TestCloseAgentForConvergence_ClosesItsCompanionTerminal(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	svc.CloseTabForReconcile(leapmuxv1.TabType_TAB_TYPE_AGENT, "", "owner-1")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.True(t, row.ClosedAt.Valid, "an offline close must carry the companion too")
}

// A subagent transcript owns no process, so it owns no companion either. Its
// close is UI-only and must run no teardown.
func TestCloseChildAgent_LeavesTheRootCompanionAlone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	require.NoError(t, svc.Queries.CreateChildAgent(context.Background(), db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "owner-1", Valid: true},
		WorkingDir:    dir,
		HomeDir:       dir,
	}))

	w := newTestWriter()
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "child-1"}, w)
	require.Empty(t, w.errors)

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.False(t, row.ClosedAt.Valid, "a subagent close must not end the root's companion")
}

// The owner is checked at the ONE write point of owner_agent_id, because every
// later consumer reads that column and assumes it gives a live ROOT agent.
func TestOpenTerminal_RefusesACompanionForASubagent(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "root-1", dir)
	require.NoError(t, svc.Queries.CreateChildAgent(context.Background(), db.CreateChildAgentParams{
		ID:            "child-1",
		ParentAgentID: sql.NullString{String: "root-1", Valid: true},
		SpawnSpanID:   "spawn-1",
		WorkingDir:    dir,
		HomeDir:       dir,
	}))

	w := newTestWriter()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:        testutil.TestShell(),
		WorkingDir:   dir,
		OwnerAgentId: "child-1",
	}, w)

	require.Len(t, w.errors, 1, "a subagent owner must be refused")
	assert.Contains(t, w.errors[0].message, "subagent")
	rows, err := svc.Queries.ListAllOpenTerminalIDsWithOwner(context.Background())
	require.NoError(t, err)
	assert.Empty(t, rows, "the refusal must create no row and no shell")
}

func TestOpenTerminal_RefusesACompanionForAnUnknownAgent(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)

	w := newTestWriter()
	dispatch(d, "OpenTerminal", &leapmuxv1.OpenTerminalRequest{
		Shell:        testutil.TestShell(),
		WorkingDir:   t.TempDir(),
		OwnerAgentId: "no-such-agent",
	}, w)

	require.Len(t, w.errors, 1)
	assert.Contains(t, w.errors[0].message, "owner agent not found")
}

// A worker RESTART leaves a companion row open with no PTY behind it. Adopting
// that row hands the user a panel that paints nothing and swallows every
// keystroke, and a companion refuses the Enter that restarts a terminal TAB --
// so the panel would stay dead for the life of the agent tab, across reloads.
// The reconciler cannot reap it either: it measures a companion by its OWNER's
// tab key, and the owner is alive.
func TestOpenTerminal_DoesNotAdoptACompanionWhoseShellIsGone(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)

	// The shape a restart leaves behind: an open row this process never hosted.
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:           "stale-companion",
		WorkingDir:   dir,
		HomeDir:      dir,
		Shell:        testutil.TestShell(),
		Title:        "Terminal Ghost",
		Screen:       []byte{},
		OwnerAgentID: "owner-1",
	}))

	fresh, _ := openCompanion(t, svc, d, "owner-1", dir)

	assert.NotEqual(t, "stale-companion", fresh, "a row with no live shell must not be adopted")
	stale, err := svc.Queries.GetTerminal(context.Background(), "stale-companion")
	require.NoError(t, err)
	assert.True(t, stale.ClosedAt.Valid,
		"the corpse must be closed, or the unique index refuses every replacement")
}

// The boot sweep is what makes the property above hold WITHOUT waiting for a
// user to toggle the panel: a companion row is valid only while this process
// hosts its PTY.
func TestCloseOrphanedCompanionTerminals_ClosesARowWithNoLiveShell(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	createAgentForPath(t, svc, "owner-2", dir)

	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:           "orphan-companion",
		WorkingDir:   dir,
		HomeDir:      dir,
		Shell:        testutil.TestShell(),
		Screen:       []byte{},
		OwnerAgentID: "owner-1",
	}))
	live, _ := openCompanion(t, svc, d, "owner-2", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(live) }, "spawn")
	plainTab := openTerminalViaRPC(t, svc, d, newTestWriter(), dir)

	svc.CloseOrphanedCompanionTerminals(context.Background())

	orphan, err := svc.Queries.GetTerminal(context.Background(), "orphan-companion")
	require.NoError(t, err)
	assert.True(t, orphan.ClosedAt.Valid, "a companion this process does not host must close")

	liveRow, err := svc.Queries.GetTerminal(context.Background(), live)
	require.NoError(t, err)
	assert.False(t, liveRow.ClosedAt.Valid, "a companion whose shell is running must survive")

	tabRow, err := svc.Queries.GetTerminal(context.Background(), plainTab)
	require.NoError(t, err)
	assert.False(t, tabRow.ClosedAt.Valid, "an ordinary terminal tab is not a companion and is untouched")
}

// A companion has no restart contract, and the worker is where that rule has to
// live: a respawn would resurrect a row the exit path already closed, and the
// unique index would then refuse every replacement.
func TestRestartTerminal_RefusesACompanion(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	// A row with no live shell, which is the only state a restart could ever
	// reach: the companion's own exit closes the row a moment later, and this
	// is the window in between.
	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:           "exited-companion",
		WorkingDir:   dir,
		HomeDir:      dir,
		Shell:        testutil.TestShell(),
		Screen:       []byte{},
		OwnerAgentID: "owner-1",
	}))

	w := newTestWriter()
	dispatch(d, "RestartTerminal", &leapmuxv1.RestartTerminalRequest{TerminalId: "exited-companion"}, w)

	require.Len(t, w.errors, 1, "a companion must refuse a restart")
	assert.Contains(t, w.errors[0].message, "quake terminal")
	assert.False(t, svc.Terminals.HasTerminal("exited-companion"), "the refusal must spawn nothing")
}

// The "[Terminal process exited - Press Enter to restart]" notice reaches an
// open panel as ordinary terminal data, and both the browser and
// RestartTerminal then decline the Enter it invites. A companion therefore gets
// no notice, so the panel retracts on a clean screen rather than on a dead
// offer.
func TestCompanionTerminal_ShellExitPaintsNoRestartNotice(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	sendShellLine(t, d, terminalID, []byte("exit 0"+testutil.TestShellEnter()))

	testutil.AssertEventually(t, func() bool {
		row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
		return err == nil && row.ClosedAt.Valid
	}, "the companion row closes on exit")

	row, err := svc.Queries.GetTerminal(context.Background(), terminalID)
	require.NoError(t, err)
	assert.NotContains(t, string(row.Screen), "Press Enter to restart",
		"a companion refuses that Enter, so it must not offer it")
}

// A companion has no CRDT tab, so the Hub never lists one and it reaches
// ApplyTabArchiveState only through its owner. Without that expansion its
// workspace_archived stays 0 for ever: its shell keeps running and SendInput
// keeps reaching the PTY in an archived workspace.
func TestApplyTabArchiveState_ArchivesTheCompanionOfARequestedAgent(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	ctx := context.Background()
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	_, err := svc.ApplyTabArchiveState(ctx,
		leapmuxv1.WorkspaceArchiveState_WORKSPACE_ARCHIVE_STATE_ARCHIVED,
		[]*leapmuxv1.TabRef{{TabId: "owner-1", TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}},
	)
	require.NoError(t, err)

	row, err := svc.Queries.GetTerminal(ctx, terminalID)
	require.NoError(t, err)
	assert.EqualValues(t, 1, row.WorkspaceArchived,
		"the companion carries its owner's archive state, or no write to it is ever refused")
}

// owner_agent_id is AGENT data on a terminal reply, so it takes agent:read on
// top of the terminal:read this handler is registered with.
//
// privateEventVisible applies the identical rule to the same datum on the event
// side: a QuakePanelCommand needs BOTH kinds. Without this, an app granted
// `worker:read terminal:read` could read the agent that owns each companion,
// and probe by agent id through owner_agent_ids -- exactly what the event gate
// refuses it.
func TestListTerminals_HidesTheOwnerAgentFromACallerWithoutAgentRead(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	createAgentForPath(t, svc, "owner-1", dir)
	terminalID, _ := openCompanion(t, svc, d, "owner-1", dir)
	testutil.AssertEventually(t, func() bool { return svc.Terminals.HasTerminal(terminalID) }, "spawn")

	byTabID := func(scopes string) *leapmuxv1.ListTerminalsResponse {
		w := newTestWriter()
		dispatchScoped(d, mustScopes(scopes), "ListTerminals", &leapmuxv1.ListTerminalsRequest{TabIds: []string{terminalID}}, w)
		require.Empty(t, w.errors)
		require.Len(t, w.responses, 1)
		var resp leapmuxv1.ListTerminalsResponse
		require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
		return &resp
	}

	withAgentRead := byTabID("worker:read terminal:read agent:read")
	require.Len(t, withAgentRead.GetTerminals(), 1)
	assert.Equal(t, "owner-1", withAgentRead.GetTerminals()[0].GetOwnerAgentId(),
		"a caller that reads both kinds sees the owner")

	terminalOnly := byTabID("worker:read terminal:read")
	require.Len(t, terminalOnly.GetTerminals(), 1,
		"the terminal itself stays readable; only the agent id is withheld")
	assert.Empty(t, terminalOnly.GetTerminals()[0].GetOwnerAgentId(),
		"terminal:read alone must not reveal the agent that owns a companion")

	// The same rule on the request side: owner_agent_ids is an agent-id probe.
	w := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read"), "ListTerminals",
		&leapmuxv1.ListTerminalsRequest{OwnerAgentIds: []string{"owner-1"}}, w)
	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var probed leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &probed))
	assert.Empty(t, probed.GetTerminals(),
		"terminal:read alone must not answer whether an agent id has a companion")
}

// An EMPTY owner id must not act as a wildcard. Ordinary terminal rows store
// owner_agent_id = ” -- the partial unique index covers only non-empty owners
// -- so an unresolved id in the list would return every open terminal on the
// worker as the companion of an agent that does not exist.
func TestListTerminals_IgnoresAnEmptyOwnerAgentID(t *testing.T) {
	t.Parallel()

	svc, d, w := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	dir := t.TempDir()
	openTerminalViaRPC(t, svc, d, w, dir)

	reply := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:read agent:read"), "ListTerminals",
		&leapmuxv1.ListTerminalsRequest{OwnerAgentIds: []string{""}}, reply)

	require.Empty(t, reply.errors)
	require.Len(t, reply.responses, 1)
	var resp leapmuxv1.ListTerminalsResponse
	require.NoError(t, proto.Unmarshal(reply.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetTerminals(), "an empty owner id names no companion")
}

// SetQuakePanel names an agent tab, and its reply tells an unknown id from a
// known one. A caller that cannot read agents must not get that oracle.
func TestSetQuakePanel_RefusesACallerWithoutAgentRead(t *testing.T) {
	t.Parallel()

	svc, d, _ := setupTestService(t, withRemoteIPC(&fakeRemoteIPC{}))
	defer drainAllInFlight(svc)
	createAgentForPath(t, svc, "owner-1", t.TempDir())

	req := &leapmuxv1.SetQuakePanelRequest{
		AgentId: "owner-1",
		Action:  leapmuxv1.QuakePanelAction_QUAKE_PANEL_ACTION_TOGGLE,
	}

	denied := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:write"), "SetQuakePanel", req, denied)
	require.Len(t, denied.errors, 1, "terminal:write alone must not reach an agent id")

	allowed := newTestWriter()
	dispatchScoped(d, mustScopes("worker:read terminal:write agent:read"), "SetQuakePanel", req, allowed)
	assert.Empty(t, allowed.errors, "a caller that reads agents and writes terminals is served")
}
