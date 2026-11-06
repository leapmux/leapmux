package service

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/inputqueue"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recoveryIntentStore runs each hook around the real conditional write. A
// zero-row result therefore comes from the SQL condition, not a fake result.
type recoveryIntentStore struct {
	db.DBTX
	before func(context.Context) error
	after  func(context.Context) error
	writes atomic.Int32
}

func (store *recoveryIntentStore) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if !strings.Contains(query, "-- name: MarkAgentResumedForInputRecovery :execrows") {
		return store.DBTX.ExecContext(ctx, query, args...)
	}
	store.writes.Add(1)
	if store.before != nil {
		if err := store.before(ctx); err != nil {
			return nil, err
		}
	}
	result, err := store.DBTX.ExecContext(ctx, query, args...)
	if err != nil {
		return result, err
	}
	if store.after != nil {
		if err := store.after(ctx); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func TestInputRecoveryRechecksWorkAfterResumeIntent(t *testing.T) {
	t.Parallel()
	for _, blocker := range []string{"turn", "background task", "root control", "descendant nonblocking control"} {
		t.Run(blocker, func(t *testing.T) {
			t.Parallel()
			svc, starts := newInputRecoveryService(t, func(int) func(int) error {
				return func(int) error { return fmtRestartRequired() }
			})
			const id = "late-blocker-agent"
			seedRecoveryAgent(t, svc, id, "saved-thread")
			require.NoError(t, svc.ensureAgentRunning(id, resumeIfConversation, interactiveStart))
			svc.Output.WaitActivityRefreshes()
			queries := db.New(svc.DB)
			if blocker == "descendant nonblocking control" {
				require.NoError(t, queries.CreateChildAgent(t.Context(), db.CreateChildAgentParams{
					ID: "child", ParentAgentID: sql.NullString{String: id, Valid: true},
					WorkingDir: t.TempDir(), AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CODEX,
				}))
			}
			hook := &recoveryIntentStore{DBTX: svc.DB, after: func(ctx context.Context) error {
				switch blocker {
				case "turn":
					starts.sinkOf(1).SetTurnState(agent.TurnState{Active: true}, 1)
					return nil
				case "background task":
					return starts.sinkOf(1).UpsertBackgroundTask(bgtask.Upsert{
						RowKey: "late-task", Kind: bgtask.KindShell, Title: "running command", Status: bgtask.StatusRunning,
					})
				default:
					owner := id
					if blocker == "descendant nonblocking control" {
						owner = "child"
					}
					_, err := queries.StoreControlRequest(ctx, db.StoreControlRequestParams{
						AgentID: owner, RequestID: "late-control", Payload: []byte(`{}`),
						NonBlocking: blocker == "descendant nonblocking control",
					})
					return err
				}
			}}
			svc.Queries = db.New(hook)
			enqueueRecoveryInput(t, svc, id, "retained-input")
			item := recoveryFailedItem(t, svc, id, "retained-input")
			assert.NotEmpty(t, item.Error)
			assert.Equal(t, int32(1), hook.writes.Load(), "the blocker appeared after the successful intent write")
			assert.Equal(t, 1, starts.count())
			stale := starts.fake(0)
			assert.False(t, stale.wasStopped())
			texts, _ := stale.sent()
			assert.Empty(t, texts)
			row, err := queries.GetAgentByID(t.Context(), id)
			require.NoError(t, err)
			assert.Equal(t, int64(1), row.Resumed)
			assert.Equal(t, "saved-thread", row.AgentSessionID)
			switch blocker {
			case "turn":
				assert.True(t, svc.Output.TurnActive(id))
			case "background task":
				assert.EqualValues(t, 1, svc.Output.AgentActivitySnapshot(id, id).ActiveTasks)
			default:
				owner := id
				if blocker == "descendant nonblocking control" {
					owner = "child"
				}
				controls, err := queries.ListControlRequestsByAgentID(t.Context(), owner)
				require.NoError(t, err)
				require.Len(t, controls, 1, "recovery preserves the unanswered control")
				assert.Equal(t, blocker == "descendant nonblocking control", controls[0].NonBlocking)
			}
		})
	}
}

func TestInputRecoveryIntentWriteRefusesFaultAndClosedRow(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"write fault", "closed row"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			svc, starts := newInputRecoveryService(t, func(int) func(int) error {
				return func(int) error { return fmtRestartRequired() }
			})
			const id = "intent-agent"
			seedRecoveryAgent(t, svc, id, "saved-thread")
			require.NoError(t, svc.ensureAgentRunning(id, resumeIfConversation, interactiveStart))
			svc.Output.WaitActivityRefreshes()
			queries := db.New(svc.DB)
			hook := &recoveryIntentStore{DBTX: svc.DB, before: func(ctx context.Context) error {
				if change == "write fault" {
					return assert.AnError
				}
				_, err := queries.CloseAgent(ctx, id)
				return err
			}}
			svc.Queries = db.New(hook)
			err := svc.ensureAgentReadyForInput(id)
			require.Error(t, err)
			var delivery *inputqueue.DeliveryError
			require.ErrorAs(t, err, &delivery)
			if change == "write fault" {
				require.ErrorIs(t, err, assert.AnError)
				assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
				assert.Equal(t, leapmuxv1.AgentInputQueuePauseReason_AGENT_INPUT_QUEUE_PAUSE_REASON_STORE_FAULT, delivery.PauseReason)
			} else {
				assert.Equal(t, inputqueue.DispatchNotReady, delivery.Outcome)
			}
			assert.Equal(t, int32(1), hook.writes.Load())
			assert.Equal(t, 1, starts.count())
			assert.False(t, starts.fake(0).wasStopped())
			texts, _ := starts.fake(0).sent()
			assert.Empty(t, texts)
			row, err := queries.GetAgentByID(t.Context(), id)
			require.NoError(t, err)
			assert.Zero(t, row.Resumed)
			assert.Equal(t, "saved-thread", row.AgentSessionID)
			assert.Equal(t, change == "closed row", row.ClosedAt.Valid)
		})
	}
}

