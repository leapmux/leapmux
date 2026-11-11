package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func savedPayloadCancellation(t *testing.T, svc *Service, directory string) db.TabCreation {
	t.Helper()
	const id = "saved-cancel"
	payload := &leapmuxv1.TabPayload{WorkingDir: directory, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(directory, "file.txt")}}}
	require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: "user-1", TabID: id, Payload: payload}))
	publication, err := proto.Marshal(&leapmuxv1.TabPublication{Creation: creationIntent(id), TabType: leapmuxv1.TabType_TAB_TYPE_FILE})
	require.NoError(t, err)
	require.NoError(t, svc.Queries.InsertTabCreation(t.Context(), db.InsertTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id, TabType: int64(leapmuxv1.TabType_TAB_TYPE_FILE), RequestHash: make([]byte, 32), RequestPayload: []byte{}, PublicationPayload: publication, RuntimePayload: []byte{}, PublicationResult: []byte{}, State: creationPending}))
	changed, err := svc.Queries.RequestTabCreationCancel(t.Context(), db.RequestTabCreationCancelParams{State: creationCancelRequested, CancelAction: sql.NullInt64{Int64: int64(leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE), Valid: true}, UserID: "user-1", WorkerID: svc.WorkerID, TabID: id, State_2: creationPending})
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	row, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: id})
	require.NoError(t, err)
	return row
}

func TestCreationCancellation_RecoversAfterDirectoryRemoval(t *testing.T) {
	t.Parallel()
	for _, boundary := range []string{"record removal", "delete tracking"} {
		t.Run(boundary, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			repo := initRepo(t)
			worktree := createWorktreeForTest(t, svc, repo, "recover-removal")
			row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
			trigger := `CREATE TRIGGER fail_cancel_checkpoint BEFORE UPDATE OF cancel_progress_payload ON tab_creations WHEN json_extract(CAST(NEW.cancel_progress_payload AS TEXT), '$.Removal.DirectoryRemoved') = 1 BEGIN SELECT RAISE(ABORT, 'record removal failed'); END`
			if boundary == "delete tracking" {
				trigger = `CREATE TRIGGER fail_cancel_checkpoint BEFORE UPDATE OF deleted_at ON worktrees BEGIN SELECT RAISE(ABORT, 'delete tracking failed'); END`
			}
			_, err := svc.DB.ExecContext(t.Context(), trigger)
			require.NoError(t, err)
			_, err = svc.runCreationCancellation(t.Context(), row)
			require.Error(t, err)
			_, err = os.Stat(worktree.WorkingDir)
			require.ErrorIs(t, err, os.ErrNotExist, "Git must finish before the injected storage failure")
			_, err = svc.DB.ExecContext(t.Context(), `DROP TRIGGER fail_cancel_checkpoint`)
			require.NoError(t, err)
			result, err := svc.runCreationCancellation(t.Context(), row)
			require.NoError(t, err, "a reloaded journal must finish after its directory disappears")
			assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval())
			completed, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
			require.NoError(t, err)
			assert.Equal(t, creationCanceled, completed.State)
			replayed, err := recordedCreationCloseResponse(completed)
			require.NoError(t, err)
			assert.True(t, proto.Equal(result, replayed))
		})
	}
}

