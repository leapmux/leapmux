package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"

	"github.com/leapmux/leapmux/internal/worker/channel"
)

// registerGitHandlers registers handlers for git operations on the local filesystem.
func registerGitHandlers(d *channel.Dispatcher, svc *Context) {
	d.Register("GetGitInfo", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.GetGitInfoRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx := bgCtx()
		resp := &leapmuxv1.GetGitInfoResponse{}

		// Check if inside a git work tree.
		isWorkTree, err := gitOutput(ctx, dirPath, "rev-parse", "--is-inside-work-tree")
		if err != nil {
			// Not a git repo.
			sendProtoResponse(sender, resp)
			return
		}
		resp.IsGitRepo = strings.TrimSpace(isWorkTree) == "true"
		if !resp.IsGitRepo {
			sendProtoResponse(sender, resp)
			return
		}

		// Get the repo root (toplevel).
		if topLevel, err := gitOutput(ctx, dirPath, "rev-parse", "--show-toplevel"); err == nil {
			topLevel = strings.TrimSpace(topLevel)
			// Resolve symlinks so paths are consistent (e.g. /var → /private/var on macOS).
			if resolved, err := filepath.EvalSymlinks(topLevel); err == nil {
				topLevel = resolved
			}
			resp.RepoRoot = topLevel
			resp.RepoDirName = filepath.Base(topLevel)

			// Resolve symlinks to compare paths accurately.
			resolvedPath, _ := filepath.EvalSymlinks(dirPath)
			resolvedTop, _ := filepath.EvalSymlinks(topLevel)
			if resolvedPath != "" && resolvedTop != "" {
				resp.IsRepoRoot = resolvedPath == resolvedTop
			}
		}

		// Detect worktree: if .git is a file (not a directory), it's a linked worktree.
		if gitDir, err := gitOutput(ctx, dirPath, "rev-parse", "--git-dir"); err == nil {
			gitDir = strings.TrimSpace(gitDir)
			// In linked worktrees, --git-dir points to <main-repo>/.git/worktrees/<name>.
			if strings.Contains(gitDir, filepath.Join(".git", "worktrees")) {
				resp.IsWorktree = true

				// Resolve the actual main repo root through the worktree link.
				if commonDir, err := gitOutput(ctx, dirPath, "rev-parse", "--git-common-dir"); err == nil {
					commonDir = strings.TrimSpace(commonDir)
					if !filepath.IsAbs(commonDir) {
						if topLevel, err := gitOutput(ctx, dirPath, "rev-parse", "--show-toplevel"); err == nil {
							commonDir = filepath.Join(strings.TrimSpace(topLevel), commonDir)
						}
					}
					// commonDir is <main-repo>/.git, so parent is the main repo root.
					mainRepoRoot := filepath.Dir(filepath.Clean(commonDir))
					// Resolve symlinks so paths are consistent.
					if resolved, err := filepath.EvalSymlinks(mainRepoRoot); err == nil {
						mainRepoRoot = resolved
					}
					resp.RepoRoot = mainRepoRoot
					resp.RepoDirName = filepath.Base(mainRepoRoot)
				}

				// Check if this path IS the worktree root.
				if topLevel, err := gitOutput(ctx, dirPath, "rev-parse", "--show-toplevel"); err == nil {
					resolvedPath, _ := filepath.EvalSymlinks(dirPath)
					resolvedTop, _ := filepath.EvalSymlinks(strings.TrimSpace(topLevel))
					if resolvedPath != "" && resolvedTop != "" {
						resp.IsWorktreeRoot = resolvedPath == resolvedTop
					}
				}
			}
		}

		// Current branch.
		if branch, err := gitOutput(ctx, dirPath, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
			resp.CurrentBranch = strings.TrimSpace(branch)
		}

		// Origin URL.
		if originURL, err := gitOutput(ctx, dirPath, "config", "--get", "remote.origin.url"); err == nil {
			resp.OriginUrl = strings.TrimSpace(originURL)
		}

		// Dirty status.
		if status, err := gitOutput(ctx, dirPath, "status", "--porcelain"); err == nil {
			resp.IsDirty = strings.TrimSpace(status) != ""
		}

		sendProtoResponse(sender, resp)
	})

	d.Register("GetGitFileStatus", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.GetGitFileStatusRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		dirPath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx := bgCtx()

		// Resolve repo root.
		repoRoot, err := gitOutput(ctx, dirPath, "rev-parse", "--show-toplevel")
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}
		repoRoot = strings.TrimSpace(repoRoot)

		files, err := getGitFileStatusEntries(ctx, repoRoot)
		if err != nil {
			slog.Error("git file status failed", "path", repoRoot, "error", err)
			sendInternalError(sender, "git status failed")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.GetGitFileStatusResponse{
			RepoRoot: repoRoot,
			Files:    files,
		})
	})

	d.Register("ReadGitFile", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ReadGitFileRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		absPath, err := sanitizePath(r.GetPath(), svc.HomeDir)
		if err != nil {
			sendPermissionDenied(sender, "access denied")
			return
		}

		ctx := bgCtx()

		// Resolve repo root from the file's directory.
		fileDir := filepath.Dir(absPath)
		repoRoot, err := gitOutput(ctx, fileDir, "rev-parse", "--show-toplevel")
		if err != nil {
			sendInternalError(sender, "not a git repository")
			return
		}
		repoRoot = strings.TrimSpace(repoRoot)

		// Compute relative path from repo root.
		relPath, err := filepath.Rel(repoRoot, absPath)
		if err != nil {
			sendInternalError(sender, "failed to compute relative path")
			return
		}

		// Determine the git ref string.
		var refStr string
		switch r.GetRef() {
		case leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD:
			refStr = "HEAD"
		case leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED:
			refStr = ":0"
		default:
			refStr = "HEAD"
		}

		// Run git show <ref>:<relative_path>.
		showArg := refStr + ":" + relPath
		content, err := gitOutputBytes(ctx, repoRoot, "show", showArg)
		if err != nil {
			// File may not exist in the given ref.
			sendProtoResponse(sender, &leapmuxv1.ReadGitFileResponse{
				Path:   absPath,
				Exists: false,
			})
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ReadGitFileResponse{
			Path:    absPath,
			Content: content,
			Exists:  true,
		})
	})

	d.Register("CheckWorktreeStatus", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.CheckWorktreeStatusRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		ctx := bgCtx()

		// Look up worktree via DB.
		wt, err := svc.Queries.GetWorktreeForTab(ctx, db.GetWorktreeForTabParams{
			TabType: r.GetTabType(),
			TabID:   r.GetTabId(),
		})
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				// No worktree associated with this tab.
				sendProtoResponse(sender, &leapmuxv1.CheckWorktreeStatusResponse{
					HasWorktree: false,
				})
				return
			}
			slog.Error("failed to get worktree for tab", "error", err)
			sendInternalError(sender, "failed to query worktree")
			return
		}

		resp := &leapmuxv1.CheckWorktreeStatusResponse{
			HasWorktree:  true,
			WorktreePath: wt.WorktreePath,
			WorktreeId:   wt.ID,
			BranchName:   wt.BranchName,
		}

		// Check if this is the last tab referencing the worktree.
		tabCount, err := svc.Queries.CountWorktreeTabs(ctx, wt.ID)
		if err != nil {
			slog.Error("failed to count worktree tabs", "error", err)
			sendInternalError(sender, "failed to count worktree tabs")
			return
		}
		resp.IsLastTab = tabCount <= 1

		// Check dirty status (uncommitted changes).
		if status, err := gitOutput(ctx, wt.WorktreePath, "status", "--porcelain"); err == nil {
			if strings.TrimSpace(status) != "" {
				resp.IsDirty = true
			}
		}

		// Check for unpushed commits.
		if !resp.IsDirty {
			// Try upstream first.
			unpushed, err := gitOutput(ctx, wt.WorktreePath, "log", "@{u}..HEAD", "--oneline")
			if err != nil {
				// No upstream configured; check if there are any commits at all.
				if commits, err2 := gitOutput(ctx, wt.WorktreePath, "log", "--oneline", "-1"); err2 == nil {
					if strings.TrimSpace(commits) != "" {
						resp.IsDirty = true
					}
				}
			} else if strings.TrimSpace(unpushed) != "" {
				resp.IsDirty = true
			}
		}

		sendProtoResponse(sender, resp)
	})

	d.Register("ForceRemoveWorktree", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.ForceRemoveWorktreeRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		ctx := bgCtx()

		// Look up worktree by ID.
		wt, err := svc.Queries.GetWorktreeByID(ctx, r.GetWorktreeId())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				sendNotFoundError(sender, "worktree not found")
				return
			}
			slog.Error("failed to get worktree", "error", err)
			sendInternalError(sender, "failed to query worktree")
			return
		}

		// Remove worktree from disk.
		if err := gitCommand(ctx, wt.RepoRoot, "worktree", "remove", "--force", wt.WorktreePath); err != nil {
			slog.Warn("failed to remove worktree from disk", "path", wt.WorktreePath, "error", err)
			// Continue anyway; the worktree may already be gone.
		}

		// Try to delete the branch.
		if wt.BranchName != "" {
			if err := gitCommand(ctx, wt.RepoRoot, "branch", "-D", wt.BranchName); err != nil {
				slog.Debug("failed to delete branch", "branch", wt.BranchName, "error", err)
				// Non-fatal: branch may have been merged or already deleted.
			}
		}

		// Delete from DB.
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Error("failed to delete worktree from DB", "id", wt.ID, "error", err)
			sendInternalError(sender, "failed to delete worktree record")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.ForceRemoveWorktreeResponse{})
	})

	d.Register("KeepWorktree", func(userID string, req *leapmuxv1.InnerRpcRequest, sender *channel.Sender) {
		var r leapmuxv1.KeepWorktreeRequest
		if err := unmarshalRequest(req, &r); err != nil {
			sendInvalidArgument(sender, "invalid request")
			return
		}

		ctx := bgCtx()

		// Look up worktree by ID.
		wt, err := svc.Queries.GetWorktreeByID(ctx, r.GetWorktreeId())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				sendNotFoundError(sender, "worktree not found")
				return
			}
			slog.Error("failed to get worktree", "error", err)
			sendInternalError(sender, "failed to query worktree")
			return
		}

		// Delete the DB record but leave the worktree on disk.
		if err := svc.Queries.DeleteWorktree(ctx, wt.ID); err != nil {
			slog.Error("failed to delete worktree from DB", "id", wt.ID, "error", err)
			sendInternalError(sender, "failed to delete worktree record")
			return
		}

		sendProtoResponse(sender, &leapmuxv1.KeepWorktreeResponse{})
	})
}

