package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// retainBackgroundTaskMutation reserves an actual sink operation behind refused storage.
// Its release changes storage availability without submitting another native event.
func retainBackgroundTaskMutation(t *testing.T, sink agent.ProviderServices, mutation func() error) (*agent.OutputQueue, func()) {
	t.Helper()
	queue := agent.OutputQueueFor(sink)
	require.NotNil(t, queue)
	var unavailable atomic.Bool
	unavailable.Store(true)
	queue.Append(
		agent.OutputWrite{Label: "refused registry storage", Run: func() error {
			if unavailable.Load() {
				return assert.AnError
			}
			return nil
		}},
		agent.OutputWrite{Label: "retained native registry mutation", Run: mutation},
	)
	require.ErrorIs(t, queue.Flush(), assert.AnError)
	release := func() { unavailable.Store(false) }
	t.Cleanup(release)
	return queue, release
}

func TestBackgroundTaskExitFollowsRetainedUpsert(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		for _, futureCreation := range []bool{false, true} {
			t.Run(fmt.Sprintf("stopped=%t/future-creation=%t", stopped, futureCreation), func(t *testing.T) {
				t.Parallel()
				clock := testutil.NewQuartzMock(t)
				svc, original := setupRootSink(t, "retained-registry-exit", withClock(clock))
				future := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
				queue, release := retainBackgroundTaskMutation(t, original, func() error {
					if err := original.UpsertBackgroundTask(bgtask.Upsert{
						RowKey: "outgoing-unit", Kind: bgtask.KindWorkflow, Title: "The outgoing native assignment",
						Status: bgtask.StatusRunning, ActiveForm: "The assignment was accepted before exit",
					}); err != nil {
						return err
					}
					if futureCreation {
						// Model a worker clock correction without sleeping or changing native data.
						_, err := svc.DB.ExecContext(t.Context(), "UPDATE agent_background_tasks SET created_at = ? WHERE owner_agent_id = ? AND row_key = ?",
							sqltime.NewSQLiteTime(future), "retained-registry-exit", "outgoing-unit")
						return err
					}
					return nil
				})

				svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "retained-registry-exit", Err: nil, Stopped: stopped, Output: original})
				require.True(t, queue.HasPending(), "the outgoing registry write still belongs to its original queue")
				release()
				require.NoError(t, queue.Flush())
				require.False(t, queue.HasPending())
				row := storedRow(t, svc, "retained-registry-exit", "outgoing-unit")
				expected := bgtask.StatusInterrupted
				if stopped {
					expected = bgtask.StatusStopped
				}
				assert.Equal(t, leapmuxv1.BackgroundTaskStatus(expected), row.Status,
					"an admitted native row must not appear running after its process exit was handled")
				require.True(t, row.EndedAt.Valid, "the retained row must receive its process completion stamp")
				assert.False(t, row.EndedAt.Time.Before(row.CreatedAt.Time), "the end stamp must follow retained creation")
				assert.False(t, row.UpdatedAt.Before(row.CreatedAt.Time))
			})
		}
	}
}