func TestCreationCancellation_RollsBackRetirementWhenProgressWriteFails(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	row := savedPayloadCancellation(t, svc, t.TempDir())
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_retirement BEFORE UPDATE OF cancel_progress_payload ON tab_creations WHEN json_type(CAST(NEW.cancel_progress_payload AS TEXT), '$.Retirement') = 'object' BEGIN SELECT RAISE(ABORT, 'record retirement failed'); END`)
	require.NoError(t, err)
	require.Error(t, journal.retire(t.Context()))
	_, err = svc.TabPayloads.Get(t.Context(), row.UserID, row.TabID)
	require.NoError(t, err, "the payload deletion and retirement proof must roll back together")
	assert.Nil(t, journal.progress.Retirement)
}

func TestCreationCancellation_PreservesRecreatedWorktreeWithSameBranchAndCommit(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "recreated-removal")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_removed BEFORE UPDATE OF cancel_progress_payload ON tab_creations WHEN json_extract(CAST(NEW.cancel_progress_payload AS TEXT), '$.Removal.DirectoryRemoved') = 1 BEGIN SELECT RAISE(ABORT, 'record removal failed'); END`)
	require.NoError(t, err)
	_, err = svc.runCreationCancellation(t.Context(), row)
	require.Error(t, err)
	_, err = os.Stat(worktree.WorkingDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	_, err = svc.DB.ExecContext(t.Context(), `DROP TRIGGER fail_removed`)
	require.NoError(t, err)
	run(t, repo, "git", "worktree", "add", worktree.WorkingDir, "recreated-removal")
	marker := filepath.Join(worktree.WorkingDir, "preserve.txt")
	require.NoError(t, os.WriteFile(marker, []byte("preserve replacement\n"), 0o600))
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err, "the completed cancellation records the refused removal")
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, "preserve replacement\n", string(data))
	assert.True(t, localBranchExists(t, repo, "recreated-removal"))
}

type failRemovalIntentJournal struct{ memoryWorktreeRemovalJournal }

func (j *failRemovalIntentJournal) SaveIntent(ctx context.Context, checkpoint worktreeRemovalCheckpoint) error {
	_ = j.memoryWorktreeRemovalJournal.SaveIntent(ctx, checkpoint)
	return context.Canceled
}

func TestCreationCancellation_PreservesChangedWorktreeAfterRemovalIntent(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "original-intent")
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
	require.NoError(t, err)
	identity, err := svc.captureWorktreeRemovalIdentity(t.Context(), tracked, newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "intent-test", "user-1"))
	require.NoError(t, err)
	journal := &failRemovalIntentJournal{memoryWorktreeRemovalJournal{identity: identity}}
	require.ErrorIs(t, svc.removeWorktreeFromDiskRecorded(t.Context(), tracked, journal), context.Canceled)
	run(t, worktree.WorkingDir, "git", "checkout", "-b", "replacement-intent")
	marker := filepath.Join(worktree.WorkingDir, "preserve.txt")
	require.NoError(t, os.WriteFile(marker, []byte("preserve replacement\n"), 0o600))
	err = svc.removeWorktreeFromDiskRecorded(t.Context(), tracked, &journal.memoryWorktreeRemovalJournal)
	require.Error(t, err, "a saved intent must not authorize removing a different live branch")
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, "preserve replacement\n", string(data))
}

func TestCreationCancellation_UsesConfiguredPayloadEvents(t *testing.T) {
	t.Parallel()
	for _, configured := range []bool{true, false} {
		t.Run(map[bool]string{true: "distinct bus", false: "no bus"}[configured], func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			bus := NewPrivateEventsBus()
			t.Cleanup(bus.Stop)
			configuredEvents := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
			otherEvents := make(chan *leapmuxv1.WorkerPrivateEvent, 1)
			bus.subscribers["user-1"] = map[string]chan *leapmuxv1.WorkerPrivateEvent{"test": configuredEvents}
			svc.PrivateEvents.subscribers["user-1"] = map[string]chan *leapmuxv1.WorkerPrivateEvent{"test": otherEvents}
			if configured {
				svc.TabPayloads.events = bus
			} else {
				svc.TabPayloads.events = nil
			}
			svc.applyTabCloseEffects(t.Context(), newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "payload", "user-1"), tabCloseRetirement{PayloadRevoked: true})
			assert.Len(t, otherEvents, 0, "the Service bus must not replace the payload store's dependency")
			if configured {
				require.Len(t, configuredEvents, 1)
				assert.Equal(t, "payload", (<-configuredEvents).GetTabPayloadRevoked().GetTabId())
			} else {
				assert.Empty(t, configuredEvents)
			}
		})
	}
}

