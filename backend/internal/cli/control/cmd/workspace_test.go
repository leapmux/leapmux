package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// TestRunWorkspaceCleanupFanout_AllSuccess verifies the happy-path
// orchestration: every worker_id from DeleteWorkspace gets a
// CleanupWorkspace, every entry reports "ok", and any worktrees the
// worker returned are surfaced for the user to decide what to do
// with.
func TestRunWorkspaceCleanupFanout_AllSuccess(t *testing.T) {
	var (
		mu     sync.Mutex
		called []string
	)
	caller := func(_ context.Context, workerID string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		mu.Lock()
		called = append(called, workerID)
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2", "w3"), caller)
	assert.Equal(t, "ok", status)
	// Fan-out runs workers in parallel, so observed call order is
	// non-deterministic — assert set-equality, then check the result
	// slice preserves input order (the user-facing contract).
	assert.ElementsMatch(t, []string{"w1", "w2", "w3"}, called)
	require.Len(t, entries, 3)
	for i, wid := range []string{"w1", "w2", "w3"} {
		assert.Equal(t, wid, entries[i]["worker_id"])
		assert.Equal(t, "ok", entries[i]["status"])
	}
}

// TestRunWorkspaceCleanupFanout_PartialFailure verifies the most
// important user-visible property: when one worker's cleanup fails,
// every other worker's result still lands in the entries slice and
// overall status downgrades to "partial". Without this, a
// deterministic-failure on worker A could silently swallow workers
// B and C.
func TestRunWorkspaceCleanupFanout_PartialFailure(t *testing.T) {
	caller := func(_ context.Context, workerID string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		if workerID == "w2" {
			return nil, errors.New("network unreachable")
		}
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2", "w3"), caller)
	assert.Equal(t, "partial", status, "any failure must downgrade overall status to 'partial'")
	require.Len(t, entries, 3)

	assert.Equal(t, "ok", entries[0]["status"])
	assert.Equal(t, "failed", entries[1]["status"])
	assert.Contains(t, entries[1]["error"], "network unreachable")
	assert.Equal(t, "ok", entries[2]["status"], "siblings of a failed worker must still surface a result")
}

// TestRunWorkspaceCleanupFanout_AllFailure pins the all-failed case:
// the per-worker entries still get assembled (the user wants to see
// what was attempted) and overall status is "partial".
func TestRunWorkspaceCleanupFanout_AllFailure(t *testing.T) {
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return nil, errors.New("worker offline")
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1", "w2"), caller)
	assert.Equal(t, "partial", status)
	require.Len(t, entries, 2)
	for _, e := range entries {
		assert.Equal(t, "failed", e["status"])
	}
}

// TestRunWorkspaceCleanupFanout_NoWorkers covers the empty-tabs case
// (workspace had no tabs at delete time): the hub-side delete is the
// only step, no fan-out is needed, and overall status is "ok".
func TestRunWorkspaceCleanupFanout_NoWorkers(t *testing.T) {
	called := false
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		called = true
		return nil, nil
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), nil, caller)
	assert.Equal(t, "ok", status)
	assert.Empty(t, entries)
	assert.False(t, called, "no workers means caller should not be invoked")
}

// TestRunWorkspaceCleanupFanout_OmitsEmptyWorktrees keeps the JSON
// payload tidy when a worker reports no worktrees: the "worktrees"
// key is dropped rather than emitting an empty list.
func TestRunWorkspaceCleanupFanout_OmitsEmptyWorktrees(t *testing.T) {
	caller := func(_ context.Context, _ string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, entries := runWorkspaceCleanupFanout(context.Background(), workerTabsFor("w1"), caller)
	require.Len(t, entries, 1)
	_, has := entries[0]["worktrees"]
	assert.False(t, has, "no worktrees → no key in the JSON output")
}

// TestRunWorkspaceCleanupFanout_RoutesEachWorkerItsOwnTabs pins the routing the
// tab-list shape introduced: CleanupWorkspace no longer takes a workspace id, so
// the fan-out has to hand every worker exactly the tabs IT hosts.
//
// Getting this wrong is not a cosmetic mis-send. A worker acts on the ids it is
// given -- closeTabCommon closes them and drops their worktree links -- and tab
// ids are unique per user, not per worker, so a tab id leaked into the wrong
// worker's list is a live tab on that machine that the request would close.
func TestRunWorkspaceCleanupFanout_RoutesEachWorkerItsOwnTabs(t *testing.T) {
	var (
		mu   sync.Mutex
		seen = map[string][]string{}
	)
	caller := func(_ context.Context, workerID string, tabs []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		ids := make([]string, 0, len(tabs))
		for _, t := range tabs {
			ids = append(ids, t.GetTabId())
		}
		mu.Lock()
		seen[workerID] = ids
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	workerTabs := []*leapmuxv1.WorkerTabs{
		{WorkerId: "w1", Tabs: []*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabId: "a-1"},
			{TabType: leapmuxv1.TabType_TAB_TYPE_TERMINAL, TabId: "t-1"},
		}},
		{WorkerId: "w2", Tabs: []*leapmuxv1.TabRef{
			{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: "f-2"},
		}},
	}

	status, _ := runWorkspaceCleanupFanout(context.Background(), workerTabs, caller)
	assert.Equal(t, "ok", status)
	assert.Equal(t, []string{"a-1", "t-1"}, seen["w1"])
	assert.Equal(t, []string{"f-2"}, seen["w2"], "w2 must not be handed w1's tab ids")
}