func TestBackgroundTaskReplacementRejectsOldQueuedMutation(t *testing.T) {
	cases := []struct {
		name              string
		initialStatus     bgtask.Status
		replacementKey    string
		replacementStatus bgtask.Status
		collision         bool
		linked            bool
		selfRenameClaim   bool
		mutate            func(agent.ProviderServices, string) error
	}{
		{
			name: "upsert", linked: true,
			mutate: func(sink agent.ProviderServices, originalChildID string) error {
				return sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: "shared", Kind: bgtask.KindSubagent, ChildAgentID: originalChildID, Title: "Obsolete native title",
					GroupKey: "obsolete-group", GroupLabel: "Obsolete group", Description: "Obsolete native description",
					Status: bgtask.StatusFailed, ActiveForm: "Obsolete native activity",
				})
			},
		},
		{name: "status", mutate: func(sink agent.ProviderServices, _ string) error {
			return sink.UpdateBackgroundTaskStatus("shared", bgtask.StatusFailed, "Obsolete native activity")
		}},
		{name: "close", mutate: func(sink agent.ProviderServices, _ string) error {
			return sink.CloseBackgroundTask("shared", bgtask.StatusFailed)
		}},
		{name: "revive", initialStatus: bgtask.StatusSucceeded, replacementStatus: bgtask.StatusSucceeded,
			mutate: func(sink agent.ProviderServices, _ string) error { return sink.ReviveBackgroundTask("shared") }},
		{name: "rename to a free key", mutate: func(sink agent.ProviderServices, _ string) error {
			return sink.RenameBackgroundTask("shared", "obsolete-destination")
		}},
		{name: "rename with a newer source", collision: true, mutate: func(sink agent.ProviderServices, _ string) error {
			return sink.RenameBackgroundTask("shared", "occupied")
		}},
		{name: "rename with a newer destination", replacementKey: "occupied", collision: true,
			mutate: func(sink agent.ProviderServices, _ string) error {
				return sink.RenameBackgroundTask("shared", "occupied")
			}},
		{name: "self-rename claims unchanged work", selfRenameClaim: true,
			mutate: func(sink agent.ProviderServices, _ string) error {
				return sink.CloseBackgroundTask("shared", bgtask.StatusFailed)
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, original := setupRootSink(t, "registry-replacement", withClock(clock))
			var originalChildID string
			if tc.linked {
				var err error
				originalChildID, err = original.EnsureChildAgent(agent.ChildAgentSpec{
					SpawnSpanID: "original-spawn", ProviderChildKey: "original-native-child", Title: "Original child",
				})
				require.NoError(t, err)
			}
			status := tc.initialStatus
			if status == bgtask.StatusUnspecified {
				status = bgtask.StatusRunning
			}
			require.NoError(t, original.UpsertBackgroundTask(bgtask.Upsert{
				RowKey: "shared", Kind: bgtask.KindSubagent, ChildAgentID: originalChildID, Title: "Original assignment", Status: status,
			}))
			if tc.collision {
				require.NoError(t, original.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: "occupied", Kind: bgtask.KindSubagent, Title: "Original rename destination", Status: bgtask.StatusRunning,
				}))
			}
			queue, release := retainBackgroundTaskMutation(t, original, func() error { return tc.mutate(original, originalChildID) })
			replacement := newTestRootOutputSink(t, svc.Output, "registry-replacement", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			require.Same(t, queue, agent.OutputQueueFor(replacement), "process replacement must retain the existing root queue")
			if tc.selfRenameClaim {
				require.NoError(t, replacement.RenameBackgroundTask("shared", "shared"))
			} else {
				key := tc.replacementKey
				if key == "" {
					key = "shared"
				}
				var childID string
				if tc.linked {
					var err error
					childID, err = replacement.EnsureChildAgent(agent.ChildAgentSpec{
						SpawnSpanID: "replacement-spawn", ProviderChildKey: "replacement-native-child", Title: "Replacement child",
					})
					require.NoError(t, err)
				}
				status := tc.replacementStatus
				if status == bgtask.StatusUnspecified {
					status = bgtask.StatusRunning
				}
				require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: key, Kind: bgtask.KindSubagent, ChildAgentID: childID, Title: "Replacement native assignment",
					GroupKey: "replacement-group", GroupLabel: "Replacement group", Description: "Replacement native description",
					ActiveForm: "Replacement native activity", Status: status,
				}))
			}
			before, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "registry-replacement")
			require.NoError(t, err)
			displayedBefore, err := svc.Output.LoadBackgroundTasks(t.Context(), "registry-replacement")
			require.NoError(t, err)
			release()
			require.NoError(t, queue.Flush(), "a superseded native mutation must complete without retrying against its replacement")
			require.False(t, queue.HasPending())
			after, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "registry-replacement")
			require.NoError(t, err)
			displayedAfter, err := svc.Output.LoadBackgroundTasks(t.Context(), "registry-replacement")
			require.NoError(t, err)
			assert.Equal(t, before, after, "the old physical writer must change no stored replacement field or rename key")
			assert.Equal(t, displayedBefore, displayedAfter, "a rejected mutation must not hide, admit, or resequence a replacement row")
		})
	}
}

func TestBackgroundTaskExitDoesNotEndIndependentReplacementWork(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, original := setupRootSink(t, "independent-registry-exit", withClock(clock))
	queue, release := retainBackgroundTaskMutation(t, original, func() error {
		return original.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "outgoing", Kind: bgtask.KindWorkflow, Title: "Outgoing assignment", Status: bgtask.StatusRunning,
		})
	})
	// The launch paths can publish NewRootSink before the old process's exit callback runs.
	replacement := newTestRootOutputSink(t, svc.Output, "independent-registry-exit", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{
		RowKey: "replacement", Kind: bgtask.KindWorkflow, Title: "Replacement assignment", Status: bgtask.StatusRunning,
	}))
	freshBefore := storedRow(t, svc, "independent-registry-exit", "replacement")
	svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "independent-registry-exit", Err: nil, Stopped: true, Output: original})
	release()
	require.NoError(t, queue.Flush())
	outgoing := storedRow(t, svc, "independent-registry-exit", "outgoing")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusStopped), outgoing.Status,
		"accepted independent history must finish even after another physical writer starts")
	assert.Equal(t, freshBefore, storedRow(t, svc, "independent-registry-exit", "replacement"),
		"the outgoing process's finalizer must leave independent replacement work active")
}