func TestCreationCancellation_RejectsWrongTypeBeforeResultReplay(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	row := savedPayloadCancellation(t, svc, t.TempDir())
	response, err := proto.Marshal(&leapmuxv1.CloseTabResponse{DescendantAgentIds: []string{"private-child"}})
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `UPDATE tab_creations SET tab_type = ?, state = ?, close_response_payload = ? WHERE tab_id = ?`, int64(leapmuxv1.TabType_TAB_TYPE_AGENT), creationCanceled, response, row.TabID)
	require.NoError(t, err)
	w := newTestWriter()
	caller := channel.NewCaller(userid.MustNew(row.UserID), mustScopes("worker:read file:read git:write"))
	require.True(t, svc.cancelPendingCreation(t.Context(), caller, &leapmuxv1.CloseTabRequest{Tab: &leapmuxv1.TabIdent{TabId: row.TabID, TabType: leapmuxv1.TabType_TAB_TYPE_FILE}, WorktreeAction: leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE}, w))
	assert.Empty(t, w.responsesSnapshot())
	require.Len(t, w.rejections(), 1)
	assert.Equal(t, codeInvalidArgument, w.rejections()[0].code)
}

func TestCreationCancellation_RepeatsTeardownFromPreparedCheckpoint(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	directory := t.TempDir()
	_ = savedAgentCreation(t, svc, creationStartupIntent{Plan: gitModePlan{Mode: gitModeUseCurrent, WorkingDir: directory, PlannedWorkingDir: directory}})
	changed, err := svc.Queries.RequestTabCreationCancel(t.Context(), db.RequestTabCreationCancelParams{State: creationCancelRequested, CancelAction: sql.NullInt64{Int64: int64(leapmuxv1.WorktreeAction_WORKTREE_ACTION_KEEP), Valid: true}, UserID: "user-1", WorkerID: svc.WorkerID, TabID: "saved", State_2: creationPublished})
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	stops := 0
	require.True(t, svc.agentCleanups.register("saved", func() { stops++ }))
	key := db.GetTabCreationParams{UserID: "user-1", WorkerID: svc.WorkerID, TabID: "saved"}
	row, err := svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.Equal(t, 1, stops)
	svc.agentCleanups = cleanupRegistry{} // A restarted Worker has a fresh runtime registry.
	require.True(t, svc.agentCleanups.register("saved", func() { stops++ }))
	row, err = svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	recovered, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, recovered.prepare(t.Context(), row))
	assert.Equal(t, 2, stops, "PREPARED proves context capture, not process teardown after a restart")
}

