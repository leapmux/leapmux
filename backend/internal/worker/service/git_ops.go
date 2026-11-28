package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"unicode/utf8"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/worker/channel"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
	"github.com/leapmux/leapmux/util/pathutil"
	"github.com/leapmux/leapmux/util/validate"
)

const (
	defaultGitLogLimit = 20
	maxGitLogLimit     = 200
	// gitDiffProtoOverhead reserves headroom for GetGitDiffResponse protobuf
	// framing (tag + varint length + truncated bool + total_size varint) so a
	// budget-capped diff never trips channel MaxPayloadBudget after marshaling.
	gitDiffProtoOverhead = 64
)

func registerGitOpsHandlers(d ownerOnlyRegistrar, svc *Service) {
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, dispatchPlain, svc.handleGetGitDiff)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGetGitLogProcedure, dispatchPlain, svc.handleGetGitLog)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, dispatchTracked, svc.handleGitCommit)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, dispatchTracked, svc.handleCreateGitWorktree)
	registerOwnerGuarded(d, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, dispatchTracked, svc.handleRemoveGitWorktree)
}

func sendGitReadError(sender channel.ResponseWriter, stderrStr string, err error) {
	msg := strings.TrimSpace(stderrStr)
	if msg == "" {
		msg = err.Error()
	}
	if native := nativeGitExit(err); native != nil && (native.ExitCode() == 128 || native.ExitCode() == 129) {
		lower := strings.ToLower(msg)
		if strings.Contains(lower, "not a git repository") || strings.Contains(lower, "unknown revision") || strings.Contains(lower, "bad revision") {
			sendInvalidArgument(sender, msg)
			return
		}
	}
	sendInternalError(sender, msg)
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

	maxDiffBytes := max(svc.payloadBudget(sender)-gitDiffProtoOverhead, 0)
	limit := min(svc.readLimit(r.GetLimit(), sender), maxDiffBytes)
	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()

	var args []string
	if r.GetStaged() {
		args = append(args, "--cached")
	}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	args = append(args, "--")
	args = append(args, filePaths...)

	limited := &limitedByteCounter{limit: limit}
	stderr, err := gitutil.DiffToWriter(ctx, dirPath, limited, args...)
	if err != nil {
		sendGitReadError(sender, string(stderr), err)
		return
	}
	diffText, truncated := sanitizeUTF8TruncatedWithinBudget(limited.buf.Bytes(), limited.total > limit, maxDiffBytes)
	sendProtoResponse(sender, &leapmuxv1.GetGitDiffResponse{
		Diff:      diffText,
		Truncated: truncated,
		TotalSize: limited.total,
	})
}

