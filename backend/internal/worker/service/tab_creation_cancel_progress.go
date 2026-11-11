package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/leapmux/leapmux/internal/util/userid"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// Nullable checkpoints distinguish prepared, retired, and removed resources.
// Retirement is stored with the close write, so an existing closed row never
// invents authority to delete its worktree.
type creationCancelProgress struct {
	Snapshot   *tabCloseSnapshot
	Retirement *tabCloseRetirement
	Removal    *worktreeRemovalCheckpoint
}
type creationCancelJournal struct {
	svc      *Service
	key      db.GetTabCreationParams
	progress creationCancelProgress
}

func loadCreationCancelJournal(svc *Service, row db.TabCreation) (*creationCancelJournal, error) {
	journal := &creationCancelJournal{svc: svc, key: db.GetTabCreationParams{UserID: row.UserID, WorkerID: row.WorkerID, TabID: row.TabID}}
	if row.CancelProgressPayload == nil {
		return journal, nil
	}
	if err := json.Unmarshal(row.CancelProgressPayload, &journal.progress); err != nil {
		return nil, fmt.Errorf("decode cancellation progress: %w", err)
	}
	progress := journal.progress
	if progress.Snapshot == nil {
		return nil, errors.New("cancellation progress has no close snapshot")
	}
	snapshot := progress.Snapshot
	if snapshot.TabType != leapmuxv1.TabType(row.TabType) || snapshot.TabID != row.TabID || snapshot.UserID != row.UserID || int64(snapshot.Action) != row.CancelAction.Int64 || snapshot.Result == nil {
		return nil, errors.New("cancellation progress does not match its admitted close")
	}
	if progress.Removal != nil && (progress.Retirement == nil || !progress.Retirement.RetiredLiveRow || snapshot.Worktree == nil) {
		return nil, errors.New("cancellation removal has no live-retirement proof")
	}
	return journal, nil
}

func (j *creationCancelJournal) saveUsing(ctx context.Context, q *db.Queries, progress creationCancelProgress) error {
	ownerID, ownerOK := userid.New(j.key.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	payload, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	changed, err := q.SaveTabCreationCancelProgress(ctx, db.SaveTabCreationCancelProgressParams{CancelProgressPayload: payload, UserID: ownerID.String(), WorkerID: j.key.WorkerID, TabID: j.key.TabID, State: creationCancelRequested})
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("the cancellation no longer owns its progress record")
	}
	return nil
}
func (j *creationCancelJournal) save(ctx context.Context, progress creationCancelProgress) error {
	if err := j.saveUsing(ctx, j.svc.Queries, progress); err != nil {
		return err
	}
	j.progress = progress
	return nil
}
func (j *creationCancelJournal) Checkpoint() *worktreeRemovalCheckpoint { return j.progress.Removal }
func (j *creationCancelJournal) TargetIdentity() *worktreeRemovalIdentity {
	return j.progress.Snapshot.RemovalIdentity
}
func (*creationCancelJournal) TrackingCleanupFailed(err error) error {
	return &closeProgressWriteError{err}
}
func (j *creationCancelJournal) SaveIntent(ctx context.Context, checkpoint worktreeRemovalCheckpoint) error {
	progress := j.progress
	progress.Removal = &checkpoint
	return j.save(ctx, progress)
}
func (j *creationCancelJournal) SaveRemoved(ctx context.Context, checkpoint worktreeRemovalCheckpoint) error {
	checkpoint.DirectoryRemoved = true
	progress := j.progress
	progress.Removal = &checkpoint
	return j.save(ctx, progress)
}