// gitRepoRoot resolves the git repository root for the given directory.
func gitRepoRoot(ctx context.Context, dir string) (string, error) {
	out, err := gitOutput(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// populateGitFileStatus fills GitRepoRoot, GitFileStatus, and GitFileStatusSet
// on the given AgentStatusChange by running git commands against workingDir.
func populateGitFileStatus(sc *leapmuxv1.AgentStatusChange, workingDir string) {
	ctx := bgCtx()
	repoRoot, err := gitRepoRoot(ctx, workingDir)
	if err != nil {
		return
	}
	entries, err := getGitFileStatusEntries(ctx, repoRoot)
	if err != nil {
		slog.Warn("failed to get git file status for broadcast",
			"working_dir", workingDir, "error", err)
		return
	}
	sc.GitRepoRoot = repoRoot
	sc.GitFileStatus = entries
	sc.GitFileStatusSet = true
}

// getGitFileStatusEntries computes per-file git status for the given repo root.
// It combines porcelain status with numstat diff data for line counts.
func getGitFileStatusEntries(ctx context.Context, repoRoot string) ([]*leapmuxv1.GitFileStatusEntry, error) {
	// Collect file status from git status --porcelain=v1.
	statusOut, err := gitOutput(ctx, repoRoot, "status", "--porcelain=v1")
	if err != nil {
		return nil, err
	}

	// Build a map of file entries.
	fileMap := make(map[string]*leapmuxv1.GitFileStatusEntry)
	for _, line := range strings.Split(statusOut, "\n") {
		if len(line) < 4 {
			continue
		}
		indexChar := line[0]
		workChar := line[1]
		filePath := line[3:]

		// Handle renames: "R  old -> new"
		var oldPath string
		if idx := strings.Index(filePath, " -> "); idx >= 0 {
			oldPath = filePath[:idx]
			filePath = filePath[idx+4:]
		}

		entry := &leapmuxv1.GitFileStatusEntry{
			Path:           filePath,
			StagedStatus:   parseGitStatusCode(indexChar),
			UnstagedStatus: parseGitStatusCode(workChar),
			OldPath:        oldPath,
		}
		fileMap[filePath] = entry
	}

	// Collect unstaged diff stats: git diff --numstat
	if diffOut, err := gitOutput(ctx, repoRoot, "diff", "--numstat"); err == nil {
		parseNumstat(diffOut, fileMap, false)
	}

	// Collect staged diff stats: git diff --cached --numstat
	if diffOut, err := gitOutput(ctx, repoRoot, "diff", "--cached", "--numstat"); err == nil {
		parseNumstat(diffOut, fileMap, true)
	}

	// Convert map to slice.
	files := make([]*leapmuxv1.GitFileStatusEntry, 0, len(fileMap))
	for _, entry := range fileMap {
		files = append(files, entry)
	}
	return files, nil
}

// gitOutput runs a git command and returns its stdout as a string.
func gitOutput(ctx context.Context, dir string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return stdout.String(), nil
}

// gitOutputBytes runs a git command and returns its stdout as raw bytes.
func gitOutputBytes(ctx context.Context, dir string, args ...string) ([]byte, error) {
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

// gitCommand runs a git command and returns an error if it fails.
func gitCommand(ctx context.Context, dir string, args ...string) error {
	fullArgs := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", fullArgs...)
	return cmd.Run()
}

// parseGitStatusCode converts a single character from git status --porcelain
// into a GitFileStatusCode enum value.
func parseGitStatusCode(c byte) leapmuxv1.GitFileStatusCode {
	switch c {
	case 'M':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_MODIFIED
	case 'A':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_ADDED
	case 'D':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_DELETED
	case 'R':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_RENAMED
	case 'C':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_COPIED
	case 'T':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_TYPE_CHANGED
	case '?':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED
	case 'U':
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNMERGED
	default:
		return leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED
	}
}

// parseNumstat parses `git diff --numstat` output and populates the line
// counts in the given file map. If staged is true, fills StagedLinesAdded
// and StagedLinesDeleted; otherwise fills LinesAdded and LinesDeleted.
func parseNumstat(output string, fileMap map[string]*leapmuxv1.GitFileStatusEntry, staged bool) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}

		// Binary files show "-" for both added and deleted.
		added, err1 := strconv.ParseInt(parts[0], 10, 32)
		deleted, err2 := strconv.ParseInt(parts[1], 10, 32)
		if err1 != nil || err2 != nil {
			continue
		}

		filePath := parts[2]
		// Handle renames: "old => new" or "{old => new}/path"
		if idx := strings.Index(filePath, " => "); idx >= 0 {
			// Use the new path.
			filePath = filePath[idx+4:]
			// Strip any trailing "}" from brace-style renames.
			filePath = strings.TrimSuffix(filePath, "}")
			filePath = strings.TrimPrefix(filePath, "{")
		}

		entry, ok := fileMap[filePath]
		if !ok {
			entry = &leapmuxv1.GitFileStatusEntry{Path: filePath}
			fileMap[filePath] = entry
		}

		if staged {
			entry.StagedLinesAdded = int32(added)
			entry.StagedLinesDeleted = int32(deleted)
		} else {
			entry.LinesAdded = int32(added)
			entry.LinesDeleted = int32(deleted)
		}
	}
}
