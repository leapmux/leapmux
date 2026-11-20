package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/pathutil"
)

var (
	errSelectedWorktreeChanged  = errors.New("the selected Git worktree changed")
	errSelectedWorktreeUnproved = errors.New("the Worker could not verify the selected Git worktree's filesystem identity")
)

// worktreeDirectoryID identifies a filesystem object for as long as it exists.
// Creation time distinguishes file indexes reused on a filesystem.
type worktreeDirectoryID struct {
	Volume             uint64
	FileIndex          uint64
	CreatedAtUnixNanos int64
}

func (id worktreeDirectoryID) known() bool { return id.CreatedAtUnixNanos != 0 }

// selectedWorktreeIdentity records the native checkout selected by an open.
// CloseTab retains this proof after creation clears private runtime inputs.
type selectedWorktreeIdentity struct {
	Path       string
	CommonDir  string
	GitDir     string
	CheckoutID worktreeDirectoryID
	GitDirID   worktreeDirectoryID
}

func captureSelectedWorktree(info *gitPathInfo) (selectedWorktreeIdentity, error) {
	checkoutID, err := worktreeDirectoryIdentity(info.TopLevel)
	if err != nil {
		return selectedWorktreeIdentity{}, fmt.Errorf("read the worktree directory identity: %w", err)
	}
	gitDirID, err := worktreeDirectoryIdentity(info.GitDir)
	if err != nil {
		return selectedWorktreeIdentity{}, fmt.Errorf("read the worktree Git directory identity: %w", err)
	}
	return selectedWorktreeIdentity{
		Path: info.TopLevel, CommonDir: info.CommonDir, GitDir: info.GitDir,
		CheckoutID: checkoutID, GitDirID: gitDirID,
	}, nil
}

func (selection selectedWorktreeIdentity) verify(info *gitPathInfo, removing bool) error {
	if !info.IsWorktree || !pathutil.SamePath(selection.Path, info.TopLevel) ||
		!pathutil.SamePath(selection.CommonDir, info.CommonDir) || !pathutil.SamePath(selection.GitDir, info.GitDir) {
		return errSelectedWorktreeChanged
	}
	if removing && (!selection.CheckoutID.known() || !selection.GitDirID.known()) {
		return errSelectedWorktreeUnproved
	}
	current, err := captureSelectedWorktree(info)
	if err != nil {
		return fmt.Errorf("%w: %w", errSelectedWorktreeChanged, err)
	}
	if selection.CheckoutID.known() && selection.CheckoutID != current.CheckoutID ||
		selection.GitDirID.known() && selection.GitDirID != current.GitDirID {
		return errSelectedWorktreeChanged
	}
	return nil
}

func (selection selectedWorktreeIdentity) read(ctx context.Context) (*gitPathInfo, error) {
	info, err := queryGitPathInfo(ctx, selection.Path)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errSelectedWorktreeChanged, err)
	}
	if err := selection.verify(info, false); err != nil {
		return nil, err
	}
	return info, nil
}

func (svc *Service) verifyTabWorktreeForLaunch(ctx context.Context, tabType leapmuxv1.TabType, tabID, workingDir string) error {
	selection, err := svc.readTabWorktreeSelection(ctx, tabType, tabID, svc.RegisteredBy().String())
	if err != nil || selection == nil {
		return err
	}
	if !pathutil.SamePath(workingDir, selection.Path) {
		return errSelectedWorktreeChanged
	}
	_, err = selection.read(ctx)
	return err
}

func (svc *Service) readTabWorktreeSelection(ctx context.Context, tabType leapmuxv1.TabType, tabID, userID string) (*selectedWorktreeIdentity, error) {
	data, err := svc.Queries.GetTabCreationSelectedWorktree(ctx, db.GetTabCreationSelectedWorktreeParams{
		UserID: userID, WorkerID: svc.WorkerID, TabID: tabID, TabType: int64(tabType),
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	var selection selectedWorktreeIdentity
	if err := json.Unmarshal(data, &selection); err != nil {
		return nil, fmt.Errorf("decode the tab's selected worktree: %w", err)
	}
	if selection.Path == "" || selection.CommonDir == "" || selection.GitDir == "" {
		return nil, errors.New("the tab's selected worktree identity is incomplete")
	}
	return &selection, nil
}
