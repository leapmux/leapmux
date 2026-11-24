package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	defaultGitLogLimit = 20
	maxGitLogLimit     = 200
)

func registerGitOpsHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, dispatchPlain, svc.handleGetGitDiff)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGetGitLogProcedure, dispatchPlain, svc.handleGetGitLog)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, dispatchTracked, svc.handleGitCommit)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, dispatchTracked, svc.handleCreateGitWorktree)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, dispatchTracked, svc.handleRemoveGitWorktree)
}

func (svc *Service) handleGetGitDiff(ctx context.Context, _ channel.Caller, r *leapmuxv1.GetGitDiffRequest, sender channel.ResponseWriter) {
	dirPath, err := validate.ResolveLiteralPath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	if r.GetLimit() < 0 {
		sendInvalidArgument(sender, "limit must not be negative")
		return
	}
	baseRef := strings.TrimSpace(r.GetBaseRef())
	if strings.HasPrefix(baseRef, "-") {
		sendInvalidArgument(sender, "base_ref must not start with '-'")
		return
	}
	filePaths := r.GetFilePaths()
	for _, fp := range filePaths {
		if strings.TrimSpace(fp) == "" {
			sendInvalidArgument(sender, "file_paths must not contain empty paths")
			return
		}
		if strings.HasPrefix(fp, "-") {
			sendInvalidArgument(sender, "file_paths must not start with '-'")
			return
		}
	}

	limit := svc.readLimit(r.GetLimit(), sender)
	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()

	args := []string{"-C", dirPath, "diff"}
	if r.GetStaged() {
		args = append(args, "--cached")
	}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	args = append(args, "--")
	args = append(args, filePaths...)

	cmd := gitutil.NewGitCmd(ctx, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	limited := &limitedByteCounter{limit: limit}
	cmd.Stdout = limited
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(strings.ToLower(msg), "not a git repository") || strings.Contains(strings.ToLower(msg), "unknown revision") || strings.Contains(strings.ToLower(msg), "bad revision") {
			sendInvalidArgument(sender, msg)
			return
		}
		sendInternalError(sender, msg)
		return
	}
	sendProtoResponse(sender, &leapmuxv1.GetGitDiffResponse{
		Diff:      sanitizeUTF8Truncated(limited.buf.Bytes(), limited.total > limit),
		Truncated: limited.total > limit,
		TotalSize: limited.total,
	})
}

