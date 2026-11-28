package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/id"
	"github.com/leapmux/leapmux/internal/util/sqltime"
	"github.com/leapmux/leapmux/internal/worker/bgtask"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

var errNativeSessionAlreadyOpen = errors.New("the native agent session is already open; close its tab before resuming")

// createAgentRecordForForkUsing creates a new forked root agent row and copies
// the source agent's transcript, virtual child subtree, and to-dos atomically
// inside the caller's transaction. Live runtime state (control requests, queued
// inputs, session goals) is intentionally not copied.
func (svc *Service) createAgentRecordForForkUsing(ctx context.Context, queries *db.Queries, params db.CreateAgentParams, source db.Agent) error {
	if err := validateAgentRecordProvider(params); err != nil {
		return err
	}
	// Never claim the source's native session in pending_resume_session_id: the
	// source tab may still be open and holds idx_agents_open_native_owner for it.
	params.PendingResumeSessionID = ""
	params.ForkedFromAgentID = source.ID
	params.Resumed = 1
	if err := svc.createAgentRecordUsing(ctx, queries, params); err != nil {
		return fmt.Errorf("create forked agent: %w", err)
	}
	if source.PlanFilePath != "" || source.PlanTitle != "" {
		if err := queries.UpdateAgentPlan(ctx, db.UpdateAgentPlanParams{
			PlanFilePath: source.PlanFilePath,
			PlanTitle:    source.PlanTitle,
			ID:           params.ID,
		}); err != nil {
			return fmt.Errorf("copy forked agent plan: %w", err)
		}
	}
	if err := cloneAgentTranscript(ctx, queries, source.ID, params.ID); err != nil {
		return fmt.Errorf("copy forked agent transcript: %w", err)
	}
	if err := cloneArchivedChildTree(ctx, queries, source.ID, params.ID); err != nil {
		return fmt.Errorf("copy forked agent child transcripts: %w", err)
	}
	if _, err := queries.CloneAgentTodosForFork(ctx, db.CloneAgentTodosForForkParams{
		TargetAgentID: params.ID,
		SourceAgentID: source.ID,
	}); err != nil {
		return fmt.Errorf("copy forked agent todos: %w", err)
	}
	return nil
}

// createAgentRecordWithTranscript gives a reopened LeapMux tab the Worker
// transcript tree from its last closed tab. An external native session opens
// without Worker history.
func (svc *Service) createAgentRecordWithTranscript(ctx context.Context, params db.CreateAgentParams, resumeSessionID string) error {
	tx, err := svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("start agent transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := svc.createAgentRecordWithTranscriptUsing(ctx, svc.Queries.WithTx(tx), params, resumeSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (svc *Service) createAgentRecordWithTranscriptUsing(ctx context.Context, queries *db.Queries, params db.CreateAgentParams, resumeSessionID string) error {
	if err := validateAgentRecordProvider(params); err != nil {
		return err
	}
	params.PendingResumeSessionID = resumeSessionID
	if err := svc.createAgentRecordUsing(ctx, queries, params); err != nil {
		if open, checkErr := queries.HasOpenAgentForNativeSession(ctx, db.HasOpenAgentForNativeSessionParams{
			AgentProvider: params.AgentProvider, SessionID: resumeSessionID, TargetAgentID: params.ID,
		}); resumeSessionID != "" && checkErr == nil && open {
			return errNativeSessionAlreadyOpen
		}
		return fmt.Errorf("create resumed agent: %w", err)
	}
	if resumeSessionID == "" {
		return nil
	}
	sourceID, err := queries.FindClosedAgentWithTranscriptForNativeSession(ctx, db.FindClosedAgentWithTranscriptForNativeSessionParams{
		AgentProvider:  params.AgentProvider,
		WorkingDir:     params.WorkingDir,
		AgentSessionID: resumeSessionID,
	})
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("find the prior Worker transcript: %w", err)
	}
	if err == nil {
		// The source tab can hold several native sessions after a context clear.
		// Its complete Worker transcript is what the reader saw in that tab.
		if err := cloneAgentTranscript(ctx, queries, sourceID, params.ID); err != nil {
			return fmt.Errorf("copy the prior Worker transcript: %w", err)
		}
		if err := cloneArchivedChildTree(ctx, queries, sourceID, params.ID); err != nil {
			return fmt.Errorf("copy the prior Worker child transcripts: %w", err)
		}
	}
	return nil
}