func TestBackgroundTaskExitPreservesOnlyWorkingReplacementRowsInHiddenActivity(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, original := setupRootSink(t, "hidden-registry-exit", withClock(clock))
	queue, release := retainBackgroundTaskMutation(t, original, func() error {
		return original.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: "outgoing", Kind: bgtask.KindWorkflow, Title: "Outgoing assignment", Status: bgtask.StatusRunning,
		})
	})
	replacement := newTestRootOutputSink(t, svc.Output, "hidden-registry-exit", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	for _, row := range []struct {
		key    string
		status bgtask.Status
	}{{"hidden-paused", bgtask.StatusPaused}, {"hidden-running", bgtask.StatusRunning}} {
		_, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{
			SpawnSpanID: row.key + "-spawn", ProviderChildKey: row.key, Title: row.key,
		})
		require.NoError(t, err)
		require.NoError(t, replacement.UpdateBackgroundTaskStatus(row.key, row.status, "Replacement native activity"))
	}
	for i := int32(0); i < bgtask.MaxTasks; i++ {
		require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{
			RowKey: fmt.Sprintf("replacement-filler-%03d", i), Kind: bgtask.KindSubagent,
			Title: "Paused replacement assignment", Status: bgtask.StatusPaused,
		}))
	}
	keys := displayedRowKeys(t, svc, "hidden-registry-exit")
	require.NotContains(t, keys, "hidden-paused")
	require.NotContains(t, keys, "hidden-running")
	cache := svc.Output.bgTaskCache("hidden-registry-exit")
	cache.Mu.Lock()
	beforeCount := cache.EvictedActiveCount()
	cache.Mu.Unlock()
	require.Equal(t, int32(1), beforeCount, "Paused stays open but contributes no hidden working activity")

	svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "hidden-registry-exit", Err: nil, Stopped: true, Output: original})
	release()
	require.NoError(t, queue.Flush())
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusPaused), storedRow(t, svc, "hidden-registry-exit", "hidden-paused").Status)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning), storedRow(t, svc, "hidden-registry-exit", "hidden-running").Status)
	cache.Mu.Lock()
	afterCount := cache.EvictedActiveCount()
	cache.Mu.Unlock()
	assert.Equal(t, int32(1), afterCount, "a filtered outgoing sweep must retain Running and exclude Paused from hidden activity")
}

func TestBackgroundTaskFinalStatusActivityUpdatePreservesItsEndStamp(t *testing.T) {
	for _, replace := range []bool{false, true} {
		for _, final := range []bgtask.Status{bgtask.StatusSucceeded, bgtask.StatusFailed} {
			t.Run(fmt.Sprintf("replacement=%t/final=%s", replace, final), func(t *testing.T) {
				t.Parallel()
				svc, sink, ownerID, _ := setupBgTaskTestWithService(t)
				require.NoError(t, sink.UpsertBackgroundTask(bgtask.Upsert{
					RowKey: "finished", Kind: bgtask.KindSubagent, Title: "Finished native assignment", Status: bgtask.StatusSucceeded,
				}))
				before := storedRow(t, svc, ownerID, "finished")
				require.True(t, before.EndedAt.Valid)
				if replace {
					sink = newTestRootOutputSink(t, svc.Output, ownerID, leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
				}
				require.NoError(t, sink.UpdateBackgroundTaskStatus("finished", final, "A later final native detail"),
					"an existing end stamp is a legitimate idempotent stamp no-op")
				after := storedRow(t, svc, ownerID, "finished")
				assert.Equal(t, leapmuxv1.BackgroundTaskStatus(final), after.Status)
				assert.Equal(t, "A later final native detail", after.ActiveForm)
				assert.Equal(t, before.EndedAt, after.EndedAt)
				assert.Equal(t, before.CreatedAt, after.CreatedAt)
			})
		}
	}
}

// failedOutputSourceStore refuses the original session restoration or the checked allocation source.
// Both constructor implementations must leave the existing actor authoritative on refusal.
type failedOutputSourceStore struct {
	db.DBTX
	agentID string
	failed  atomic.Bool
}

func (store *failedOutputSourceStore) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	source := strings.HasPrefix(query, "-- name: GetAgentByID :one") || strings.HasPrefix(query, "-- name: AllocateAgentOutputGeneration :one")
	if source && len(args) == 1 && args[0] == store.agentID && store.failed.CompareAndSwap(false, true) {
		return store.DBTX.QueryRowContext(ctx, "SELECT missing_output_source_column")
	}
	return store.DBTX.QueryRowContext(ctx, query, args...)
}

