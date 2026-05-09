package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/generated/db"
)

// FileTabPathStore is the worker-local store of (org_id, tab_id) →
// (workspace_id, file_path). The hub never sees these rows; clients
// fetch them via WatchWorkspacePrivateEvents (which carries the path
// over the existing E2EE channel) or one-shot GetFileTabPath.
type FileTabPathStore struct {
	q      *db.Queries
	events *PrivateEventsBus
}

// NewFileTabPathStore returns a store bound to the worker's DB
// queries and the bus where snapshot/live events get published.
func NewFileTabPathStore(q *db.Queries, events *PrivateEventsBus) *FileTabPathStore {
	return &FileTabPathStore{q: q, events: events}
}

// Register persists a (org_id, tab_id, workspace_id, file_path)
// tuple and broadcasts FileTabPathRegistered on the matching
// workspace's private-event stream.
func (s *FileTabPathStore) Register(ctx context.Context, p RegisterFileTabPathParams) error {
	if p.OrgID == "" || p.TabID == "" || p.WorkspaceID == "" || p.FilePath == "" {
		return fmt.Errorf("register file tab path: required field empty")
	}
	if err := s.q.UpsertWorkerFileTab(ctx, db.UpsertWorkerFileTabParams{
		OrgID:       p.OrgID,
		TabID:       p.TabID,
		WorkspaceID: p.WorkspaceID,
		FilePath:    p.FilePath,
	}); err != nil {
		return fmt.Errorf("upsert worker_file_tab: %w", err)
	}
	if s.events != nil {
		s.events.PublishFileTabPathRegistered(p.WorkspaceID, p.TabID, p.FilePath)
	}
	return nil
}

// Get returns the workspace_id and file_path for a tab, or
// ErrFileTabPathNotFound if absent.
func (s *FileTabPathStore) Get(ctx context.Context, orgID, tabID string) (workspaceID, filePath string, err error) {
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{OrgID: orgID, TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", "", ErrFileTabPathNotFound
		}
		return "", "", err
	}
	return row.WorkspaceID, row.FilePath, nil
}

// Revoke deletes the row and broadcasts FileTabPathRevoked. Returns
// ErrFileTabPathNotFound when no row exists.
func (s *FileTabPathStore) Revoke(ctx context.Context, orgID, tabID string) error {
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{OrgID: orgID, TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFileTabPathNotFound
		}
		return err
	}
	if err := s.q.DeleteWorkerFileTab(ctx, db.DeleteWorkerFileTabParams{OrgID: orgID, TabID: tabID}); err != nil {
		return fmt.Errorf("delete worker_file_tab: %w", err)
	}
	if s.events != nil {
		s.events.PublishFileTabPathRevoked(row.WorkspaceID, tabID)
	}
	return nil
}

// Relocate moves a file tab to a new workspace. Emits FileTabPathRevoked
// on the source workspace's private-event stream and
// FileTabPathRegistered on the destination workspace's stream — there
// is no "Relocated" event so destination workspace_id is never leaked
// to source-only watchers.
func (s *FileTabPathStore) Relocate(ctx context.Context, orgID, tabID, newWorkspaceID string) error {
	row, err := s.q.GetWorkerFileTab(ctx, db.GetWorkerFileTabParams{OrgID: orgID, TabID: tabID})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrFileTabPathNotFound
		}
		return err
	}
	if newWorkspaceID == "" {
		return fmt.Errorf("relocate file tab path: new_workspace_id is empty")
	}
	if row.WorkspaceID == newWorkspaceID {
		return nil
	}
	if err := s.q.UpdateWorkerFileTabWorkspace(ctx, db.UpdateWorkerFileTabWorkspaceParams{
		WorkspaceID: newWorkspaceID,
		OrgID:       orgID,
		TabID:       tabID,
	}); err != nil {
		return fmt.Errorf("update worker_file_tab.workspace_id: %w", err)
	}
	if s.events != nil {
		s.events.PublishFileTabPathRevoked(row.WorkspaceID, tabID)
		s.events.PublishFileTabPathRegistered(newWorkspaceID, tabID, row.FilePath)
	}
	return nil
}

// SnapshotForWorkspace returns the FileTabPathRegistered events the
// private-event subscribe path replays before going live, so a
// late-joining client always sees the current path set. Walks every
// row in worker_file_tabs (org boundary doesn't matter to the
// worker — it doesn't host multiple orgs in practice) and filters
// by workspace_id.
func (s *FileTabPathStore) SnapshotForWorkspace(ctx context.Context, workspaceID string) ([]*leapmuxv1.WorkspacePrivateEvent, error) {
	rows, err := s.q.ListAllWorkerFileTabs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*leapmuxv1.WorkspacePrivateEvent, 0, len(rows))
	for _, r := range rows {
		if r.WorkspaceID != workspaceID {
			continue
		}
		out = append(out, &leapmuxv1.WorkspacePrivateEvent{
			Event: &leapmuxv1.WorkspacePrivateEvent_FileTabPathRegistered{
				FileTabPathRegistered: &leapmuxv1.FileTabPathRegistered{
					TabId:       r.TabID,
					WorkspaceId: r.WorkspaceID,
					FilePath:    r.FilePath,
				},
			},
		})
	}
	return out, nil
}

// RegisterFileTabPathParams is the input shape for Register.
type RegisterFileTabPathParams struct {
	OrgID       string
	TabID       string
	WorkspaceID string
	FilePath    string
}

// ErrFileTabPathNotFound is returned when the requested tab has no
// row in worker_file_tabs.
var ErrFileTabPathNotFound = errors.New("file_tab_path: not found")
