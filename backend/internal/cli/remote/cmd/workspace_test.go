package cmd

import (
	"context"
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
	caller := func(_ context.Context, workerID, workspaceID string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		mu.Lock()
		called = append(called, workerID)
		mu.Unlock()
		require.Equal(t, "ws-1", workspaceID)
		return &leapmuxv1.CleanupWorkspaceResponse{
			Worktrees: []*leapmuxv1.WorktreeInfo{
				{WorktreeId: "wt-" + workerID, WorktreePath: "/repo-on-" + workerID},
			},
		}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), "ws-1", []string{"w1", "w2", "w3"}, caller)
	assert.Equal(t, "ok", status)
	// Fan-out runs workers in parallel, so observed call order is
	// non-deterministic — assert set-equality, then check the result
	// slice preserves input order (the user-facing contract).
	assert.ElementsMatch(t, []string{"w1", "w2", "w3"}, called)
	require.Len(t, entries, 3)
	for i, wid := range []string{"w1", "w2", "w3"} {
		assert.Equal(t, wid, entries[i]["worker_id"])
		assert.Equal(t, "ok", entries[i]["status"])
		require.Contains(t, entries[i], "worktrees")
	}
}

// TestRunWorkspaceCleanupFanout_PartialFailure verifies the most
// important user-visible property: when one worker's cleanup fails,
// every other worker's result still lands in the entries slice and
// overall status downgrades to "partial". Without this, a
// deterministic-failure on worker A could silently swallow workers
// B and C.
func TestRunWorkspaceCleanupFanout_PartialFailure(t *testing.T) {
	caller := func(_ context.Context, workerID, _ string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		if workerID == "w2" {
			return nil, errors.New("network unreachable")
		}
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}

	status, entries := runWorkspaceCleanupFanout(context.Background(), "ws-1", []string{"w1", "w2", "w3"}, caller)
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
	caller := func(_ context.Context, _, _ string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return nil, errors.New("worker offline")
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), "ws-1", []string{"w1", "w2"}, caller)
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
	caller := func(_ context.Context, _, _ string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		called = true
		return nil, nil
	}
	status, entries := runWorkspaceCleanupFanout(context.Background(), "ws-1", nil, caller)
	assert.Equal(t, "ok", status)
	assert.Empty(t, entries)
	assert.False(t, called, "no workers means caller should not be invoked")
}

// TestRunWorkspaceCleanupFanout_OmitsEmptyWorktrees keeps the JSON
// payload tidy when a worker reports no worktrees: the "worktrees"
// key is dropped rather than emitting an empty list.
func TestRunWorkspaceCleanupFanout_OmitsEmptyWorktrees(t *testing.T) {
	caller := func(_ context.Context, _, _ string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, entries := runWorkspaceCleanupFanout(context.Background(), "ws-1", []string{"w1"}, caller)
	require.Len(t, entries, 1)
	_, has := entries[0]["worktrees"]
	assert.False(t, has, "no worktrees → no key in the JSON output")
}

// TestRunWorkspaceCleanupFanout_ContextPropagates verifies the
// per-worker call receives the same context the CLI was given
// (carries deadlines, cancellation). Important so a Ctrl-C between
// workers cancels the in-flight call.
func TestRunWorkspaceCleanupFanout_ContextPropagates(t *testing.T) {
	type ctxKey string
	const key ctxKey = "k"
	parent := context.WithValue(context.Background(), key, "v")

	caller := func(ctx context.Context, _, _ string) (*leapmuxv1.CleanupWorkspaceResponse, error) {
		assert.Equal(t, "v", ctx.Value(key), "fan-out must propagate the caller's context")
		return &leapmuxv1.CleanupWorkspaceResponse{}, nil
	}
	_, _ = runWorkspaceCleanupFanout(parent, "ws-1", []string{"w1"}, caller)
}