func TestCreationCancellation_OrdinaryCloseKeepsSuccessfulRemovalAfterTrackingFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "ordinary-tracking-failure")
	const tabID = "ordinary-file"
	require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: "user-1", TabID: tabID, Payload: &leapmuxv1.TabPayload{WorkingDir: worktree.WorkingDir, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(worktree.WorkingDir, "file.txt")}}}}))
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_tracking_cleanup BEFORE UPDATE OF deleted_at ON worktrees BEGIN SELECT RAISE(ABORT, 'tracking cleanup failed'); END`)
	require.NoError(t, err)
	result := svc.closePayloadTabCommon("user-1", tabID, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
	_, err = os.Stat(worktree.WorkingDir)
	require.ErrorIs(t, err, os.ErrNotExist)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval())
	assert.Empty(t, result.GetFailureMessage(), "the directory removal succeeded; ordinary tracking cleanup remains best effort")
}

func TestCreationCancellation_IgnoresAnUncommittedPartialMarker(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "partial-marker")
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
	require.NoError(t, err)
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "partial-marker", "user-1")
	identity, err := svc.captureWorktreeRemovalIdentity(t.Context(), tracked, key)
	require.NoError(t, err)
	marker := filepath.Join(identity.GitDir, identity.MarkerName)
	// No PREPARED record exists. Model an interrupted first publication.
	require.NoError(t, os.WriteFile(marker, []byte("partial"), 0o600))
	recovered, err := svc.captureWorktreeRemovalIdentity(t.Context(), tracked, key)
	require.NoError(t, err, "an incomplete marker cannot permanently poison an unprepared close")
	assert.NotEqual(t, identity.Token, recovered.Token)
	require.NoError(t, validateWorktreeRemovalIdentity(t.Context(), tracked, recovered))
}

func TestCreationCancellation_ReleasesMarkerAfterRefusedRemoval(t *testing.T) {
	t.Parallel()
	for _, durable := range []bool{true, false} {
		t.Run(map[bool]string{true: "durable cancellation", false: "ordinary close"}[durable], func(t *testing.T) {
			t.Parallel()
			svc, _, _ := setupTestService(t)
			repo := initRepo(t)
			worktree := createWorktreeForTest(t, svc, repo, "kept-marker")
			tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
			require.NoError(t, err)
			directory, err := removalGitDir(t.Context(), tracked)
			require.NoError(t, err)
			run(t, repo, "git", "worktree", "lock", worktree.WorkingDir)
			row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
			if durable {
				_, err = svc.runCreationCancellation(t.Context(), row)
				require.NoError(t, err)
			} else {
				result := svc.closePayloadTabCommon(row.UserID, row.TabID, leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.WorktreeAction_WORKTREE_ACTION_REMOVE, dropWorktreeLink)
				assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
			}
			files, err := filepath.Glob(filepath.Join(directory, "leapmux-removal-*"))
			require.NoError(t, err)
			assert.Empty(t, files, "a completed close that keeps the worktree must release its marker")
			assert.DirExists(t, worktree.WorkingDir)
		})
	}
}

func TestCreationCancellation_RecoversMarkerCleanupAfterResultCommit(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "completed-marker")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	identity := journal.TargetIdentity()
	marker := filepath.Join(identity.GitDir, identity.MarkerName)
	response, err := proto.Marshal(&leapmuxv1.CloseTabResponse{WorktreeRemoval: leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED})
	require.NoError(t, err)
	// Model the committed-result boundary before its marker cleanup runs.
	_, err = svc.DB.ExecContext(t.Context(), `UPDATE tab_creations SET state = ?, close_response_payload = ? WHERE tab_id = ?`, creationCanceled, response, row.TabID)
	require.NoError(t, err)
	require.NoError(t, svc.ResolvePendingTabCreations(t.Context()))
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
	completed, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
	require.NoError(t, err)
	assert.True(t, completed.CancelProgressPayload == nil, "cleanup must clear its saved progress")
	replayed, err := recordedCreationCloseResponse(completed)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, replayed.GetWorktreeRemoval())
	assert.DirExists(t, worktree.WorkingDir, "cleanup after result commit must never repeat destructive removal")
}

func TestCreationCancellation_NeverRepairsASavedMarker(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "saved-marker")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	identity := journal.progress.Snapshot.RemovalIdentity
	require.NotNil(t, identity)
	marker := filepath.Join(identity.GitDir, identity.MarkerName)
	require.NoError(t, os.WriteFile(marker, []byte("changed"), 0o600))
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	assert.Equal(t, "changed", string(data), "a saved identity is evidence to validate, never permission to replace its marker")
	assert.DirExists(t, worktree.WorkingDir)
}

func TestCreationCancellation_CommittedPayloadCloseUsesTheStoredKind(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	row := savedPayloadCancellation(t, svc, t.TempDir())
	_, err := svc.DB.ExecContext(t.Context(), `UPDATE tab_creations SET state = ?, cancel_action = NULL WHERE tab_id = ?`, creationCommitted, row.TabID)
	require.NoError(t, err)
	w := newTestWriter()
	caller := channel.NewCaller(userid.MustNew(row.UserID), mustScopes("worker:read file:read"))
	svc.handleCloseTab(t.Context(), caller, &leapmuxv1.CloseTabRequest{Tab: &leapmuxv1.TabIdent{TabId: row.TabID, TabType: leapmuxv1.TabType_TAB_TYPE_IMAGE}}, w)
	assert.Empty(t, w.rejections())
	require.Len(t, w.responsesSnapshot(), 1)
	_, err = svc.TabPayloads.Get(t.Context(), row.UserID, row.TabID)
	assert.ErrorIs(t, err, ErrTabPayloadNotFound)
}

func TestCreationCancellation_ReleasesUnpublishedMarkerAfterPrepareWriteFailure(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "prepare-write-failure")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
	require.NoError(t, err)
	directory, err := removalGitDir(t.Context(), tracked)
	require.NoError(t, err)
	_, err = svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_prepared BEFORE UPDATE OF cancel_progress_payload ON tab_creations BEGIN SELECT RAISE(ABORT, 'save prepared failed'); END`)
	require.NoError(t, err)
	_, err = svc.runCreationCancellation(t.Context(), row)
	require.ErrorContains(t, err, "save prepared failed")
	markers, err := filepath.Glob(filepath.Join(directory, "leapmux-removal-*"))
	require.NoError(t, err)
	assert.Empty(t, markers, "a failed PREPARED write owns no persisted marker")
	_, err = svc.TabPayloads.Get(t.Context(), row.UserID, row.TabID)
	assert.NoError(t, err, "preparation failure must precede payload retirement")
}