// TestRunWorkspaceCleanupFanout_EmptyTabGroupIsStillCalled pins the
// best-effort half of the contract.
//
// The hub groups the tabs it read inside the delete transaction, so a worker it
// names normally arrives WITH tabs -- but the wire shape permits an empty group,
// and a worker in the list must still be called rather than skipped. Calling it
// with nothing to close is a harmless no-op the reconciler covers; skipping it
// would drop that worker from the per-worker status the user is shown.
func TestRunWorkspaceCleanupFanout_EmptyTabGroupIsStillCalled(t *testing.T) {
	var (
		mu     sync.Mutex
		called = map[string]int{}
	)
	caller := func(_ context.Context, workerID string, tabs []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		assert.Emptyf(t, tabs, "%s arrived with an empty group, so it must be called with no tabs", workerID)
		mu.Lock()
		called[workerID]++
		mu.Unlock()
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(),
		[]*leapmuxv1.WorkerTabs{{WorkerId: "w-empty"}}, caller)

	assert.Equal(t, "ok", status)
	assert.Equal(t, 1, called["w-empty"], "a worker with an empty tab group must still be called")
	require.Len(t, entries, 1)
	assert.Equal(t, "w-empty", entries[0]["worker_id"])
}

// TestRunWorkspaceCleanupFanout_ContextPropagates verifies the
// per-worker call receives the same context the CLI was given
// (carries deadlines, cancellation). Important so a Ctrl-C between
// workers cancels the in-flight call.
func TestRunWorkspaceCleanupFanout_ContextPropagates(t *testing.T) {
	type ctxKey string
	const key ctxKey = "k"
	parent := context.WithValue(context.Background(), key, "v")

	caller := func(ctx context.Context, _ string, _ []*leapmuxv1.TabRef) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		assert.Equal(t, "v", ctx.Value(key), "fan-out must propagate the caller's context")
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, _ = runWorkspaceCleanupFanout(parent, workerTabsFor("w1"), caller)
}

// TestRunWorkspaceList_InsideWorkerSpawn is the end-to-end regression
// test for `leapmux control workspace list` run from inside a
// worker-spawned agent. The spawn injects LEAPMUX_CONTROL_USER_ID into
// every agent process (see controlipc/server.go); the command must
// ignore it and issue exactly one hub call. It used to bind the value
// to a --user-id flag whose resolver leg called the (now-deleted)
// hub GetUser RPC -- which the agent's delegation bearer may not
// invoke -- aborting an otherwise-legal ListWorkspaces with
// `resolve_failed`.
func TestRunWorkspaceList_InsideWorkerSpawn(t *testing.T) {
	// No worker.* call, so the spawn needs no local dispatcher.
	hub := &recordingHub{}
	startSpawnIPC(t, hub, nil)
	// The worker injects this into every spawned agent (see
	// controlipc.EnvVars).
	t.Setenv("LEAPMUX_CONTROL_USER_ID", "u-spawn")

	out := withCapturedStdout(t, func() {
		require.NoError(t, RunWorkspaceList(fakeCmdCtx{}, nil))
	})

	var env struct {
		Data  []map[string]any `json:"data"`
		Error map[string]any   `json:"error"`
	}
	require.NoError(t, json.Unmarshal(out, &env))
	require.Nil(t, env.Error, "workspace list must succeed inside a worker spawn")
	require.Len(t, env.Data, 1)
	assert.Equal(t, "ws-1", env.Data[0]["id"])
	assert.Equal(t, []string{"ListWorkspaces"}, hub.called(),
		"a session-scoped list must cost exactly one hub call; no entity-id env var may add another")
}

// workerTabsFor builds the per-worker fan-out shape for tests that only care
// about which workers are called, not which tabs each gets.
func workerTabsFor(workerIDs ...string) []*leapmuxv1.WorkerTabs {
	out := make([]*leapmuxv1.WorkerTabs, 0, len(workerIDs))
	for _, wid := range workerIDs {
		out = append(out, &leapmuxv1.WorkerTabs{WorkerId: wid})
	}
	return out
}
