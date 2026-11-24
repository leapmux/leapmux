package service

import (
	"github.com/leapmux/leapmux/internal/worker/bgtask"

	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRootSinkRefusalPreservesThePhysicalSource(t *testing.T) {
	t.Parallel()
	svc, original := setupRootSink(t, "root-generation")
	childID, err := original.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "child"})
	require.NoError(t, err)
	child := original.ChildSink(childID)
	root := requireRootOutputSink(t, svc.Output, "root-generation")
	childOwner := child.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"Child source"}`)}, agent.SpanInfo{}).Publication.Owner().(*transcriptOwner)
	before, err := svc.Queries.GetAgentByID(t.Context(), "root-generation")
	require.NoError(t, err)
	svc.Output.queries = db.New(&failedOutputSourceStore{DBTX: svc.DB, agentID: "root-generation", errMsg: "output source refused"})
	services, err := svc.Output.NewRootSink("root-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.ErrorContains(t, err, "output source refused")
	assert.Nil(t, services)
	assert.Same(t, root, svc.Output.sinkForAgent("root-generation"))
	assert.Same(t, childOwner.sink, svc.Output.sinkForAgent(childID))
	assert.True(t, root.registeredCurrent())
	root.progress.mu.Lock()
	closed := root.progress.closed
	root.progress.mu.Unlock()
	assert.False(t, closed)
	after, err := svc.Queries.GetAgentByID(t.Context(), "root-generation")
	require.NoError(t, err)
	assert.Equal(t, before, after)
}

func TestNewRootSinkRejectsVirtualChildrenAndChildrenInheritTheirParent(t *testing.T) {
	t.Parallel()
	svc, original := setupRootSink(t, "root-generation")
	root := requireRootOutputSink(t, svc.Output, "root-generation")
	childID, err := original.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "spawn", ProviderChildKey: "child"})
	require.NoError(t, err)
	child := original.ChildSink(childID)
	childOwner := child.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"Child source"}`)}, agent.SpanInfo{}).Publication.Owner().(*transcriptOwner)
	assert.Equal(t, root.outputGeneration, childOwner.sink.outputGeneration)
	grandchildID, err := child.EnsureChildAgent(agent.ChildAgentSpec{
		RegistryRole: bgtask.RoleChildTask,
		SpawnSpanID:  "nested-spawn", ProviderChildKey: "nested-child"})
	require.NoError(t, err)
	grandchild := child.ChildSink(grandchildID)
	grandchildOwner := grandchild.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"Nested source"}`)}, agent.SpanInfo{}).Publication.Owner().(*transcriptOwner)
	assert.Equal(t, root.outputGeneration, grandchildOwner.sink.outputGeneration)
	services, err := svc.Output.NewRootSink(childID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	assert.ErrorIs(t, err, sql.ErrNoRows)
	assert.Nil(t, services)
	assert.Same(t, root, svc.Output.sinkForAgent("root-generation"))
	assert.Same(t, childOwner.sink, svc.Output.sinkForAgent(childID))
	for _, id := range []string{childID, grandchildID} {
		_, err := svc.Queries.GetAgentByID(t.Context(), id)
		require.NoError(t, err)
		assert.Zero(t, svc.Output.currentOutputGeneration(id), "virtual child rows allocate no physical writer")
	}
}

type outputAllocationProbe struct {
	db.DBTX
	beforeAllocation func()
	allocationCalls  atomic.Int64
	setupReads       atomic.Int64
}

func (store *outputAllocationProbe) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if strings.HasPrefix(query, "-- name: GetAgentByID :one") {
		store.setupReads.Add(1)
	}
	if strings.HasPrefix(query, "-- name: GetRootAgentSessionID :one") {
		store.allocationCalls.Add(1)
		if store.beforeAllocation != nil {
			store.beforeAllocation()
		}
	}
	return store.DBTX.QueryRowContext(ctx, query, args...)
}

func TestNewRootSinkUsesTheSessionReturnedWithItsGeneration(t *testing.T) {
	t.Parallel()
	svc, _ := setupRootSink(t, "coherent-output-source")
	probe := &outputAllocationProbe{DBTX: svc.DB, beforeAllocation: func() {
		_, err := svc.DB.ExecContext(t.Context(), "UPDATE agents SET agent_session_id = ? WHERE id = ?", "allocated-native-session", "coherent-output-source")
		require.NoError(t, err)
	}}
	svc.Output.queries = db.New(probe)
	newTestRootOutputSink(t, svc.Output, "coherent-output-source", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	root := requireRootOutputSink(t, svc.Output, "coherent-output-source")
	assert.Equal(t, "allocated-native-session", root.currentMessageSessionID())
	assert.Equal(t, int64(1), probe.allocationCalls.Load())
	assert.Zero(t, probe.setupReads.Load(), "the checked root session lookup replaces an unchecked restoration read")
	assert.Equal(t, root.outputGeneration, svc.Output.currentOutputGeneration("coherent-output-source"))
}

func TestConcurrentNewRootSinkPublicationFollowsAllocationOrder(t *testing.T) {
	t.Parallel()
	svc, _ := setupRootSink(t, "ordered-output-source")
	ctx := testutil.DeadlineContext(t)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseOnce := sync.OnceFunc(func() { close(release) })
	var first sync.Once
	probe := &outputAllocationProbe{DBTX: svc.DB, beforeAllocation: func() {
		first.Do(func() { close(entered); <-release })
	}}
	svc.Output.queries = db.New(probe)
	firstDone, secondDone := make(chan error, 1), make(chan error, 1)
	t.Cleanup(releaseOnce)
	go func() {
		_, err := svc.Output.NewRootSink("ordered-output-source", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		firstDone <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("the first root did not enter its allocation")
	}
	go func() {
		_, err := svc.Output.NewRootSink("ordered-output-source", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		secondDone <- err
	}()
	mutation := svc.Output.transcriptMutationMutex("ordered-output-source")
	testutil.RequireEventually(t, func() bool { return mutation.writerAdmissions.Load() == 2 })
	assert.Equal(t, int64(1), probe.allocationCalls.Load(), "the waiting constructor must not allocate before its source lease")
	releaseOnce()
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-ctx.Done():
			t.Fatal("the admitted root constructor did not finish")
		}
	}
	current := requireRootOutputSink(t, svc.Output, "ordered-output-source")
	assert.Equal(t, int64(3), current.outputGeneration)
	assert.Equal(t, current.outputGeneration, svc.Output.currentOutputGeneration("ordered-output-source"))
	assert.Zero(t, mutation.writerAdmissions.Load())
}

func TestChildSessionRestorationWarningsReleaseConstructionLocks(t *testing.T) {
	for _, operation := range []string{"child services", "opening prompt"} {
		t.Run(operation, func(t *testing.T) {
			svc, original := setupRootSink(t, "child-restoration-source")
			childID, err := original.EnsureChildAgent(agent.ChildAgentSpec{
				RegistryRole: bgtask.RoleChildTask,
				SpawnSpanID:  "spawn", ProviderChildKey: "child", AgentSessionID: "stored-child-session"})
			require.NoError(t, err)
			root := requireRootOutputSink(t, svc.Output, "child-restoration-source")
			store := &failedOutputSourceStore{DBTX: svc.DB, agentID: childID}
			svc.Output.queries = db.New(store)
			mutation := svc.Output.transcriptMutationMutex("child-restoration-source")
			var warnings int
			var locksFree bool
			var restoredChild *agentOutputSink
			logger := slog.Default()
			slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
				if record.Message != "Read the provider session for the transcript" {
					return
				}
				warnings++
				if !mutation.TryLock() {
					return
				}
				mutation.Unlock()
				if !root.childMu.TryLock() {
					return
				}
				restoredChild = root.childSinks[childID]
				root.childMu.Unlock()
				locksFree = true
				newTestRootOutputSink(t, svc.Output, "child-restoration-source", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			}}))
			t.Cleanup(func() { slog.SetDefault(logger) })
			var childServices agent.ProviderServices
			if operation == "opening prompt" {
				require.NoError(t, original.PersistChildPrompt(childID, "The original assignment."))
			} else {
				childServices = original.ChildSink(childID)
			}
			assert.True(t, store.failed.Load(), "the warning must report the real child restoration read refusal")
			assert.Equal(t, 1, warnings)
			assert.True(t, locksFree, "the actual warning handler must be able to replace its root synchronously")
			require.NotNil(t, restoredChild, "retain the constructed child before its warning observer retires the old tree")
			assert.Empty(t, restoredChild.currentMessageSessionID(), "a failed restoration preserves the existing empty-session fallback")
			assert.Equal(t, root.outputGeneration, restoredChild.outputGeneration)
			assert.Same(t, root, restoredChild.root)
			assert.False(t, restoredChild.registeredCurrent())
			replacement := requireRootOutputSink(t, svc.Output, root.agentID)
			assert.NotSame(t, root, replacement)
			assert.True(t, replacement.registeredCurrent())
			assert.Greater(t, replacement.outputGeneration, root.outputGeneration)
			assert.Nil(t, svc.Output.sinkForAgent(childID), "the warning's replacement must retire the original child registration")
			root.childMu.Lock()
			assert.Empty(t, root.childSinks)
			root.childMu.Unlock()
			if childServices != nil {
				content := childServices.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"Historical child source"}`)}, agent.SpanInfo{})
				owner := content.Publication.Owner().(*transcriptOwner)
				assert.Same(t, restoredChild, owner.sink, "ChildSink must return its original constructed source through observer reentry")
				assert.Empty(t, content.AgentSessionID)
			} else {
				rows, err := svc.Queries.ListAllMessagesByAgentID(t.Context(), db.ListAllMessagesByAgentIDParams{AgentID: childID})
				require.NoError(t, err)
				require.Len(t, rows, 1)
				assert.Empty(t, rows[0].AgentSessionID, "the opening prompt keeps its refused-restoration source")
				assert.True(t, rows[0].TranscriptOnly)
			}
			assert.Zero(t, mutation.writerAdmissions.Load())
		})
	}
}