func TestOutputSinkSourceReadFailurePreservesTheOriginalActor(t *testing.T) {
	t.Parallel()
	svc, services := setupRootSink(t, "source-read-failure")
	services.UpdateSessionID("original-native-session")
	original := requireRootOutputSink(t, svc.Output, "source-read-failure")
	svc.Output.queries = db.New(&failedOutputSourceStore{DBTX: svc.DB, agentID: "source-read-failure"})
	_, err := svc.Output.NewRootSink("source-read-failure", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.Error(t, err)
	assert.Same(t, original, svc.Output.sinkForAgent("source-read-failure"),
		"a failed source read must not retire or replace the original output actor")
	assert.True(t, original.registeredCurrent())
	assert.Equal(t, "original-native-session", original.currentMessageSessionID())
	original.progress.mu.Lock()
	closed := original.progress.closed
	original.progress.mu.Unlock()
	assert.False(t, closed, "the original progress publisher must remain usable after construction refusal")
}

func TestOutputSinkChildServicesKeepTheirOriginalPhysicalRoot(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached-child=%t", cached), func(t *testing.T) {
			t.Parallel()
			svc, original := setupRootSink(t, "physical-root")
			root := requireRootOutputSink(t, svc.Output, "physical-root")
			childID, err := original.EnsureChildAgent(agent.ChildAgentSpec{
				SpawnSpanID: "native-spawn", ProviderChildKey: "native-child", AgentSessionID: "native-child-session",
			})
			require.NoError(t, err)
			if cached {
				original.ChildSink(childID)
			}
			replacement := newTestRootOutputSink(t, svc.Output, "physical-root", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			currentRoot := requireRootOutputSink(t, svc.Output, "physical-root")
			current := replacement.ChildSink(childID)
			currentMessage := current.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"The replacement child answer."}`)}, agent.SpanInfo{})
			currentOwner := currentMessage.Publication.Owner().(*transcriptOwner)
			historical := original.ChildSink(childID)
			oldMessage := historical.CaptureMessage(agent.MessageContent{Original: []byte(`{"content":"The outgoing child answer."}`)}, agent.SpanInfo{})
			oldOwner := oldMessage.Publication.Owner().(*transcriptOwner)
			assert.Same(t, root, oldOwner.sink.root, "a captured parent must not resolve a replacement output actor")
			assert.Same(t, currentRoot, currentOwner.sink.root)
			assert.NotSame(t, oldOwner.sink, currentOwner.sink)
			assert.False(t, oldOwner.IsCurrent())
			assert.Same(t, currentOwner.sink, svc.Output.sinkForAgent(childID), "a historical child cannot replace the current child registration")
			_, childRegisteredAsRoot := svc.Output.rootSinks.Load(childID)
			assert.False(t, childRegisteredAsRoot)
		})
	}
}

func TestBackgroundTaskUnchangedObservationClaimsItsPhysicalWriter(t *testing.T) {
	for _, operation := range []string{"upsert", "status", "finished close", "active revive", "self rename", "paused child link", "collision winner"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			clock := testutil.NewQuartzMock(t)
			svc, original := setupRootSink(t, "unchanged-generation", withClock(clock))
			task := bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindSubagent, Title: "The unchanged native assignment", Status: bgtask.StatusRunning}
			if operation == "paused child link" {
				_, err := original.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "shared-spawn", ProviderChildKey: "shared", Title: task.Title})
				require.NoError(t, err)
				require.NoError(t, original.UpdateBackgroundTaskStatus("shared", bgtask.StatusPaused, "Paused native assignment"))
			} else {
				if operation == "finished close" {
					task.Status = bgtask.StatusSucceeded
				}
				require.NoError(t, original.UpsertBackgroundTask(task))
			}
			if operation == "collision winner" {
				require.NoError(t, original.UpsertBackgroundTask(bgtask.Upsert{RowKey: "provisional", Kind: bgtask.KindSubagent, Title: "Provisional assignment", Status: bgtask.StatusRunning}))
			}
			before := storedRow(t, svc, "unchanged-generation", "shared")
			queue, release := retainBackgroundTaskMutation(t, original, func() error {
				if bgtask.Status(before.Status).IsFinished() {
					return original.ReviveBackgroundTask("shared")
				}
				return original.CloseBackgroundTask("shared", bgtask.StatusFailed)
			})
			replacement := newTestRootOutputSink(t, svc.Output, "unchanged-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			switch operation {
			case "upsert":
				require.NoError(t, replacement.UpsertBackgroundTask(task))
			case "status":
				require.NoError(t, replacement.UpdateBackgroundTaskStatus("shared", bgtask.StatusRunning, ""))
			case "finished close":
				require.NoError(t, replacement.CloseBackgroundTask("shared", bgtask.StatusSucceeded))
			case "active revive":
				require.NoError(t, replacement.ReviveBackgroundTask("shared"))
			case "self rename":
				require.NoError(t, replacement.RenameBackgroundTask("shared", "shared"))
			case "paused child link":
				_, err := replacement.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "shared-spawn", ProviderChildKey: "shared", Title: task.Title})
				require.NoError(t, err)
			case "collision winner":
				require.NoError(t, replacement.RenameBackgroundTask("provisional", "shared"))
			}
			before.OutputGeneration = requireRootOutputSink(t, svc.Output, "unchanged-generation").outputGeneration
			assert.Equal(t, before, storedRow(t, svc, "unchanged-generation", "shared"), "claiming unchanged work must preserve every visible field and timestamp")
			release()
			require.NoError(t, queue.Flush())
			assert.Equal(t, before, storedRow(t, svc, "unchanged-generation", "shared"), "the unchanged observation must revoke the earlier writer's mutation authority")
		})
	}
}

func TestBackgroundTaskQueuedExitCannotCloseReclaimedSameKey(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, original := setupRootSink(t, "reclaimed-generation", withClock(clock))
	old := bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindWorkflow, Title: "Original workflow", Status: bgtask.StatusRunning}
	require.NoError(t, original.UpsertBackgroundTask(old))
	queue, release := retainBackgroundTaskMutation(t, original, func() error { return original.UpsertBackgroundTask(old) })
	replacement := newTestRootOutputSink(t, svc.Output, "reclaimed-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindWorkflow, Title: "Replacement workflow", ActiveForm: "Replacement activity", Status: bgtask.StatusRunning}))
	before := storedRow(t, svc, "reclaimed-generation", "shared")
	svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "reclaimed-generation", Stopped: true, Output: original})
	release()
	require.NoError(t, queue.Flush())
	assert.Equal(t, before, storedRow(t, svc, "reclaimed-generation", "shared"))
}

func TestBackgroundTaskClaimStorageFailureRetainsItsImmutableWrite(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, original := setupRootSink(t, "claim-storage-generation", withClock(clock))
	task := bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindWorkflow, Title: "Unchanged workflow", Status: bgtask.StatusRunning}
	require.NoError(t, original.UpsertBackgroundTask(task))
	before := storedRow(t, svc, "claim-storage-generation", "shared")
	replacement := newTestRootOutputSink(t, svc.Output, "claim-storage-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER refuse_generation_claim BEFORE UPDATE OF output_generation ON agent_background_tasks
WHEN NEW.owner_agent_id = 'claim-storage-generation' BEGIN SELECT RAISE(ABORT, 'generation claim refused'); END`)
	require.NoError(t, err)
	queue := agent.OutputQueueFor(replacement)
	timer := clock.Trap().NewTimer(agent.OutputRetryTimerTag)
	t.Cleanup(timer.Close)
	queue.Append(agent.OutputWrite{Label: "claim an unchanged native workflow", Run: func() error { return replacement.UpsertBackgroundTask(task) }})
	require.ErrorContains(t, queue.Flush(), "generation claim refused")
	require.True(t, queue.HasPending())
	assert.Equal(t, before, storedRow(t, svc, "claim-storage-generation", "shared"))
	ctx := testutil.DeadlineContext(t)
	delay := testutil.WaitForTimer(t, ctx, timer)
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER refuse_generation_claim")
	require.NoError(t, err)
	clock.Advance(delay).MustWait(ctx)
	testutil.RequireEventually(t, func() bool { return !queue.HasPending() })
	before.OutputGeneration = requireRootOutputSink(t, svc.Output, "claim-storage-generation").outputGeneration
	assert.Equal(t, before, storedRow(t, svc, "claim-storage-generation", "shared"))
	require.NoError(t, original.CloseBackgroundTask("shared", bgtask.StatusFailed))
	assert.Equal(t, before, storedRow(t, svc, "claim-storage-generation", "shared"))
}