func (j *creationCancelJournal) prepare(ctx context.Context, row db.TabCreation) error {
	ownerID, ownerOK := userid.New(j.key.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	queryKey := j.key
	queryKey.UserID = ownerID.String()
	if j.progress.Snapshot != nil {
		if j.progress.Retirement == nil {
			stop, err := j.svc.cancellationStopAction(ctx, row, leapmuxv1.WorktreeAction(row.CancelAction.Int64))
			if err != nil {
				return err
			}
			stop()
		}
		return nil
	}
	action := leapmuxv1.WorktreeAction(row.CancelAction.Int64)
	stop, err := j.svc.cancellationStopAction(ctx, row, action)
	if err != nil {
		return err
	}
	snapshot := j.svc.prepareTabClose(ctx, leapmuxv1.TabType(row.TabType), row.TabID, row.UserID, action, dropWorktreeLink, stop)
	if snapshot.LookupFailed {
		return fmt.Errorf("capture cancellation worktree: %s", snapshot.Result.GetFailureDetail())
	}
	progress := j.progress
	progress.Snapshot = snapshot
	if err := j.save(ctx, progress); err != nil {
		// A failed write can have an unknown outcome. Release the fresh marker
		// only when a read confirms that no durable snapshot owns it.
		current, readErr := j.svc.Queries.GetTabCreation(ctx, queryKey)
		if readErr == nil && current.CancelProgressPayload == nil {
			return errors.Join(err, releaseWorktreeRemovalIdentity(snapshot.RemovalIdentity))
		}
		return err
	}
	return nil
}

func (j *creationCancelJournal) retire(ctx context.Context) error {
	if j.progress.Retirement != nil {
		return nil
	}
	snapshot := j.progress.Snapshot
	tx, err := j.svc.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	q := j.svc.Queries.WithTx(tx)
	key := newOwnedTabKey(snapshot.TabType, snapshot.TabID, snapshot.UserID)
	retirement, err := j.svc.retireTabResource(ctx, q, key)
	if err != nil {
		return err
	}
	progress := j.progress
	progress.Retirement = &retirement
	if err := j.saveUsing(ctx, q, progress); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	j.progress = progress
	return nil
}

// cleanupAfterResult runs only after the recorded close result commits. Keeping
// progress until cleanup succeeds makes a crash after that commit recoverable.
func (j *creationCancelJournal) cleanupAfterResult(ctx context.Context, release func(*worktreeRemovalIdentity) error) error {
	ownerID, ownerOK := userid.New(j.key.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	if j.progress.Snapshot == nil {
		return nil
	}
	if err := release(j.TargetIdentity()); err != nil {
		return err
	}
	return j.svc.Queries.ClearTabCreationCancelProgress(ctx, db.ClearTabCreationCancelProgressParams{UserID: ownerID.String(), WorkerID: j.key.WorkerID, TabID: j.key.TabID, State: creationCanceled})
}

func (svc *Service) cleanupCompletedCreationCancellation(ctx context.Context, row db.TabCreation) error {
	ownerID, ownerOK := userid.New(row.UserID)
	if !ownerOK {
		return errCreationOwnerMissing
	}
	unlock := svc.creations.lock(creationKey{ownerID.String(), row.TabID})
	defer unlock()
	current, err := svc.Queries.GetTabCreation(ctx, db.GetTabCreationParams{UserID: ownerID.String(), WorkerID: row.WorkerID, TabID: row.TabID})
	if err != nil {
		return err
	}
	if current.State != creationCanceled {
		return errors.New("the cancellation result has not committed")
	}
	if current.CancelProgressPayload == nil {
		return nil
	}
	journal, err := loadCreationCancelJournal(svc, current)
	if err != nil {
		return err
	}
	return journal.cleanupAfterResult(ctx, releaseWorktreeRemovalIdentity)
}

// cancellationStopAction reuses the ordinary close teardown after startup joined.
func (svc *Service) cancellationStopAction(ctx context.Context, row db.TabCreation, action leapmuxv1.WorktreeAction) (func(), error) {
	ownerID, ownerOK := userid.New(row.UserID)
	if !ownerOK {
		return nil, errCreationOwnerMissing
	}
	switch kind := leapmuxv1.TabType(row.TabType); kind {
	case leapmuxv1.TabType_TAB_TYPE_AGENT:
		resource, err := svc.Queries.GetAgentByID(ctx, row.TabID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if resource.ParentAgentID.Valid {
			return nil, errors.New("a creation cancellation cannot retire a virtual child")
		}
		return func() { svc.stopRootAgentTabForClose(ctx, row.UserID, row.TabID, resource, action, dropWorktreeLink) }, nil
	case leapmuxv1.TabType_TAB_TYPE_TERMINAL:
		resource, err := svc.Queries.GetTerminalQuakeAndWorkingDir(ctx, row.TabID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		known := err == nil
		return func() { svc.stopTerminalTabForClose(row.UserID, row.TabID, resource, known, action, dropWorktreeLink) }, nil
	case leapmuxv1.TabType_TAB_TYPE_FILE, leapmuxv1.TabType_TAB_TYPE_IMAGE:
		resource, err := svc.Queries.GetWorkerTabPayload(ctx, db.GetWorkerTabPayloadParams{UserID: ownerID.String(), TabID: row.TabID})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return func() { svc.stopPayloadTabForClose(row.UserID, row.TabID, kind, resource.WorkingDir, dropWorktreeLink) }, nil
	default:
		return nil, errors.New("the creation has no closeable resource type")
	}
}