func TestInputRecoveryRechecksLifecycleAfterHeldPreparation(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"close", "archive", "shutdown", "settings"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			ctx := testutil.DeadlineContext(t)
			entered := make(chan struct{})
			releasePreparation := make(chan struct{})
			release := sync.OnceFunc(func() { close(releasePreparation) })
			defer release()
			svc, starts := newInputRecoveryService(t, func(launch int) func(int) error {
				if launch != 1 {
					return nil
				}
				return func(int) error {
					close(entered)
					<-releasePreparation
					return fmtRestartRequired()
				}
			})
			const id = "preparation-agent"
			seedRecoveryAgent(t, svc, id, "saved-thread")
			require.NoError(t, svc.ensureAgentRunning(id, resumeIfConversation, interactiveStart))
			svc.Output.WaitActivityRefreshes()
			var replacementOptions agent.Options
			workingStart := svc.startAgentFn
			svc.startAgentFn = func(ctx context.Context, opts agent.Options, sink agent.ProviderServices) (map[string]string, error) {
				replacementOptions = opts
				return workingStart(ctx, opts, sink)
			}
			result := make(chan error, 1)
			go func() { result <- svc.ensureAgentReadyForInput(id) }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("preparation did not reach the barrier")
			}
			switch change {
			case "close":
				svc.closeAgentTabCommon("", id, leapmuxv1.WorktreeAction_WORKTREE_ACTION_UNSPECIFIED, dropWorktreeLink)
			case "archive":
				_, err := svc.Queries.SetAgentWorkspaceArchived(ctx, db.SetAgentWorkspaceArchivedParams{ID: id, WorkspaceArchived: true})
				require.NoError(t, err)
			case "shutdown":
				svc.Shutdown()
			case "settings":
				_, err := svc.DB.ExecContext(ctx, "UPDATE agents SET options = ? WHERE id = ?", marshalOptions(map[string]string{
					agent.OptionIDModel: "gpt-6.1", agent.OptionIDEffort: "high", agent.OptionIDPermissionMode: "default",
				}), id)
				require.NoError(t, err)
			}
			release()
			select {
			case err := <-result:
				if change == "settings" {
					require.NoError(t, err)
				} else {
					require.Error(t, err)
				}
			case <-ctx.Done():
				t.Fatal("recovery did not finish after preparation released")
			}
			if change == "settings" {
				require.Equal(t, 2, starts.count())
				assert.Equal(t, "saved-thread", replacementOptions.ResumeSessionID)
				assert.Equal(t, "gpt-6.1", replacementOptions.Options[agent.OptionIDModel])
				assert.Equal(t, "high", replacementOptions.Options[agent.OptionIDEffort])
				assert.Equal(t, "default", replacementOptions.PermissionMode())
			} else {
				assert.Equal(t, 1, starts.count(), "the lifecycle change prevents a replacement")
			}
			for index := range starts.count() {
				texts, _ := starts.fake(index).sent()
				assert.Empty(t, texts)
			}
		})
	}
}