func TestBackgroundTaskRejectedSQLRestoresClaimAndDisplayAdmission(t *testing.T) {
	for _, operation := range []string{"status", "upsert"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			svc, original := setupRootSink(t, "rollback-generation")
			_, err := original.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "retained-spawn", ProviderChildKey: "retained", Title: "Retained assignment"})
			require.NoError(t, err)
			require.NoError(t, original.CloseBackgroundTask("retained", bgtask.StatusSucceeded))
			require.NoError(t, original.UpsertBackgroundTask(bgtask.Upsert{RowKey: "victim", Kind: bgtask.KindSubagent, Title: "Unlinked eviction victim", Status: bgtask.StatusSucceeded}))
			fillSubagentDisplayCap(t, original, bgtask.MaxTasks-1)
			require.NotContains(t, displayedRowKeys(t, svc, "rollback-generation"), "retained")
			require.Contains(t, displayedRowKeys(t, svc, "rollback-generation"), "victim")
			replacement := newTestRootOutputSink(t, svc.Output, "rollback-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			before, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "rollback-generation")
			require.NoError(t, err)
			cache := svc.Output.bgTaskCache("rollback-generation")
			cache.Mu.Lock()
			mirror := cache.mirrorLocked()
			cache.Mu.Unlock()
			_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER reject_provisional_status BEFORE UPDATE OF status ON agent_background_tasks
WHEN NEW.owner_agent_id = 'rollback-generation' AND NEW.row_key = 'retained' BEGIN SELECT RAISE(IGNORE); END`)
			require.NoError(t, err)
			if operation == "status" {
				require.NoError(t, replacement.UpdateBackgroundTaskStatus("retained", bgtask.StatusFailed, "Rejected final activity"))
			} else {
				require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{RowKey: "retained", Kind: bgtask.KindSubagent, Title: "Rejected final title", Status: bgtask.StatusFailed}))
			}
			after, err := svc.Queries.ListAllAgentBackgroundTasksForResume(t.Context(), "rollback-generation")
			require.NoError(t, err)
			assert.Equal(t, before, after, "a zero-row mutation must roll back its claim, eviction, and resequence")
			cache.Mu.Lock()
			current := cache.mirrorLocked()
			cache.Mu.Unlock()
			assert.Equal(t, mirror, current)
		})
	}
}

func TestBackgroundTaskExitProjectionFailureRollsBackAndRetries(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, source := setupRootSink(t, "exit-projection-generation", withClock(clock))
	require.NoError(t, source.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindWorkflow, Title: "Native workflow", Status: bgtask.StatusRunning}))
	before := storedRow(t, svc, "exit-projection-generation", "shared")
	_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER break_exit_projection AFTER UPDATE OF status ON agent_background_tasks
WHEN NEW.owner_agent_id = 'exit-projection-generation' AND NEW.status >= %d
BEGIN UPDATE agent_background_tasks SET created_at = 'invalid-worker-time' WHERE owner_agent_id = NEW.owner_agent_id AND row_key = NEW.row_key; END`, bgtask.MinFinalStatus))
	require.NoError(t, err)
	svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "exit-projection-generation", Stopped: true, Output: source})
	queue := agent.OutputQueueFor(source)
	require.True(t, queue.HasPending(), "the failed post-sweep projection remains owned by the original output queue")
	assert.Equal(t, before, storedRow(t, svc, "exit-projection-generation", "shared"), "the failed projection must roll back the SQL sweep too")
	_, err = svc.DB.ExecContext(t.Context(), "DROP TRIGGER break_exit_projection")
	require.NoError(t, err)
	require.NoError(t, queue.Flush())
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusStopped), storedRow(t, svc, "exit-projection-generation", "shared").Status)
	assert.False(t, queue.HasPending())
}

