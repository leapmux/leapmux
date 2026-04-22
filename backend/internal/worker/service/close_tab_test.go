package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// --- Fixture ---------------------------------------------------------
//
// closeTabFixture bundles the setup common to the "happy path" close
// tests: a service with one workspace, a real git repo + worktree on a
// fresh branch, a registered agent or terminal, and a tab→worktree
// association. The fixture registers drainAllInFlight via t.Cleanup *after*
// its own t.TempDir() calls, so LIFO cleanup ordering drains in-flight
// handlers before any fixture-owned disk is removed.

type closeTabFixture struct {
	svc   *Context
	d     *channel.Dispatcher
	w     *testResponseWriter
	wtDir string
	wtID  string
	tabID string
}

func setupCloseTabFixture(t *testing.T, tabType leapmuxv1.TabType, scenario string) closeTabFixture {
	t.Helper()
	svc, d, w := setupTestService(t, "ws-1")

	// The fixture only adds a worktree on a fresh branch named after
	// the scenario, so a shared base repo is safe and saves ~4 git
	// execs per call vs. initRepo.
	repoDir := sharedCloseTabRepo(t)
	wtDir := filepath.Join(t.TempDir(), scenario+"-wt")
	run(t, repoDir, "git", "worktree", "add", "-b", scenario, wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)

	tabID := scenario + "-tab"
	switch tabType {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		createAgentForPath(t, svc, tabID, wtDir)
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
			ID:          tabID,
			WorkspaceID: "ws-1",
			WorkingDir:  wtDir,
			Screen:      []byte{},
		}))
	default:
		t.Fatalf("unsupported tab type: %v", tabType)
	}
	svc.registerTabForWorktree(wtID, tabType, tabID)

	t.Cleanup(func() { drainAllInFlight(svc) })

	return closeTabFixture{svc: svc, d: d, w: w, wtDir: wtDir, wtID: wtID, tabID: tabID}
}

// --- CloseAgent -------------------------------------------------------

func TestCloseAgent_WithWorktreeActionRemove_RemovesWorktreeSync(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-remove")

	dispatch(fx.d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        fx.tabID,
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors, "unexpected RPC errors: %+v", fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseAgentResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetResult().GetFailureMessage(), "expected success, got failure: %s / %s", resp.GetResult().GetFailureMessage(), resp.GetResult().GetFailureDetail())

	// Worktree directory gone.
	_, statErr := os.Stat(fx.wtDir)
	assert.True(t, os.IsNotExist(statErr), "expected worktree dir removed, stat err: %v", statErr)

	// DB row soft-deleted.
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "expected DB row to be soft-deleted")
}

func TestCloseAgent_WithWorktreeActionKeep_PreservesWorktree(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-keep")

	dispatch(fx.d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        fx.tabID,
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)

	// Worktree dir survives.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved")

	// DB row still present and NOT soft-deleted.
	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.False(t, row.DeletedAt.Valid, "worktree DB row should not be soft-deleted under KEEP")

	// Tab association dropped.
	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(0), count)
}

func TestCloseAgent_WithWorktreeActionUnspecified_PreservesWorktree(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-default")

	// No worktree_action set = UNSPECIFIED — should behave like KEEP.
	dispatch(fx.d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: fx.tabID}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)

	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved by default")
}

func TestCloseAgent_NoWorktree_Succeeds(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	// Agent without any worktree association.
	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          "agent-noworktree",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        "agent-noworktree",
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetResult().GetFailureMessage())
}

func TestCloseAgent_WorktreeRemove_OtherTabsStillOpen_PreservesWorktree(t *testing.T) {
	// This test extends the fixture with a second agent on the same
	// worktree; the fixture normally registers only fx.tabID. That
	// sibling is what gates the worktree against removal even though
	// the close request asks for REMOVE.
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-shared")
	const siblingID = "close-shared-sibling"
	createAgentForPath(t, fx.svc, siblingID, fx.wtDir)
	fx.svc.registerTabForWorktree(fx.wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, siblingID)

	dispatch(fx.d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        fx.tabID,
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)

	// Other tab still references the worktree — it must survive.
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "worktree must survive while other tabs reference it")

	count, err := fx.svc.Queries.CountWorktreeTabs(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "exactly one tab should remain linked")
}

