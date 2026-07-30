package crdt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingChecker allows everything and records what it was asked, so a test can
// assert both the verdict and whether the inner checker was consulted at all.
type recordingChecker struct {
	workersAsked    []string
	workspacesAsked []string
}

func (r *recordingChecker) CanAccessWorkspace(_ context.Context, workspaceID, _ string) (bool, error) {
	r.workspacesAsked = append(r.workspacesAsked, workspaceID)
	return true, nil
}

func (r *recordingChecker) CanUseWorker(_ context.Context, workerID, _ string) (bool, error) {
	r.workersAsked = append(r.workersAsked, workerID)
	return true, nil
}

// A credential with no worker bound must not be wrapped at all: the session/API
// case pays for no decorator and no predicate.
func TestScopedAuthChecker_UnscopedReturnsInnerUntouched(t *testing.T) {
	inner := &recordingChecker{}
	assert.Same(t, AuthChecker(inner), scopedAuthChecker(inner, nil),
		"a credential with no scope must not be wrapped")
}

// The worker bound subtracts access BEFORE the inner check: an out-of-scope worker
// is denied without the inner checker ever being consulted, so no amount of
// "may this USER use this worker" can re-open the cross-tenant reach.
func TestScopedAuthChecker_WorkerScopeDeniesBeforeInnerCheck(t *testing.T) {
	ctx := context.Background()
	inner := &recordingChecker{}
	// The bound the auth package resolves for a bearer minted by "minter": only the
	// minter itself is in reach.
	checker := scopedAuthChecker(inner, func(workerID string) bool { return workerID == "minter" })

	ok, err := checker.CanUseWorker(ctx, "victim-worker", "victim")
	require.NoError(t, err)
	assert.False(t, ok, "a worker outside the delegation's minter bound must be denied")
	assert.Empty(t, inner.workersAsked,
		"an out-of-scope worker must be denied without consulting the inner checker")

	ok, err = checker.CanUseWorker(ctx, "minter", "victim")
	require.NoError(t, err)
	assert.True(t, ok, "the minting worker itself stays reachable")
	assert.Equal(t, []string{"minter"}, inner.workersAsked,
		"an in-scope worker must still face the inner may-this-user-use-it check")
}

// The scope can only ever SUBTRACT: a worker the bound allows but the inner checker
// denies must stay denied.
func TestScopedAuthChecker_WorkerScopeCannotGrantAccess(t *testing.T) {
	denyInner := workerScopeDenyAll{}
	checker := scopedAuthChecker(denyInner, func(string) bool { return true })

	ok, err := checker.CanUseWorker(context.Background(), "any", "p1")
	require.NoError(t, err)
	assert.False(t, ok, "an allowing scope must not override the inner checker's deny")
}

type workerScopeDenyAll struct{}

func (workerScopeDenyAll) CanAccessWorkspace(context.Context, string, string) (bool, error) {
	return false, nil
}
func (workerScopeDenyAll) CanUseWorker(context.Context, string, string) (bool, error) {
	return false, nil
}

// The worker bound gates WORKERS only. It must not gate workspaces as a side
// effect: a delegation bearer authenticates AS its user, so every workspace
// that user owns is legitimately readable, and a bearer that could only see one
// of them is the behaviour this change deliberately retired.
func TestScopedAuthChecker_WorkerScopeDoesNotGateWorkspaces(t *testing.T) {
	ctx := context.Background()
	inner := &recordingChecker{}
	checker := scopedAuthChecker(inner, func(workerID string) bool { return workerID == "minter" })

	for _, ws := range []string{"ws-1", "ws-2"} {
		ok, err := checker.CanAccessWorkspace(ctx, ws, "p1")
		require.NoError(t, err)
		assert.True(t, ok, "a worker bound must not gate workspace %s", ws)
	}
	assert.Equal(t, []string{"ws-1", "ws-2"}, inner.workspacesAsked,
		"every workspace question must reach the inner checker unchanged")
}

// batchRecordingChecker also implements the optional workspaceReaderBatch
// capability, recording whether the BATCH form (a single load) was consulted
// rather than N per-user CanAccessWorkspace calls.
type batchRecordingChecker struct {
	recordingChecker
	batchCalls int
}

func (b *batchRecordingChecker) CanAccessWorkspaceForUsers(_ context.Context, _ string, userIDs []string) (map[string]bool, error) {
	b.batchCalls++
	out := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		out[id] = true
	}
	return out, nil
}

// Both wrappers must forward the OPTIONAL workspaceReaderBatch capability so
// wrapping a batch-capable checker can never silently drop the batched
// fast path onto N per-user loads (WRAPPERS-2).
func TestScopedAuthChecker_ForwardsBatchCapability(t *testing.T) {
	ctx := context.Background()
	inner := &batchRecordingChecker{}
	// The worker scope leaves workspace access ungated, so the batch call
	// reaches inner unchanged.
	scoped := scopedAuthChecker(inner, func(string) bool { return true })
	batch, ok := scoped.(workspaceReaderBatch)
	require.True(t, ok, "the scoped wrapper must expose workspaceReaderBatch")

	res, err := batch.CanAccessWorkspaceForUsers(ctx, "ws-1", []string{"u1", "u2"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"u1": true, "u2": true}, res)
	assert.Equal(t, 1, inner.batchCalls, "must use the single batched load, not per-user calls")
	assert.Empty(t, inner.workspacesAsked, "the per-user fallback must not run when the batch form exists")
}

// When inner lacks the batch capability, the wrapper falls back to a per-user
// CanAccessWorkspace loop and still forwards -- so a caller keying on the
// capability never sees it vanish just because a wrapper was applied.
func TestScopedAuthChecker_BatchFallsBackToPerUser(t *testing.T) {
	ctx := context.Background()
	inner := &recordingChecker{} // no CanAccessWorkspaceForUsers
	scoped := scopedAuthChecker(inner, func(string) bool { return true }).(workspaceReaderBatch)

	res, err := scoped.CanAccessWorkspaceForUsers(ctx, "ws-1", []string{"u1", "u2"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"u1": true, "u2": true}, res)
	assert.Equal(t, []string{"ws-1", "ws-1"}, inner.workspacesAsked,
		"the fallback issues one per-user CanAccessWorkspace")
}

// The memo wrapper must forward the batch capability too.
func TestMemoAuthChecker_ForwardsBatchCapability(t *testing.T) {
	ctx := context.Background()
	inner := &batchRecordingChecker{}
	memo := &memoAuthChecker{inner: inner}
	batch, ok := any(memo).(workspaceReaderBatch)
	require.True(t, ok, "the memo wrapper must expose workspaceReaderBatch")

	res, err := batch.CanAccessWorkspaceForUsers(ctx, "ws-1", []string{"u1"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"u1": true}, res)
	assert.Equal(t, 1, inner.batchCalls)
}