func TestBackgroundTaskExitRechecksShutdownAfterAdmission(t *testing.T) {
	for _, ownedStart := range []bool{false, true} {
		for _, shutdownBeforeCapture := range []bool{false, true} {
			t.Run(fmt.Sprintf("owned-start=%t/shutdown-before-capture=%t", ownedStart, shutdownBeforeCapture), func(t *testing.T) {
				t.Parallel()
				clock := testutil.NewQuartzMock(t)
				svc, source := setupRootSink(t, "shutdown-generation", withClock(clock))
				queue, release := retainBackgroundTaskMutation(t, source, func() error {
					return source.UpsertBackgroundTask(bgtask.Upsert{RowKey: "shared", Kind: bgtask.KindWorkflow, Title: "Interrupted by Worker shutdown", Status: bgtask.StatusRunning})
				})
				if shutdownBeforeCapture {
					svc.Output.SetShuttingDown()
				}
				if ownedStart {
					_, err := svc.Agents.StartAgentWith(t.Context(), agent.Options{AgentID: "shutdown-generation", WorkingDir: t.TempDir()}, source,
						func(context.Context, agent.Options, agent.ProviderServices) (agent.Agent, error) {
							return nil, assert.AnError
						})
					require.ErrorIs(t, err, assert.AnError)
				} else {
					svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "shutdown-generation", Stopped: true, Output: source})
				}
				svc.Output.SetShuttingDown()
				release()
				require.NoError(t, queue.Flush())
				assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning), storedRow(t, svc, "shutdown-generation", "shared").Status,
					"Worker shutdown leaves the row for the next boot's Interrupted sweep")
			})
		}
	}
}