// cloneAgentTranscript copies the rows and receipts that belong to one transcript.
// The caller's transaction commits this copy with the destination agent record.
func cloneAgentTranscript(ctx context.Context, queries *db.Queries, sourceID, targetID string) error {
	if _, err := queries.CloneAgentMessagesForResume(ctx, db.CloneAgentMessagesForResumeParams{
		TargetAgentID: targetID, SourceAgentID: sourceID,
	}); err != nil {
		return fmt.Errorf("copy messages: %w", err)
	}
	if err := queries.CloneAgentTurnEndsForResume(ctx, db.CloneAgentTurnEndsForResumeParams{
		TargetAgentID: targetID, SourceAgentID: sourceID,
	}); err != nil {
		return fmt.Errorf("copy native completion receipts: %w", err)
	}
	if err := queries.CloneAgentMessageRetractionsForResume(ctx, db.CloneAgentMessageRetractionsForResumeParams{
		TargetAgentID: targetID, SourceAgentID: sourceID,
	}); err != nil {
		return fmt.Errorf("copy native eviction receipts: %w", err)
	}
	return nil
}

// cloneArchivedChildTree gives each prior virtual child a new id under the
// reopened root. The registry and every child transcript follow those ids.
func cloneArchivedChildTree(ctx context.Context, queries *db.Queries, sourceRootID, targetRootID string) error {
	children, err := queries.ListAgentDescendantsForResume(ctx, sql.NullString{String: sourceRootID, Valid: true})
	if err != nil {
		return fmt.Errorf("list archived children: %w", err)
	}
	mappedIDs := map[string]string{sourceRootID: targetRootID}
	for _, child := range children {
		if !child.ParentAgentID.Valid {
			return fmt.Errorf("archived child %q has no parent", child.ID)
		}
		newParentID, ok := mappedIDs[child.ParentAgentID.String]
		if !ok {
			return fmt.Errorf("archived child %q has an unknown parent %q", child.ID, child.ParentAgentID.String)
		}
		newChildID := id.Generate()
		count, err := queries.CloneChildAgentForResume(ctx, db.CloneChildAgentForResumeParams{
			NewChildID:  newChildID,
			NewParentID: sql.NullString{String: newParentID, Valid: true},
			OldChildID:  child.ID,
		})
		if err != nil {
			return fmt.Errorf("copy archived child %q: %w", child.ID, err)
		}
		if count != 1 {
			return fmt.Errorf("copy archived child %q: expected one row, got %d", child.ID, count)
		}
		if err := cloneAgentTranscript(ctx, queries, child.ID, newChildID); err != nil {
			return fmt.Errorf("copy archived child %q transcript: %w", child.ID, err)
		}
		mappedIDs[child.ID] = newChildID
	}

	rows, err := queries.ListAllAgentBackgroundTasksForResume(ctx, sourceRootID)
	if err != nil {
		return fmt.Errorf("list archived child registry: %w", err)
	}
	copyTime := sqltime.NewSQLiteTime(nowMillis())
	for _, row := range rows {
		newChildID, err := remapArchivedAgentID(mappedIDs, row.ChildAgentID)
		if err != nil {
			return fmt.Errorf("copy archived child registry row %q: %w", row.RowKey, err)
		}
		newParentID, err := remapArchivedAgentID(mappedIDs, row.ParentAgentID)
		if err != nil {
			return fmt.Errorf("copy archived child registry row %q: %w", row.RowKey, err)
		}
		count, err := queries.CloneAgentBackgroundTaskForResume(ctx, db.CloneAgentBackgroundTaskForResumeParams{
			NewRootID: targetRootID, NewChildID: newChildID, NewParentID: newParentID,
			MinFinalStatus:    leapmuxv1.BackgroundTaskStatus(bgtask.MinFinalStatus),
			InterruptedStatus: leapmuxv1.BackgroundTaskStatus(bgtask.StatusInterrupted),
			CopyTime:          copyTime,
			OldRootID:         sourceRootID, RowKey: row.RowKey,
		})
		if err != nil {
			return fmt.Errorf("copy archived child registry row %q: %w", row.RowKey, err)
		}
		if count != 1 {
			return fmt.Errorf("copy archived child registry row %q: expected one row, got %d", row.RowKey, count)
		}
	}
	return nil
}

func remapArchivedAgentID(mappedIDs map[string]string, oldID string) (string, error) {
	if oldID == "" {
		return "", nil
	}
	newID, ok := mappedIDs[oldID]
	if !ok {
		return "", fmt.Errorf("unknown archived child agent %q", oldID)
	}
	return newID, nil
}