// sanitizeUTF8Truncated trims an incomplete trailing UTF-8 sequence when b was
// cut mid-rune, then replaces any remaining non-UTF-8 bytes so proto3 string
// marshaling never fails. It inspects only the trailing utf8.UTFMax bytes so
// invalid bytes earlier in b never strip valid trailing runes.
func sanitizeUTF8Truncated(b []byte, truncated bool) string {
	if truncated && len(b) > 0 {
		// Walk back across up to utf8.UTFMax-1 continuation bytes to the start
		// of the final rune, and drop it only if the trailing sequence is incomplete.
		start := len(b) - 1
		for steps := 0; start > 0 && steps < utf8.UTFMax-1 && !utf8.RuneStart(b[start]); steps++ {
			start--
		}
		if utf8.RuneStart(b[start]) && !utf8.FullRune(b[start:]) {
			b = b[:start]
		}
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// sanitizeUTF8TruncatedWithinBudget sanitizes b and ensures the resulting UTF-8
// string fits within maxBytes (accounting for invalid-byte -> 3-byte U+FFFD
// expansion) without splitting a multi-byte rune.
func sanitizeUTF8TruncatedWithinBudget(b []byte, truncated bool, maxBytes int64) (string, bool) {
	s := sanitizeUTF8Truncated(b, truncated)
	if maxBytes >= 0 && int64(len(s)) > maxBytes {
		cut := int(maxBytes)
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
		truncated = true
	}
	return s, truncated
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
	limit, err := resolveSearchLimit(r.GetLimit(), defaultGitLogLimit, maxGitLogLimit)
	if err != nil {
		sendInvalidArgument(sender, err.Error())
		return
	}
	ref := strings.TrimSpace(r.GetRef())
	if strings.HasPrefix(ref, "-") {
		sendInvalidArgument(sender, "ref must not start with '-'")
		return
	}

	ctx, cancel := context.WithTimeout(ctx, gitReadTimeout)
	defer cancel()

	args := []string{
		"log",
		fmt.Sprintf("-n%d", limit),
		"--format=%H%x1f%h%x1f%an%x1f%ae%x1f%aI%x1f%s%x1e",
	}
	if ref != "" {
		args = append(args, ref)
	}
	args = append(args, "--")

	stdout, stderr, err := gitutil.Capture(ctx, dirPath, args...)
	if err != nil {
		if native := nativeGitExit(err); native != nil && native.ExitCode() == 128 &&
			strings.Contains(strings.ToLower(strings.TrimSpace(string(stderr))), "does not have any commits yet") {
			sendProtoResponse(sender, &leapmuxv1.GetGitLogResponse{})
			return
		}
		sendGitReadError(sender, string(stderr), err)
		return
	}

	var commits []*leapmuxv1.GitCommitEntry
	for _, rec := range strings.Split(string(stdout), "\x1e") {
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
			resp.Subject = strings.ToValidUTF8(parts[1], "\uFFFD")
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, gitutil.ErrInvalidArgument) || errors.Is(err, errNotGitRepo) {
			sendInvalidArgument(sender, err.Error())
			return
		}
		if native := nativeGitExit(err); native != nil && (native.ExitCode() == 1 || native.ExitCode() == 128) &&
			isGitCommitPreconditionError(strings.ToLower(err.Error())) {
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

type gitCommandOutputError struct {
	msg string
	err error
}

func (e *gitCommandOutputError) Error() string { return e.msg }
func (e *gitCommandOutputError) Unwrap() error { return e.err }

func runGitCommand(ctx context.Context, dir string, stdin io.Reader, args ...string) error {
	combined, err := gitutil.RunCombinedWithStdin(ctx, dir, stdin, args...)
	if err != nil {
		msg := strings.TrimSpace(string(combined))
		if msg != "" {
			return &gitCommandOutputError{msg: msg, err: err}
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
	if raw := r.GetWorktreePath(); raw != "" {
		explicitPath, err = validate.ResolveLiteralPath(raw, svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}
	}

	ctx, cancel := context.WithTimeout(bgCtx(), branchMutationTimeout)
	defer cancel()

	plan, err := svc.validateCreateWorktree(ctx, createWorktreeValidation{
		workingDir:      dirPath,
		branch:          branch,
		baseBranch:      baseBranch,
		explicitPath:    explicitPath,
		allowBaseCommit: true,
	})
	if err != nil {
		if errors.Is(err, errWorktreePathExists) {
			sendFailedPrecondition(sender, err.Error())
			return
		}
		sendValidationError(sender, err)
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

	var (
		preconditionErr error
		internalErr     error
	)
	_ = svc.withGitIndexLock(info.CommonDir, func() error {
		liveInfo, probeErr := queryGitPathInfo(ctx, info.TopLevel)
		if probeErr != nil {
			if _, statErr := os.Stat(info.TopLevel); errors.Is(statErr, os.ErrNotExist) &&
				!svc.gitStillListsWorktree(ctx, db.Worktree{WorktreePath: info.TopLevel, RepoRoot: info.RepoRoot, RepoGitDir: info.CommonDir}) {
				// Already removed concurrently.
				return nil
			}
			internalErr = probeErr
			return nil
		}
		if !liveInfo.IsWorktree || !pathutil.SamePath(liveInfo.TopLevel, info.TopLevel) || !pathutil.SamePath(liveInfo.CommonDir, info.CommonDir) {
			preconditionErr = errSelectedWorktreeChanged
			return nil
		}

		args := []string{"worktree", "remove"}
		if r.GetForce() {
			args = append(args, "--force")
		}
		args = append(args, liveInfo.TopLevel)
		if removeErr := (gitutil.Repository{GitDir: liveInfo.CommonDir}).Run(ctx, args...); removeErr != nil {
			wt := db.Worktree{WorktreePath: liveInfo.TopLevel, RepoRoot: liveInfo.RepoRoot, RepoGitDir: liveInfo.CommonDir}
			if _, statErr := os.Stat(liveInfo.TopLevel); !errors.Is(statErr, os.ErrNotExist) || svc.gitStillListsWorktree(ctx, wt) {
				msg := removeErr.Error()
				if native := nativeGitExit(removeErr); native != nil && native.ExitCode() == 128 &&
					strings.Contains(strings.ToLower(string(native.Stderr)), "contains modified or untracked files") {
					preconditionErr = errors.New(msg)
				} else {
					internalErr = errors.New(msg)
				}
				return nil
			}
		}

		// Clean up any LeapMux tracking row only if the removed directory is still absent.
		if _, statErr := os.Stat(canonicalPath); errors.Is(statErr, os.ErrNotExist) {
			if tracked, terr := svc.Queries.GetWorktreeByPath(ctx, canonicalPath); terr == nil {
				if delErr := svc.Queries.DeleteWorktree(ctx, tracked.ID); delErr != nil {
					slog.Warn("failed to delete removed worktree tracking row", "worktree_id", tracked.ID, "error", delErr)
				}
			} else if !errors.Is(terr, sql.ErrNoRows) {
				slog.Warn("failed to look up removed worktree row", "path", canonicalPath, "error", terr)
			}
		}

		if r.GetDeleteBranch() && liveInfo.BranchName != "" {
			flag := "-d"
			if r.GetForce() {
				flag = "-D"
			}
			if brErr := (gitutil.Repository{GitDir: liveInfo.CommonDir}).Run(ctx, "branch", flag, "--", liveInfo.BranchName); brErr != nil {
				preconditionErr = fmt.Errorf("worktree removed, but deleting branch %q failed: %w", liveInfo.BranchName, brErr)
			}
		}
		return nil
	})
	if preconditionErr != nil {
		sendFailedPrecondition(sender, preconditionErr.Error())
		return
	}
	if internalErr != nil {
		sendInternalError(sender, internalErr.Error())
		return
	}

	sendProtoResponse(sender, &leapmuxv1.RemoveGitWorktreeResponse{})
}

// countLiveTabsInWorktree mirrors hasOtherLiveWorktreeTab's fail-closed
// physical prefix containment across both workingDir and linkDir.
func (svc *Service) countLiveTabsInWorktree(ctx context.Context, canonicalTop string) (int, error) {
	if svc.Queries == nil {
		return 0, nil
	}
	top, err := canonicalGitDirectory(canonicalTop)
	if err != nil {
		return 0, err
	}
	tabs, err := svc.liveWorktreeTabs(ctx)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, tab := range tabs {
		if tab.err != nil {
			return 0, tab.err
		}
		matched := false
		for _, dir := range []string{tab.workingDir, tab.linkDir} {
			if dir == "" {
				continue
			}
			canonical, candErr := worktreeDirectoryCandidate(dir, top)
			if candErr != nil {
				return 0, candErr
			}
			if canonical != "" {
				matched = true
				break
			}
		}
		if matched {
			count++
		}
	}
	return count, nil
}