func TestTranscriptMutationLockBalancesDiagnosticAdmission(t *testing.T) {
	t.Parallel()
	var mutation transcriptMutationLock
	mutation.RLock()
	assert.False(t, mutation.TryLock())
	assert.Zero(t, mutation.writerAdmissions.Load())
	assert.True(t, mutation.TryRLock())
	mutation.RUnlock()
	mutation.RUnlock()
	mutation.Lock()
	assert.Equal(t, int64(1), mutation.writerAdmissions.Load())
	assert.False(t, mutation.TryLock())
	assert.Equal(t, int64(1), mutation.writerAdmissions.Load())
	mutation.Unlock()
	assert.Zero(t, mutation.writerAdmissions.Load())
	require.True(t, mutation.TryLock())
	assert.Equal(t, int64(1), mutation.writerAdmissions.Load())
	mutation.Unlock()
	assert.Zero(t, mutation.writerAdmissions.Load())
}

func TestRootSourceAllocationFailureNeverReachesAnyNativeLaunch(t *testing.T) {
	for _, operation := range []string{"open", "startup settings restart", "settings restart", "clear", "resume", "plan restart"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, _ := setupRootSink(t, "allocation-launch-source", withClock(clock))
			row, err := svc.Queries.GetAgentByID(t.Context(), "allocation-launch-source")
			require.NoError(t, err)
			beforeGen := svc.Output.currentOutputGeneration(row.ID)
			var launches atomic.Int64
			svc.startAgentFn = func(context.Context, agent.Options, agent.ProviderServices) (map[string]string, error) {
				launches.Add(1)
				return map[string]string{}, nil
			}
			svc.Output.queries = db.New(&failedOutputSourceStore{DBTX: svc.DB, agentID: "allocation-launch-source", errMsg: "launch source refused"})
			switch operation {
			case "open":
				handle := svc.AgentStartup.begin(row.ID, func() {})
				require.NotNil(t, handle)
				svc.runAgentStartup(t.Context(), row, gitModePlan{}, svc.baseAgentOptions(row.ID, row.WorkingDir, row.AgentProvider), handle)
				current, err := svc.Queries.GetAgentByID(t.Context(), row.ID)
				require.NoError(t, err)
				assert.Contains(t, current.StartupError, "launch source refused")
			case "startup settings restart":
				_, running := svc.relaunchForStartupSettingsChange(row.ID, row.AgentProvider, svc.baseAgentOptions(row.ID, row.WorkingDir, row.AgentProvider), row)
				assert.True(t, running, "prelaunch refusal preserves the fallback process decision")
			case "settings restart":
				_, err = svc.restartAgentPreservingSession(row, storedRestartOptions, settingsRestartMessages, restartTurnEndPending, nil)
				require.ErrorContains(t, err, "launch source refused")
			case "clear":
				_, err = svc.prepareClearContext(row.ID)
				require.ErrorContains(t, err, "launch source refused")
			case "resume":
				err = svc.ensureAgentRunning(row.ID, resumeIfConversation, interactiveStart)
				require.ErrorContains(t, err, "launch source refused")
			case "plan restart":
				_, err = svc.restartPlanContextLocked(row.ID, "acceptEdits", row)
				require.ErrorContains(t, err, "launch source refused")
			}
			assert.Zero(t, launches.Load(), "allocation failure is before the native starter in every production path")
			assert.Equal(t, beforeGen, svc.Output.currentOutputGeneration(row.ID))
		})
	}
}

