package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/agent"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func interruptPhase0Checkpoint(t *testing.T, database *sql.DB) func() {
	t.Helper()
	for _, statement := range []string{
		`CREATE TABLE runtime_write_count (n INTEGER NOT NULL)`,
		`INSERT INTO runtime_write_count(n) VALUES (0)`,
		`CREATE TRIGGER interrupt_runtime_checkpoint BEFORE UPDATE OF runtime_payload ON tab_creations WHEN (SELECT n FROM runtime_write_count) >= 1 BEGIN SELECT RAISE(ABORT, 'simulated loss before checkpoint'); END`,
		`CREATE TRIGGER count_runtime_start AFTER UPDATE OF runtime_payload ON tab_creations BEGIN UPDATE runtime_write_count SET n = n + 1; END`,
	} {
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	return func() {
		for _, statement := range []string{`DROP TRIGGER interrupt_runtime_checkpoint`, `DROP TRIGGER count_runtime_start`, `DROP TABLE runtime_write_count`} {
			_, err := database.ExecContext(t.Context(), statement)
			require.NoError(t, err)
		}
	}
}

func TestCreationRestart_RecoversInterruptedGitWithoutNewRollbackRights(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"worktree before tracking", "worktree before checkpoint", "branch before checkpoint", "checkout before checkpoint", "branch before directory", "conflicting path"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			hub := newRecoveryPublicationHub(t)
			directory, repository := t.TempDir(), initRepo(t)
			path := filepath.Join(directory, "worker.db")
			oldDatabase := openRecoveryDatabase(t, path)
			oldService, _ := newRecoveryService(t, hub.config(oldDatabase, directory))
			defer oldService.Shutdown()
			setup := createWorktreeSetup("recover-feature", "")
			if scenario == "branch before checkpoint" {
				setup = createBranchSetup("recover-feature", "")
			}
			if scenario == "checkout before checkpoint" {
				run(t, repository, "git", "branch", "recover-feature")
				setup = checkoutBranchSetup("recover-feature")
			}
			plan, err := oldService.validateGitMode(t.Context(), repository, setup)
			require.NoError(t, err)
			startup, err := oldService.prepareCreationStartup(t.Context(), plan)
			require.NoError(t, err)
			intent := hub.intent("interrupted-git")
			request := &leapmuxv1.OpenAgentRequest{Creation: intent, WorkingDir: repository, GitSetup: setup}
			runtime := &agentCreationRuntime{Startup: startup}
			op := seedRecoveryCreation(t, oldService, recoveryCreationSeed{request: request, publication: &leapmuxv1.TabPublication{Creation: intent, TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, runtime: runtime, state: creationPublished,
				write: func(ctx context.Context, q *db.Queries) error {
					return q.CreateAgent(ctx, db.CreateAgentParams{ID: intent.TabId, WorkingDir: plan.PlannedWorkingDir, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Interrupted Git", Options: "{}"})
				},
			})
			switch scenario {
			case "branch before directory":
				runtime.Startup.Started = true
				require.NoError(t, op.saveRuntime(t.Context()))
				run(t, repository, "git", "branch", plan.BranchName, startup.ExpectedCommit)
			case "conflicting path":
				runtime.Startup.Started = true
				require.NoError(t, op.saveRuntime(t.Context()))
				require.NoError(t, os.MkdirAll(plan.PlannedWorkingDir, 0o700))
			case "worktree before tracking":
				_, err := oldDatabase.ExecContext(t.Context(), `CREATE TRIGGER interrupt_worktree_tracking BEFORE INSERT ON worktrees BEGIN SELECT RAISE(ABORT, 'simulated loss before tracking'); END`)
				require.NoError(t, err)
				// The ordinary startup goroutine would roll back on this injected
				// error. Run phase 0 directly to preserve the actual crash state.
				_, err = op.runCreationPhase0(t.Context())
				require.ErrorContains(t, err, "simulated loss before tracking")
				_, err = oldDatabase.ExecContext(t.Context(), `DROP TRIGGER interrupt_worktree_tracking`)
				require.NoError(t, err)
			default:
				clearFault := interruptPhase0Checkpoint(t, oldDatabase)
				_, err := op.runCreationPhase0(t.Context())
				require.ErrorContains(t, err, "simulated loss before checkpoint")
				clearFault()
			}
			key := db.GetTabCreationParams{UserID: hub.owner.String(), WorkerID: hub.workerID, TabID: intent.TabId}
			before, err := oldService.Queries.GetTabCreation(t.Context(), key)
			require.NoError(t, err)
			inputs, err := decodeCreationRecoveryInputs(before.RuntimePayload)
			require.NoError(t, err)
			var incomplete agentCreationRuntime
			require.NoError(t, json.Unmarshal(inputs.Runtime, &incomplete))
			require.True(t, incomplete.Startup.Started)
			require.Nil(t, incomplete.Startup.Result)
			marker := filepath.Join(plan.PlannedWorkingDir, "preserve.txt")
			if scenario != "branch before directory" {
				require.NoError(t, os.WriteFile(marker, []byte("preserve interrupted files\n"), 0o600))
			}
			oldService.Shutdown()
			require.NoError(t, oldDatabase.Close())
			freshDatabase := openRecoveryDatabase(t, path)
			fresh, _ := newRecoveryService(t, hub.config(freshDatabase, directory))
			defer fresh.Shutdown()
			type startupObservation struct {
				runtime agentCreationRuntime
				state   int64
				err     error
			}
			observed := make(chan startupObservation, 2)
			var starts atomic.Int32
			fresh.startAgentFn = func(ctx context.Context, _ agent.Options, _ agent.ProviderServices) (map[string]string, error) {
				starts.Add(1)
				row, err := fresh.Queries.GetTabCreation(ctx, key)
				observation := startupObservation{state: row.State, err: err}
				if err == nil {
					inputs, decodeErr := decodeCreationRecoveryInputs(row.RuntimePayload)
					observation.err = decodeErr
					if decodeErr == nil {
						observation.err = json.Unmarshal(inputs.Runtime, &observation.runtime)
					}
				}
				if scenario == "branch before directory" && observation.err == nil {
					observation.err = os.WriteFile(marker, []byte("preserve interrupted files\n"), 0o600)
				}
				select {
				case observed <- observation:
				default:
				}
				return nil, errors.New("the provider fails after the recovered checkpoint")
			}
			ctx := creationOutcomeContext(t)
			require.NoError(t, fresh.ResolvePendingTabCreations(ctx))
			fresh.AgentStartup.WaitForInFlight()
			if scenario == "conflicting path" {
				assert.Zero(t, starts.Load())
				resource, err := fresh.Queries.GetAgentByID(ctx, intent.TabId)
				require.NoError(t, err)
				assert.Contains(t, resource.StartupError, "kept")
			} else {
				var observation startupObservation
				select {
				case observation = <-observed:
				case <-ctx.Done():
					t.Fatal("recovered Git setup did not reach provider startup")
				}
				require.NoError(t, observation.err)
				assert.Equal(t, creationPublished, observation.state)
				require.NotNil(t, observation.runtime.Startup.Result, "the checkpoint must commit before provider startup")
				assert.False(t, observation.runtime.Startup.Result.Rollback.HasPartialMutation(), "recovery cannot acquire new deletion rights")
				assert.Equal(t, int32(1), starts.Load())
				assert.Equal(t, "recover-feature", currentBranchName(t, plan.PlannedWorkingDir))
				if strings.HasPrefix(scenario, "worktree") || scenario == "branch before directory" {
					linked, err := fresh.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{TabType: leapmuxv1.TabType_TAB_TYPE_AGENT, TabID: intent.TabId})
					require.NoError(t, err)
					assert.Equal(t, observation.runtime.Startup.Result.WorktreeID, linked.ID)
					var count int
					require.NoError(t, freshDatabase.QueryRowContext(ctx, `SELECT COUNT(*) FROM worktrees WHERE deleted_at IS NULL`).Scan(&count))
					assert.Equal(t, 1, count)
				}
			}
			contents, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.Equal(t, "preserve interrupted files\n", string(contents))
			completed, err := fresh.Queries.GetTabCreation(ctx, key)
			require.NoError(t, err)
			assert.Equal(t, creationCommitted, completed.State)
		})
	}
}