func TestCloseAgent_WorktreeRemoveFailure_ReturnsFailureMessage(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	// Create a worktree DB row pointing at a path that is NOT a git
	// worktree. This makes `git worktree remove` fail; since the path
	// still exists, removeWorktreeFromDisk must return an error.
	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "not-a-worktree")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))
	wtID := "wt-bogus"
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: bogusPath,
		RepoRoot:     repoDir,
		BranchName:   "",
	}))

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          "agent-fail",
		WorkspaceID: "ws-1",
		WorkingDir:  bogusPath,
		HomeDir:     bogusPath,
	}))
	svc.registerTabForWorktree(wtID, leapmuxv1.TabType_TAB_TYPE_AGENT, "agent-fail")

	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        "agent-fail",
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseAgentResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.NotEmpty(t, resp.GetResult().GetFailureMessage(), "expected failure message for unremovable worktree")
	assert.Contains(t, resp.GetResult().GetFailureDetail(), bogusPath, "detail should include worktree path")
	assert.Equal(t, bogusPath, resp.GetResult().GetWorktreePath())
	assert.Equal(t, wtID, resp.GetResult().GetWorktreeId())

	// Disk orphan remains.
	_, statErr := os.Stat(bogusPath)
	assert.NoError(t, statErr, "disk path should still exist when removal failed")

	// Per the plan, the DB row is still soft-deleted (a stale row is
	// less harmful than a live row pointing at an unremovable path).
	row, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row should be soft-deleted even on disk-remove failure")
}

func TestCloseAgent_WorktreeRemove_DiskAlreadyGone_StillDeletesDBRow(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_AGENT, "close-gone")

	// Simulate the user manually deleting the worktree directory before
	// closing the tab. git worktree-remove will fail, but os.Stat tells
	// us the path is already gone, so the handler treats it as success.
	require.NoError(t, os.RemoveAll(fx.wtDir))

	dispatch(fx.d, "CloseAgent", &leapmuxv1.CloseAgentRequest{
		AgentId:        fx.tabID,
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseAgentResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetResult().GetFailureMessage(), "path already gone should count as success")

	row, err := fx.svc.Queries.GetWorktreeByID(context.Background(), fx.wtID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row should be soft-deleted")
}

func TestCloseAgent_AlreadyClosed_Idempotent(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          "agent-idem",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// First close.
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-idem"}, w)
	require.Empty(t, w.errors)

	// Second close — requireAccessibleAgent should reject because the
	// agent is now closed. That's acceptable; the key property is the
	// handler does not wedge the Cleanup wait-group.
	dispatch(d, "CloseAgent", &leapmuxv1.CloseAgentRequest{AgentId: "agent-idem"}, w)
	// Accept either a benign error or a success response.
	// What we actually require: Cleanup wait-group is not stuck.
	done := make(chan struct{})
	go func() {
		svc.Cleanup.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Cleanup.Wait did not return after double close")
	}
}

// --- CloseTerminal mirrors --------------------------------------------

func TestCloseTerminal_WithWorktreeActionRemove_RemovesWorktreeSync(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-remove")

	dispatch(fx.d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
		TerminalId:     fx.tabID,
		WorkspaceId:    "ws-1",
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	require.Len(t, fx.w.responses, 1)
	var resp leapmuxv1.CloseTerminalResponse
	require.NoError(t, proto.Unmarshal(fx.w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetResult().GetFailureMessage())

	_, statErr := os.Stat(fx.wtDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestCloseTerminal_WithWorktreeActionUnspecified_PreservesWorktree(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-default")

	// No worktree_action = UNSPECIFIED, treated as KEEP.
	dispatch(fx.d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
		TerminalId:  fx.tabID,
		WorkspaceId: "ws-1",
	}, fx.w)

	require.Empty(t, fx.w.errors)
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr, "expected worktree dir preserved by default")
}

func TestCloseTerminal_NoWorktree_Succeeds(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	require.NoError(t, svc.Queries.UpsertTerminal(context.Background(), db.UpsertTerminalParams{
		ID:          "term-noworktree",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		Screen:      []byte{},
	}))

	dispatch(d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
		TerminalId:     "term-noworktree",
		WorkspaceId:    "ws-1",
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE,
	}, w)

	require.Empty(t, w.errors)
	require.Len(t, w.responses, 1)
	var resp leapmuxv1.CloseTerminalResponse
	require.NoError(t, proto.Unmarshal(w.responses[0].GetPayload(), &resp))
	assert.Empty(t, resp.GetResult().GetFailureMessage())
}

func TestCloseTerminal_WithWorktreeActionKeep_PreservesWorktree(t *testing.T) {
	fx := setupCloseTabFixture(t, leapmuxv1.TabType_TAB_TYPE_TERMINAL, "t-close-keep")

	dispatch(fx.d, "CloseTerminal", &leapmuxv1.CloseTerminalRequest{
		TerminalId:     fx.tabID,
		WorkspaceId:    "ws-1",
		WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP,
	}, fx.w)

	require.Empty(t, fx.w.errors)
	_, statErr := os.Stat(fx.wtDir)
	assert.NoError(t, statErr)
}

// --- removeWorktreeFromDisk direct coverage ---------------------------

func TestRemoveWorktreeFromDisk_Success_ReturnsNil(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "rwtfd-ok")
	run(t, repoDir, "git", "worktree", "add", "-b", "rwtfd-ok", wtDir)

	wtID, err := svc.ensureTrackedWorktree(context.Background(), wtDir)
	require.NoError(t, err)
	wt, err := svc.Queries.GetWorktreeByID(context.Background(), wtID)
	require.NoError(t, err)

	err = svc.removeWorktreeFromDisk(wt, true)
	assert.NoError(t, err)
	_, statErr := os.Stat(wtDir)
	assert.True(t, os.IsNotExist(statErr))
}

func TestRemoveWorktreeFromDisk_GitFailure_PathIntact_ReturnsError(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "rwtfd-fail")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))

	wt := db.Worktree{
		ID:           "wt-bogus",
		WorktreePath: bogusPath,
		RepoRoot:     repoDir,
	}
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wt.ID,
		WorktreePath: wt.WorktreePath,
		RepoRoot:     wt.RepoRoot,
	}))

	err := svc.removeWorktreeFromDisk(wt, true)
	assert.Error(t, err, "git-remove failure with path intact should return error")
	assert.Contains(t, err.Error(), bogusPath)

	// Disk path still present.
	_, statErr := os.Stat(bogusPath)
	assert.NoError(t, statErr)

	// DB row still soft-deleted.
	row, err := svc.Queries.GetWorktreeByID(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row soft-deleted even on failure")
}