func TestNewRootSinkConcurrentAllocationsAreDistinct(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	roots := []string{"concurrent-root-0", "concurrent-root-1", "concurrent-root-2", "concurrent-root-3"}
	for _, rootID := range roots {
		err := svc.Queries.CreateAgent(t.Context(), db.CreateAgentParams{
			ID:            rootID,
			WorkingDir:    t.TempDir(),
			AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE,
		})
		require.NoError(t, err)
	}

	const total = 16
	gens := make([]int64, total)
	var wg sync.WaitGroup
	wg.Add(total)
	for i := range total {
		go func(idx int) {
			defer wg.Done()
			rootID := roots[idx%len(roots)]
			services, err := svc.Output.NewRootSink(rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			require.NoError(t, err)
			owner := services.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"probe"}`)}, agent.SpanInfo{}).Publication.Owner().(*transcriptOwner)
			gens[idx] = owner.sink.outputGeneration
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]struct{}, total)
	for _, gen := range gens {
		assert.Positive(t, gen)
		_, dup := seen[gen]
		assert.False(t, dup, "generation %d must be unique across concurrent NewRootSink calls", gen)
		seen[gen] = struct{}{}
	}
	assert.Len(t, seen, total)
}

func TestNewRootSinkAfterCleanupAgentKeepsGenerationMonotonicAndPreservesRowFencing(t *testing.T) {
	t.Parallel()
	const rootID = "cleanup-preserves-fencing"
	svc, sink1 := setupRootSink(t, rootID)
	require.NoError(t, sink1.UpsertBackgroundTask(bgtask.Upsert{
		RowKey:     "t1",
		Kind:       bgtask.KindSubagent,
		Title:      "Task 1",
		Status:     bgtask.StatusRunning,
		ActiveForm: "step 1",
	}))
	sink2 := newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	gen2 := requireRootOutputSink(t, svc.Output, rootID).outputGeneration
	require.NoError(t, sink2.UpdateBackgroundTaskStatus("t1", bgtask.StatusRunning, "step 2"))
	beforeCleanup := storedRow(t, svc, rootID, "t1")

	// Simulate transient cleanup on an open agent (/clear, plan restart, or archive).
	svc.Output.CleanupAgent(rootID)

	// Late write from sink1 must still be fenced by preserved meta.rowGen.
	require.NoError(t, sink1.UpdateBackgroundTaskStatus("t1", bgtask.StatusFailed, "stale failure"))
	assert.Equal(t, beforeCleanup, storedRow(t, svc, rootID, "t1"))

	// Allocating a third sink must stay strictly monotonic above gen2.
	newTestRootOutputSink(t, svc.Output, rootID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	gen3 := requireRootOutputSink(t, svc.Output, rootID).outputGeneration
	assert.Greater(t, gen3, gen2)

	// Permanent closure removes the agent from all OutputHandler tracking maps.
	svc.Output.CleanupAgent(rootID)
	svc.Output.ForgetAgentGenerationState(rootID)
	assert.NotContains(t, svc.Output.TrackedAgentIDs(), rootID)
}