// sanitizeUTF8Truncated trims a partial trailing UTF-8 sequence when truncated
// mid-rune and replaces any non-UTF-8 bytes so proto3 string marshaling never fails.
func sanitizeUTF8Truncated(b []byte, truncated bool) string {
	if truncated {
		for i := 0; i < 3 && len(b) > 0; i++ {
			if utf8.Valid(b) {
				break
			}
			// Drop trailing continuation/leading byte of a split rune.
			if utf8.RuneStart(b[len(b)-1]) {
				b = b[:len(b)-1]
				break
			}
			b = b[:len(b)-1]
		}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// limitedByteCounter keeps at most limit bytes while counting total bytes written.
type limitedByteCounter struct {
	limit int64
	total int64
	buf   bytes.Buffer
}

func (l *limitedByteCounter) Write(p []byte) (int, error) {
	l.total += int64(len(p))
	rem := l.limit - int64(l.buf.Len())
	if rem > 0 {
		if int64(len(p)) <= rem {
			l.buf.Write(p)
		} else {
			l.buf.Write(p[:rem])
		}
	}
	return len(p), nil
}

func (svc *Service) handleGetGitLog(ctx context.Context, _ channel.Caller, r *leapmuxv1.GetGitLogRequest, sender channel.ResponseWriter) {
	dirPath, err := validate.ResolveLiteralPath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	if r.GetLimit() < 0 {
		sendInvalidArgument(sender, "limit must not be negative")
		return
	}
	limit := int(r.GetLimit())
	if limit == 0 {
		limit = defaultGitLogLimit
	}
	if limit > maxGitLogLimit {
		limit = maxGitLogLimit
	}
	ref := strings.TrimSpace(r.GetRef())
	if strings.HasPrefix(ref, "-") {
		sendInvalidArgument(sender, "ref must not start with '-'")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()

	args := []string{
		"-C", dirPath, "log",
		fmt.Sprintf("-n%d", limit),
		"--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1e",
	}
	if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--")

	cmd := gitutil.NewGitCmd(ctx, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "does not have any commits yet") {
			sendProtoResponse(sender, &leapmuxv1.GetGitLogResponse{})
			return
		}
		if msg == "" {
			msg = err.Error()
		}
		if strings.Contains(lower, "not a git repository") || strings.Contains(lower, "unknown revision") || strings.Contains(lower, "bad revision") {
			sendInvalidArgument(sender, msg)
			return
		}
		sendInternalError(sender, msg)
		return
	}

	var commits []*leapmuxv1.GitCommitEntry
	for _, rec := range strings.Split(stdout.String(), "\x1e") {
		rec = strings.Trim(rec, "\r\n")
		if rec == "" {
			continue
		}
		fields := strings.SplitN(rec, "\x1f", 6)
		if len(fields) < 6 {
			continue
		}
		commits = append(commits, &leapmuxv1.GitCommitEntry{
			Hash:        fields[0],
			ShortHash:   fields[1],
			AuthorName:  strings.ToValidUTF8(fields[2], "\uFFFD"),
			AuthorEmail: strings.ToValidUTF8(fields[3], "\uFFFD"),
			AuthoredAt:  fields[4],
			Subject:     strings.ToValidUTF8(fields[5], "\uFFFD"),
		})
	}
	sendProtoResponse(sender, &leapmuxv1.GetGitLogResponse{Commits: commits})
}

func (svc *Service) handleGitCommit(_ context.Context, _ channel.Caller, r *leapmuxv1.GitCommitRequest, sender channel.ResponseWriter) {
	dirPath, err := validate.ResolveLiteralPath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	message := strings.TrimSpace(r.GetMessage())
	if message == "" {
		sendInvalidArgument(sender, "commit message is required")
		return
	}

	ctx, cancel := context.WithTimeout(bgCtx(), branchMutationTimeout)
	defer cancel()

	var resp leapmuxv1.GitCommitResponse
	err = svc.withRepoIndexLockForDir(ctx, dirPath, func(_ *gitPathInfo) error {
		if r.GetStageAll() {
			if err := runGitCommand(ctx, dirPath, nil, "add", "-A"); err != nil {
				return err
			}
		}
		if err := runGitCommand(ctx, dirPath, strings.NewReader(message+"\n"), "commit", "-F", "-"); err != nil {
			return err
		}
		out, err := gitutil.Output(ctx, dirPath, "log", "-1", "--format=%H%x1f%s")
		if err != nil {
			return fmt.Errorf("read created commit: %w", err)
		}
		parts := strings.SplitN(strings.TrimSpace(out), "\x1f", 2)
		if len(parts) == 2 {
			resp.Hash = parts[0]
			resp.Subject = parts[1]
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gitutil.ErrInvalidArgument) || errors.Is(err, errNotGitRepo) {
			sendInvalidArgument(sender, err.Error())
			return
		}
		lowerErr := strings.ToLower(err.Error())
		if isGitCommitPreconditionError(lowerErr) {
			sendFailedPrecondition(sender, err.Error())
			return
		}
		sendInternalError(sender, err.Error())
		return
	}
	sendProtoResponse(sender, &resp)
}

func isGitCommitPreconditionError(lower string) bool {
	for _, needle := range []string{
		"nothing to commit",
		"no changes added to commit",
		"nothing added to commit",
		"unmerged",
		"unresolved conflict",
		"author identity unknown",
		"please tell me who you are",
		"cannot do a partial commit during a merge",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func runGitCommand(ctx context.Context, dir string, stdin io.Reader, args ...string) error {
	full := append([]string{"-C", dir}, args...)
	cmd := gitutil.NewGitCmd(ctx, full...)
	cmd.Stdin = stdin
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(combined.String())
		if msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func (svc *Service) handleCreateGitWorktree(_ context.Context, _ channel.Caller, r *leapmuxv1.CreateGitWorktreeRequest, sender channel.ResponseWriter) {
	dirPath, err := validate.ResolveLiteralPath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}
	branch := strings.TrimSpace(r.GetBranch())
	if branch == "" {
		sendInvalidArgument(sender, "branch is required")
		return
	}
	baseBranch := strings.TrimSpace(r.GetBaseBranch())
	if strings.HasPrefix(baseBranch, "-") {
		sendInvalidArgument(sender, "base_branch must not start with '-'")
		return
	}

	var explicitPath string
	if raw := strings.TrimSpace(r.GetWorktreePath()); raw != "" {
		explicitPath, err = validate.ResolveLiteralPath(raw, svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}
	}

	ctx, cancel := context.WithTimeout(bgCtx(), branchMutationTimeout)
	defer cancel()

	plan, err := svc.validateCreateWorktreeWithPath(ctx, dirPath, branch, baseBranch, explicitPath, true)
	if err != nil {
		if strings.Contains(err.Error(), "already exists on disk") {
			sendFailedPrecondition(sender, err.Error())
			return
		}
		sendInvalidArgument(sender, err.Error())
		return
	}

	res, err := svc.executeCreateWorktree(ctx, plan)
	if err != nil {
		if res.Rollback.CreatedWorktree != nil {
			svc.rollbackGitMode(res)
		}
		sendInternalError(sender, err.Error())
		return
	}
	sendProtoResponse(sender, &leapmuxv1.CreateGitWorktreeResponse{
		WorktreePath: res.WorkingDir,
		Branch:       branch,
		RepoRoot:     res.RepoRoot,
	})
}

func (svc *Service) handleRemoveGitWorktree(_ context.Context, _ channel.Caller, r *leapmuxv1.RemoveGitWorktreeRequest, sender channel.ResponseWriter) {
	dirPath, err := validate.ResolveLiteralPath(r.GetPath(), svc.HomeDir)
	if err != nil {
		sendPermissionDenied(sender, "access denied")
		return
	}

	ctx, cancel := context.WithTimeout(bgCtx(), branchMutationTimeout)
	defer cancel()

	info, err := queryGitPathInfo(ctx, dirPath)
	if err != nil {
		if errors.Is(err, errNotGitRepo) {
			sendInvalidArgument(sender, fmt.Sprintf("%s is not inside a git repository", dirPath))
			return
		}
		sendInternalError(sender, err.Error())
		return
	}
	if !info.IsWorktree {
		sendInvalidArgument(sender, "cannot remove the main git working tree; pass a linked worktree path")
		return
	}

	// Capture the canonical path BEFORE deletion so DB tracking cleanup matches
	// even through symlinked roots (e.g. macOS /var -> /private/var).
	canonicalPath := pathutil.Canonicalize(info.TopLevel)

	// Serialize against concurrent tab admission and verify no live tab uses
	// this worktree while holding the removal write lock.
	unlock := svc.worktreeLifecycle.remove(canonicalPath)
	defer unlock()

	if inUseCount, refErr := svc.countLiveTabsInWorktree(ctx, canonicalPath); refErr != nil {
		sendInternalError(sender, fmt.Sprintf("check live tabs in worktree: %v", refErr))
		return
	} else if inUseCount > 0 {
		sendFailedPrecondition(sender, fmt.Sprintf("cannot remove worktree %s: %d live tab(s) still use it; close those tabs first", canonicalPath, inUseCount))
		return
	}

	if reason, ierr := svc.worktreeRemovalBlockedReason(ctx, info.TopLevel, info); ierr == nil && reason != "" {
		sendFailedPrecondition(sender, reason)
		return
	}

	args := []string{"worktree", "remove"}
	if r.GetForce() {
		args = append(args, "--force")
	}
	args = append(args, info.TopLevel)

	removeErr := svc.withGitIndexLock(info.CommonDir, func() error {
		return (gitutil.Repository{GitDir: info.CommonDir}).Run(ctx, args...)
	})
	if removeErr != nil {
		msg := removeErr.Error()
		if strings.Contains(strings.ToLower(msg), "contains modified or untracked files") {
			sendFailedPrecondition(sender, msg)
			return
		}
		sendInternalError(sender, msg)
		return
	}

	// Clean up any LeapMux tracking row for this worktree best-effort.
	if tracked, terr := svc.Queries.GetWorktreeByPath(ctx, canonicalPath); terr == nil {
		if delErr := svc.Queries.DeleteWorktree(ctx, tracked.ID); delErr != nil {
			slog.Warn("failed to delete removed worktree tracking row", "worktree_id", tracked.ID, "error", delErr)
		}
	} else if !errors.Is(terr, sql.ErrNoRows) {
		slog.Warn("failed to look up removed worktree row", "path", canonicalPath, "error", terr)
	}

	if r.GetDeleteBranch() && info.BranchName != "" {
		flag := "-d"
		if r.GetForce() {
			flag = "-D"
		}
		if brErr := svc.withGitIndexLock(info.CommonDir, func() error {
			return (gitutil.Repository{GitDir: info.CommonDir}).Run(ctx, "branch", flag, "--", info.BranchName)
		}); brErr != nil {
			sendFailedPrecondition(sender, fmt.Sprintf("worktree removed, but deleting branch %q failed: %v", info.BranchName, brErr))
			return
		}
	}

	sendProtoResponse(sender, &leapmuxv1.RemoveGitWorktreeResponse{})
}

func (svc *Service) countLiveTabsInWorktree(ctx context.Context, canonicalTop string) (int, error) {
	if svc.Queries == nil {
		return 0, nil
	}
	tabs, err := svc.liveWorktreeTabs(ctx)
	if err != nil {
		return 0, err
	}
	if len(tabs) == 0 {
		return 0, nil
	}
	dirs := make([]string, 0, len(tabs))
	for _, tab := range tabs {
		dirs = append(dirs, tab.linkDir)
	}
	matched := matchWorktreeDirectories(ctx, canonicalTop, dirs, newBranchInfoProbe(branchProbeConcurrency))
	count := 0
	for _, tab := range tabs {
		if matched[tab.linkDir].matches {
			count++
		}
	}
	return count, nil
}
