package service

import (
	"context"
	"log/slog"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

// tabCloseSnapshot retains the evidence captured before a resource disappears.
type tabCloseSnapshot struct {
	TabType         leapmuxv1.TabType
	TabID           string
	UserID          string
	Action          leapmuxv1.WorktreeAction
	LinkPolicy      worktreeLinkPolicy
	WorkingDir      string
	RootEligible    bool
	RemovalInfo     *gitPathInfo
	Worktree        *db.Worktree
	RemovalIdentity *worktreeRemovalIdentity
	LookupFailed    bool
	Result          *leapmuxv1.CloseTabResponse
}

// worktreeRemovalCheckpoint records the live branch selected before removal.
type worktreeRemovalCheckpoint struct {
	WorktreeID       string
	WorktreePath     string
	RepoRoot         string
	Branch           string
	Commit           string
	DirectoryRemoved bool
}

// worktreeRemovalJournal makes destructive boundaries explicit for every close.
// Ordinary closes retain checkpoints in memory; cancellation persists them.
type worktreeRemovalJournal interface {
	TargetIdentity() *worktreeRemovalIdentity
	Checkpoint() *worktreeRemovalCheckpoint
	SaveIntent(context.Context, worktreeRemovalCheckpoint) error
	SaveRemoved(context.Context, worktreeRemovalCheckpoint) error
	TrackingCleanupFailed(error) error
}
type memoryWorktreeRemovalJournal struct {
	checkpoint *worktreeRemovalCheckpoint
	identity   *worktreeRemovalIdentity
}

func (j *memoryWorktreeRemovalJournal) TargetIdentity() *worktreeRemovalIdentity { return j.identity }

func (j *memoryWorktreeRemovalJournal) Checkpoint() *worktreeRemovalCheckpoint { return j.checkpoint }
func (j *memoryWorktreeRemovalJournal) SaveIntent(_ context.Context, checkpoint worktreeRemovalCheckpoint) error {
	j.checkpoint = &checkpoint
	return nil
}
func (j *memoryWorktreeRemovalJournal) SaveRemoved(_ context.Context, checkpoint worktreeRemovalCheckpoint) error {
	checkpoint.DirectoryRemoved = true
	j.checkpoint = &checkpoint
	return nil
}

func (*memoryWorktreeRemovalJournal) TrackingCleanupFailed(err error) error {
	slog.Warn("worktree removal succeeded but tracking cleanup failed", "error", err)
	return nil
}

// closeProgressWriteError keeps persistence failure retryable after an external step.
type closeProgressWriteError struct{ cause error }

func (e *closeProgressWriteError) Error() string {
	return "persist worktree-removal progress: " + e.cause.Error()
}
func (e *closeProgressWriteError) Unwrap() error { return e.cause }
