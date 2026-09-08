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