func TestBackgroundTaskCapWarningAllowsSynchronousSourceReplacement(t *testing.T) {
	svc, source := setupRootSink(t, "cap-warning-generation")
	for i := int32(0); i < bgtask.MaxTasks; i++ {
		require.NoError(t, source.UpsertBackgroundTask(bgtask.Upsert{RowKey: fmt.Sprintf("active-%d", i), Kind: bgtask.KindShell, Title: "Active native command", Status: bgtask.StatusRunning}))
	}
	logger := slog.Default()
	var warnings int
	var cacheFree bool
	slog.SetDefault(slog.New(controlFailureLogObserver{Handler: slog.NewTextHandler(io.Discard, nil), observe: func(record slog.Record) {
		if record.Message != "The background task registry reached its display cap. It drops the oldest active display row because no row finished." {
			return
		}
		warnings++
		cache := svc.Output.bgTaskCache("cap-warning-generation")
		if !cache.Mu.TryLock() {
			return
		}
		cache.Mu.Unlock()
		cacheFree = true
		newTestRootOutputSink(t, svc.Output, "cap-warning-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	}}))
	t.Cleanup(func() { slog.SetDefault(logger) })
	require.NoError(t, source.UpsertBackgroundTask(bgtask.Upsert{RowKey: "last-active", Kind: bgtask.KindShell, Title: "Last native command", Status: bgtask.StatusRunning}))
	assert.Equal(t, 1, warnings)
	assert.True(t, cacheFree, "the real cap warning must run after cache publication and unlock")
	assert.Equal(t, "Last native command", storedRow(t, svc, "cap-warning-generation", "last-active").Title)
}

func TestBackgroundTaskOwnedStartFailureFinalizesExactlyItsAttempt(t *testing.T) {
	t.Parallel()
	clock := testutil.NewQuartzMock(t)
	svc, older := setupRootSink(t, "owned-start-generation", withClock(clock))
	require.NoError(t, older.UpsertBackgroundTask(bgtask.Upsert{RowKey: "older-unregistered", Kind: bgtask.KindWorkflow, Title: "Older still-starting work", Status: bgtask.StatusRunning}))
	future := time.Date(2099, time.January, 1, 0, 0, 0, 0, time.UTC)
	_, err := svc.DB.ExecContext(t.Context(), "UPDATE agent_background_tasks SET created_at=? WHERE owner_agent_id=? AND row_key=?", sqltime.NewSQLiteTime(future), "owned-start-generation", "older-unregistered")
	require.NoError(t, err)
	oldBefore := storedRow(t, svc, "owned-start-generation", "older-unregistered")
	attempt := newTestRootOutputSink(t, svc.Output, "owned-start-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
	var normalExits atomic.Int64
	svc.Agents.SetOnExit(func(agent.ProcessExit) { normalExits.Add(1) })
	var queue *agent.OutputQueue
	var release func()
	var freshShared, freshIndependent db.AgentBackgroundTask
	var cleaned bool
	failure := errors.New("the owned native start failed after cleanup")
	_, err = svc.Agents.StartAgentWith(t.Context(), agent.Options{AgentID: "owned-start-generation", AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, WorkingDir: t.TempDir()}, attempt,
		func(_ context.Context, _ agent.Options, output agent.ProviderServices) (agent.Agent, error) {
			defer func() { cleaned = true }()
			queue, release = retainBackgroundTaskMutation(t, output, func() error {
				for _, key := range []string{"failed-only", "shared"} {
					if err := output.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindWorkflow, Title: "Owned failed assignment", Status: bgtask.StatusRunning}); err != nil {
						return err
					}
				}
				return nil
			})
			// Replacement publication can precede the older attempt's error return.
			replacement := newTestRootOutputSink(t, svc.Output, "owned-start-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
			for _, key := range []string{"shared", "new-only"} {
				require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{RowKey: key, Kind: bgtask.KindWorkflow, Title: "Replacement assignment", Status: bgtask.StatusRunning}))
			}
			freshShared = storedRow(t, svc, "owned-start-generation", "shared")
			freshIndependent = storedRow(t, svc, "owned-start-generation", "new-only")
			return nil, failure
		})
	require.ErrorIs(t, err, failure)
	require.True(t, cleaned)
	assert.Zero(t, normalExits.Load(), "failed start must not run agent-wide registered-process cleanup")
	require.NotNil(t, release)
	release()
	require.NoError(t, queue.Flush())
	owned := storedRow(t, svc, "owned-start-generation", "failed-only")
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted), owned.Status)
	require.True(t, owned.EndedAt.Valid)
	assert.False(t, owned.EndedAt.Time.Before(owned.CreatedAt.Time))
	assert.True(t, owned.EndedAt.Time.Before(future), "an unrelated older generation cannot determine the owned failure timestamp")
	assert.Equal(t, oldBefore, storedRow(t, svc, "owned-start-generation", "older-unregistered"))
	assert.Equal(t, freshShared, storedRow(t, svc, "owned-start-generation", "shared"))
	assert.Equal(t, freshIndependent, storedRow(t, svc, "owned-start-generation", "new-only"))
	assert.False(t, svc.Agents.HasAgent("owned-start-generation"))
}

func TestBackgroundTaskVirtualChildCapturesNoPhysicalProcessCompletion(t *testing.T) {
	t.Parallel()
	svc, parent := setupRootSink(t, "virtual-completion-generation")
	childID, err := parent.EnsureChildAgent(agent.ChildAgentSpec{SpawnSpanID: "spawn", ProviderChildKey: "child"})
	require.NoError(t, err)
	child := parent.ChildSink(childID)
	before := storedRow(t, svc, "virtual-completion-generation", "child")
	assert.Nil(t, child.CaptureBackgroundTaskExit(false))
	assert.Nil(t, child.CaptureBackgroundTaskExit(true))
	assert.Nil(t, child.CaptureBackgroundTaskStartFailure())
	assert.Equal(t, before, storedRow(t, svc, "virtual-completion-generation", "child"))
}

func TestBackgroundTaskExitPublicationAllowsReadersAndSourceReplacement(t *testing.T) {
	t.Parallel()
	svc, original := setupRootSink(t, "exit-observer-generation")
	require.NoError(t, original.UpsertBackgroundTask(bgtask.Upsert{RowKey: "outgoing", Kind: bgtask.KindWorkflow, Title: "Outgoing workflow", Status: bgtask.StatusRunning}))
	writer := &turnAdmissionWatchingWriter{testResponseWriter: &testResponseWriter{channelID: "exit-generation-observer"}}
	var entered, cacheFree, readerCompleted atomic.Bool
	writer.onEvent = func(event *leapmuxv1.AgentEvent) {
		completion := false
		for _, task := range event.GetBackgroundTasksChanged().GetTasks() {
			completion = completion || task.GetId() == "outgoing" && task.GetStatus() == leapmuxv1.BackgroundTaskStatus(bgtask.StatusStopped)
		}
		if !completion || !entered.CompareAndSwap(false, true) {
			return
		}
		cache := svc.Output.bgTaskCache("exit-observer-generation")
		if !cache.Mu.TryLock() {
			return
		}
		cache.Mu.Unlock()
		cacheFree.Store(true)
		svc.Agents.HasAgent("exit-observer-generation")
		readerCompleted.Store(true)
		replacement := newTestRootOutputSink(t, svc.Output, "exit-observer-generation", leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE)
		require.NoError(t, replacement.UpsertBackgroundTask(bgtask.Upsert{RowKey: "replacement", Kind: bgtask.KindWorkflow, Title: "Replacement workflow", Status: bgtask.StatusRunning}))
	}
	registerAgentWatch(svc, writer.channelID, "exit-observer-generation", leapmuxv1.WatchMode_WATCH_MODE_FULL, writer)
	svc.HandleAgentProcessExit(agent.ProcessExit{AgentID: "exit-observer-generation", Stopped: true, Output: original})
	assert.True(t, entered.Load())
	assert.True(t, cacheFree.Load())
	assert.True(t, readerCompleted.Load())
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusStopped), storedRow(t, svc, "exit-observer-generation", "outgoing").Status)
	assert.Equal(t, leapmuxv1.BackgroundTaskStatus(bgtask.StatusRunning), storedRow(t, svc, "exit-observer-generation", "replacement").Status)
}