func TestCreationCancellation_RetainsMarkerUntilTheResultCommits(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "result-write-failure")
	run(t, repo, "git", "worktree", "lock", worktree.WorkingDir)
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	_, err := svc.DB.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER fail_cancel_result BEFORE UPDATE OF state ON tab_creations WHEN NEW.state = %d BEGIN SELECT RAISE(ABORT, 'save result failed'); END`, creationCanceled))
	require.NoError(t, err)
	_, err = svc.runCreationCancellation(t.Context(), row)
	require.ErrorContains(t, err, "save result failed")
	key := db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID}
	current, err := svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	require.Equal(t, creationCancelRequested, current.State)
	journal, err := loadCreationCancelJournal(svc, current)
	require.NoError(t, err)
	marker := filepath.Join(journal.TargetIdentity().GitDir, journal.TargetIdentity().MarkerName)
	_, err = os.Stat(marker)
	require.NoError(t, err, "retryable cancellation must retain its identity")
	_, err = svc.DB.ExecContext(t.Context(), `DROP TRIGGER fail_cancel_result`)
	require.NoError(t, err)
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	_, err = os.Stat(marker)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCreationCancellation_OverlappingCapturesOwnDistinctMarkers(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "overlapping-markers")
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
	require.NoError(t, err)
	key := newOwnedTabKey(leapmuxv1.TabType_TAB_TYPE_FILE, "same-tab", "user-1")
	first, err := svc.captureWorktreeRemovalIdentity(t.Context(), tracked, key)
	require.NoError(t, err)
	second, err := svc.captureWorktreeRemovalIdentity(t.Context(), tracked, key)
	require.NoError(t, err)
	require.NotEqual(t, first.MarkerName, second.MarkerName)
	require.NoError(t, releaseWorktreeRemovalIdentity(first))
	require.NoError(t, validateWorktreeRemovalIdentity(t.Context(), tracked, second), "releasing one close must preserve another close's authority")
	require.NoError(t, releaseWorktreeRemovalIdentity(second))
}

func TestCreationCancellation_ResumedRemovalPreservesANewLiveSibling(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "new-live-sibling")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	require.NoError(t, svc.TabPayloads.Register(t.Context(), RegisterTabPayloadParams{UserID: row.UserID, TabID: "new-sibling", Payload: &leapmuxv1.TabPayload{WorkingDir: worktree.WorkingDir, Kind: &leapmuxv1.TabPayload_File{File: &leapmuxv1.FileTabPayload{FilePath: filepath.Join(worktree.WorkingDir, "sibling.txt")}}}}))
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_STILL_REFERENCED, result.GetWorktreeRemoval())
	assert.DirExists(t, worktree.WorkingDir)
	_, err = svc.TabPayloads.Get(t.Context(), row.UserID, "new-sibling")
	assert.NoError(t, err)
	_, err = os.Stat(filepath.Join(journal.TargetIdentity().GitDir, journal.TargetIdentity().MarkerName))
	assert.ErrorIs(t, err, os.ErrNotExist)
}

func TestCreationCancellation_RefusesALockedMissingRegistration(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "locked-missing-cancel")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	run(t, repo, "git", "worktree", "lock", worktree.WorkingDir)
	require.NoError(t, os.RemoveAll(worktree.WorkingDir))
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err, "a confirmed refusal must not keep cancellation pending forever")
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_FAILED, result.GetWorktreeRemoval())
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), worktree.WorktreeID)
	require.NoError(t, err)
	assert.False(t, tracked.DeletedAt.Valid)
	assert.True(t, svc.gitStillListsWorktree(t.Context(), tracked))
	assert.True(t, localBranchExists(t, repo, "locked-missing-cancel"))
}

func TestCreationCancellation_PreservesAReplacementTrackingRecord(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "replacement-tracking")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	run(t, repo, "git", "worktree", "remove", "--force", worktree.WorkingDir)
	require.NoError(t, svc.Queries.DeleteWorktree(t.Context(), worktree.WorktreeID))
	run(t, repo, "git", "worktree", "add", worktree.WorkingDir, "replacement-tracking")
	replacement, err := svc.ensureTrackedWorktree(t.Context(), worktree.WorkingDir)
	require.NoError(t, err)
	require.NotEqual(t, worktree.WorktreeID, replacement)
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval())
	tracked, err := svc.Queries.GetWorktreeByID(t.Context(), replacement)
	require.NoError(t, err)
	assert.False(t, tracked.DeletedAt.Valid)
	assert.DirExists(t, worktree.WorkingDir)
}

func TestCreationCancellation_PreservesABranchAdvancedAfterRemoval(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "advanced-after-removal")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	_, err := svc.DB.ExecContext(t.Context(), `CREATE TRIGGER fail_removed_checkpoint BEFORE UPDATE OF cancel_progress_payload ON tab_creations WHEN json_extract(CAST(NEW.cancel_progress_payload AS TEXT), '$.Removal.DirectoryRemoved') = 1 BEGIN SELECT RAISE(ABORT, 'save removal failed'); END`)
	require.NoError(t, err)
	_, err = svc.runCreationCancellation(t.Context(), row)
	require.ErrorContains(t, err, "save removal failed")
	run(t, repo, "git", "commit", "--allow-empty", "-m", "advance the retained branch")
	run(t, repo, "git", "branch", "-f", "advanced-after-removal", "HEAD")
	_, err = svc.DB.ExecContext(t.Context(), `DROP TRIGGER fail_removed_checkpoint`)
	require.NoError(t, err)
	result, err := svc.runCreationCancellation(t.Context(), row)
	require.NoError(t, err)
	assert.Equal(t, leapmuxv1.WorktreeRemovalOutcome_WORKTREE_REMOVAL_OUTCOME_REMOVED, result.GetWorktreeRemoval())
	assert.True(t, localBranchExists(t, repo, "advanced-after-removal"), "expected-ref deletion must preserve an advanced branch")
}

func TestCreationCancellation_DistinguishesEmptyAndAbsentRecordedResults(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	row := savedPayloadCancellation(t, svc, t.TempDir())
	_, err := recordedCreationCloseResponse(row)
	require.ErrorContains(t, err, "no recorded result")
	changed, err := svc.Queries.FinishTabCreationCancel(t.Context(), db.FinishTabCreationCancelParams{State: creationCanceled, CloseResponsePayload: sql.Null[[]byte]{V: []byte{}, Valid: true}, UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationCancelRequested})
	require.NoError(t, err)
	require.Equal(t, int64(1), changed)
	completed, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
	require.NoError(t, err)
	result, err := recordedCreationCloseResponse(completed)
	require.NoError(t, err)
	assert.True(t, proto.Equal(&leapmuxv1.CloseTabResponse{}, result))
}

func TestCreationCancellation_RetriesMarkerDeletionDurabilityBeforeClearingProgress(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "marker-sync-retry")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	_, err = svc.Queries.FinishTabCreationCancel(t.Context(), db.FinishTabCreationCancelParams{State: creationCanceled, CloseResponsePayload: sql.Null[[]byte]{V: []byte{}, Valid: true}, UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationCancelRequested})
	require.NoError(t, err)
	key := db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID}
	calls := 0
	syncFailure := errors.New("the directory sync failed")
	release := func(identity *worktreeRemovalIdentity) error {
		return releaseRemovalIdentity(identity, func(string) error {
			calls++
			if calls == 1 {
				return syncFailure
			}
			return nil
		})
	}
	require.ErrorIs(t, journal.cleanupAfterResult(t.Context(), release), syncFailure)
	_, err = os.Stat(filepath.Join(journal.TargetIdentity().GitDir, journal.TargetIdentity().MarkerName))
	require.ErrorIs(t, err, os.ErrNotExist, "the file removal precedes the injected sync failure")
	current, err := svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	assert.True(t, current.CancelProgressPayload != nil, "a failed durability step must retain progress")
	require.NoError(t, journal.cleanupAfterResult(t.Context(), release))
	assert.Equal(t, 2, calls, "an absent marker still needs its existing directory synchronized")
	current, err = svc.Queries.GetTabCreation(t.Context(), key)
	require.NoError(t, err)
	assert.True(t, current.CancelProgressPayload == nil)
	result, err := recordedCreationCloseResponse(current)
	require.NoError(t, err)
	assert.True(t, proto.Equal(&leapmuxv1.CloseTabResponse{}, result))
}

func TestCreationCancellation_ConcurrentCompletedCleanupPreservesTheResult(t *testing.T) {
	t.Parallel()
	svc, _, _ := setupTestService(t)
	repo := initRepo(t)
	worktree := createWorktreeForTest(t, svc, repo, "concurrent-marker-cleanup")
	row := savedPayloadCancellation(t, svc, worktree.WorkingDir)
	journal, err := loadCreationCancelJournal(svc, row)
	require.NoError(t, err)
	require.NoError(t, journal.prepare(t.Context(), row))
	require.NoError(t, journal.retire(t.Context()))
	_, err = svc.Queries.FinishTabCreationCancel(t.Context(), db.FinishTabCreationCancelParams{State: creationCanceled, CloseResponsePayload: sql.Null[[]byte]{V: []byte{}, Valid: true}, UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID, State_2: creationCancelRequested})
	require.NoError(t, err)
	row, err = svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
	require.NoError(t, err)
	var calls sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		calls.Go(func() { results <- svc.cleanupCompletedCreationCancellation(t.Context(), row) })
	}
	calls.Wait()
	for range 2 {
		assert.NoError(t, <-results)
	}
	completed, err := svc.Queries.GetTabCreation(t.Context(), db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID})
	require.NoError(t, err)
	assert.True(t, completed.CancelProgressPayload == nil)
	_, err = recordedCreationCloseResponse(completed)
	assert.NoError(t, err)
	assert.DirExists(t, worktree.WorkingDir)
}
