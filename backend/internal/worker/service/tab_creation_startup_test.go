package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/authscope"
	"github.com/leapmux/leapmux/internal/tabcreation"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// savedAgentCreation exercises the real atomic persistence and publication records.
// Its caller supplies the Git checkpoint that an earlier Worker completed.
func savedAgentCreation(t *testing.T, svc *Service, intent creationStartupIntent) *creationOperation {
	t.Helper()
	request := &leapmuxv1.OpenAgentRequest{Creation: creationIntent("saved"), WorkingDir: intent.Plan.WorkingDir}
	hash, err := tabcreation.Fingerprint(request)
	require.NoError(t, err)
	op := &creationOperation{coordinator: svc.creations, caller: channel.NewCaller(userid.MustNew("user-1"), authscope.NonAdminGrant()), request: request, publication: &leapmuxv1.TabPublication{Creation: proto.CloneOf(request.GetCreation()), TabType: leapmuxv1.TabType_TAB_TYPE_AGENT}, hash: hash}
	runtime := &agentCreationRuntime{Startup: intent}
	require.NoError(t, op.persist(runtime, func(ctx context.Context, q *db.Queries) error {
		return q.CreateAgent(ctx, db.CreateAgentParams{ID: "saved", WorkingDir: intent.Plan.PlannedWorkingDir, AgentProvider: leapmuxv1.AgentProvider_AGENT_PROVIDER_CLAUDE_CODE, Title: "Saved agent", Options: "{}"})
	}))
	published, err := fixturePublishTab(t.Context(), op.publication)
	require.NoError(t, err)
	payload, err := proto.Marshal(published)
	require.NoError(t, err)
	changed, err := svc.Queries.PublishTabCreation(t.Context(), db.PublishTabCreationParams{State: creationPublished, PublicationResult: payload, UserID: "user-1", WorkerID: svc.WorkerID, TabID: "saved", State_2: creationPending})
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	if intent.Result != nil && intent.Result.WorktreeID != "" {
		svc.registerTabForWorktree(intent.Result.WorktreeID, leapmuxv1.TabType_TAB_TYPE_AGENT, "saved")
	}
	return op
}

func TestCreationCheckpoint_ValidatesSavedGitIdentityWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unchanged", "advanced branch", "replacement repository", "different branch", "deleted tracking"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			repository := initRepo(t)
			plan, err := svc.validateGitMode(t.Context(), repository, createWorktreeSetup("feature", ""))
			require.NoError(t, err)
			intent, err := svc.prepareCreationStartup(t.Context(), plan)
			require.NoError(t, err)
			result, err := svc.executeGitMode(t.Context(), intent.Plan)
			require.NoError(t, err)
			intent.Started = true
			intent.Result = &result
			op := savedAgentCreation(t, svc, intent)
			switch scenario {
			case "unchanged":
			case "advanced branch":
				run(t, result.WorkingDir, "git", "commit", "--allow-empty", "-m", "advance saved branch")
			case "replacement repository":
				run(t, repository, "git", "worktree", "remove", "--force", result.WorkingDir)
				replacement := initRepo(t)
				require.NoError(t, os.Rename(replacement, result.WorkingDir))
			case "different branch":
				run(t, result.WorkingDir, "git", "checkout", "-b", "replacement-branch")
			case "deleted tracking":
				require.NoError(t, svc.Queries.DeleteWorktree(t.Context(), result.WorktreeID))
			default:
				t.Fatal("unsupported checkpoint scenario")
			}
			marker := filepath.Join(result.WorkingDir, "preserve.txt")
			require.NoError(t, os.WriteFile(marker, []byte("preserve existing files\n"), 0o600))
			headBefore, err := gitutil.Output(t.Context(), result.WorkingDir, "rev-parse", "HEAD")
			require.NoError(t, err)
			branchBefore, err := gitutil.Output(t.Context(), result.WorkingDir, "branch", "--show-current")
			require.NoError(t, err)
			recovered, recoveryErr := op.runCreationPhase0(t.Context())
			if scenario == "unchanged" || scenario == "advanced branch" {
				require.NoError(t, recoveryErr)
				assert.Equal(t, result.WorktreeID, recovered.WorktreeID)
				assert.False(t, recovered.Rollback.HasPartialMutation(), "a recovered checkpoint gains no deletion rights")
			} else {
				require.Error(t, recoveryErr, "a saved path alone does not establish Git identity")
			}
			content, err := os.ReadFile(marker)
			require.NoError(t, err)
			assert.Equal(t, "preserve existing files\n", string(content))
			headAfter, err := gitutil.Output(t.Context(), result.WorkingDir, "rev-parse", "HEAD")
			require.NoError(t, err)
			assert.Equal(t, headBefore, headAfter)
			branchAfter, err := gitutil.Output(t.Context(), result.WorkingDir, "branch", "--show-current")
			require.NoError(t, err)
			assert.Equal(t, strings.TrimSpace(branchBefore), strings.TrimSpace(branchAfter))
		})
	}
}

func TestCreationCheckpoint_RejectsDirectoryReplacedByFile(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	directory := filepath.Join(t.TempDir(), "working")
	require.NoError(t, os.Mkdir(directory, 0o700))
	plan := gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}
	op := savedAgentCreation(t, svc, creationStartupIntent{Plan: plan, Started: true, Result: &gitModeResult{WorkingDir: directory}})
	require.NoError(t, os.Remove(directory))
	require.NoError(t, os.WriteFile(directory, []byte("replacement file\n"), 0o600))
	_, err := op.runCreationPhase0(t.Context())
	require.Error(t, err)
	content, err := os.ReadFile(directory)
	require.NoError(t, err)
	assert.Equal(t, "replacement file\n", string(content))
}