func TestRemoveWorktreeFromDisk_PathMissing_ReturnsNil(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	wtDir := filepath.Join(t.TempDir(), "rwtfd-missing")
	// Never create the directory — it's missing from the start.

	wt := db.Worktree{
		ID:           "wt-missing",
		WorktreePath: wtDir,
		RepoRoot:     repoDir,
	}
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wt.ID,
		WorktreePath: wt.WorktreePath,
		RepoRoot:     wt.RepoRoot,
	}))

	err := svc.removeWorktreeFromDisk(wt, true)
	assert.NoError(t, err, "missing path should count as success")

	row, err := svc.Queries.GetWorktreeByID(context.Background(), wt.ID)
	require.NoError(t, err)
	assert.True(t, row.DeletedAt.Valid, "DB row soft-deleted")
}

func TestForceRemoveWorktree_GitFailure_PathIntact_ReturnsInternalError(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	defer drainAllInFlight(svc)

	repoDir := initRepo(t)
	bogusPath := filepath.Join(t.TempDir(), "force-fail")
	require.NoError(t, os.MkdirAll(bogusPath, 0o755))
	wtID := "wt-force-bogus"
	require.NoError(t, svc.Queries.CreateWorktree(context.Background(), db.CreateWorktreeParams{
		ID:           wtID,
		WorktreePath: bogusPath,
		RepoRoot:     repoDir,
	}))

	dispatch(d, "ForceRemoveWorktree", &leapmuxv1.ForceRemoveWorktreeRequest{WorktreeId: wtID}, w)

	require.Len(t, w.errors, 1, "expected an RPC error; responses=%+v", w.responses)
	assert.Contains(t, strings.ToLower(w.errors[0].message), "git worktree remove")
}

// --- Shutdown waiting -------------------------------------------------

func TestShutdown_WaitsForInFlightClose(t *testing.T) {
	svc, d, w := setupTestService(t, "ws-1")
	// Intentionally do NOT defer drainAllInFlight — this test invokes
	// Shutdown explicitly, which performs the same wait.

	require.NoError(t, svc.Queries.CreateAgent(context.Background(), db.CreateAgentParams{
		ID:          "agent-slow",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
	}))

	// Manually Add(1) to the cleanup wait-group to simulate an
	// in-flight close still doing work while Shutdown races it.
	svc.Cleanup.Add(1)

	shutdownDone := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(shutdownDone)
	}()

	// Shutdown must not complete while a handler is still in flight.
	select {
	case <-shutdownDone:
		t.Fatal("Shutdown returned while close was still in flight")
	case <-time.After(150 * time.Millisecond):
	}

	svc.Cleanup.Done()

	select {
	case <-shutdownDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown did not return after cleanup finished")
	}

	// Smoke: dispatcher still functional post-shutdown isn't required.
	_ = d
	_ = w
}

func TestShutdown_WaitsForCloseAfterHandlerPanic(t *testing.T) {
	svc, _, _ := setupTestService(t, "ws-1")

	// Simulate a handler that panics: Add(1) has been called, and the
	// deferred Done() MUST still fire.
	func() {
		defer func() { _ = recover() }()
		svc.Cleanup.Add(1)
		defer svc.Cleanup.Done()
		panic("simulated handler panic")
	}()

	done := make(chan struct{})
	go func() {
		svc.Shutdown()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Shutdown hung after a handler panic")
	}
}
