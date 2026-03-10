package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
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

// getGitFileStatusEntries computes per-file git status for the given repo root.
// It runs three git commands:
//   - git status --porcelain=v2 -z (all changed files with XY status codes)
//   - git diff --numstat -z (unstaged diff stats)
//   - git diff --numstat --staged -z (staged diff stats)
//
// Returns nil for non-git directories.
func getGitFileStatusEntries(_ context.Context, repoRoot string) ([]*leapmuxv1.GitFileStatusEntry, error) {
	// 1. Get file status via porcelain v2.
	cmd := exec.Command("git", "-C", repoRoot, "status", "--porcelain=v2", "-z")
	statusOut, err := cmd.Output()
	if err != nil {
		return nil, nil // Not a git repo or git unavailable.
	}

	files := parseStatusV2(statusOut)
	if len(files) == 0 {
		return nil, nil
	}

	// Build a map for easy lookup.
	fileMap := make(map[string]*leapmuxv1.GitFileStatusEntry, len(files))
	for _, f := range files {
		fileMap[f.Path] = f
	}

	// 2. Get unstaged diff stats.
	cmd2 := exec.Command("git", "-C", repoRoot, "diff", "--numstat", "-z")
	if numstatOut, err := cmd2.Output(); err == nil {
		applyNumstat(numstatOut, fileMap, false)
	}

	// 3. Get staged diff stats.
	cmd3 := exec.Command("git", "-C", repoRoot, "diff", "--numstat", "--staged", "-z")
	if numstatOut, err := cmd3.Output(); err == nil {
		applyNumstat(numstatOut, fileMap, true)
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

// parseStatusV2 parses NUL-delimited `git status --porcelain=v2 -z` output
// into proto entries.
func parseStatusV2(data []byte) []*leapmuxv1.GitFileStatusEntry {
	var files []*leapmuxv1.GitFileStatusEntry
	parts := splitNUL(data)

	i := 0
	for i < len(parts) {
		entry := parts[i]
		if len(entry) == 0 {
			i++
			continue
		}

		switch entry[0] {
		case '1':
			// Ordinary changed entry: "1 XY sub mH mI mW hH hI path"
			if f := parseOrdinaryEntry(entry); f != nil {
				files = append(files, f)
			}
			i++

		case '2':
			// Renamed/copied entry: "2 XY sub mH mI mW hH hI Xscore path\0origPath"
			if f := parseRenameEntry(entry); f != nil {
				if i+1 < len(parts) {
					f.OldPath = parts[i+1]
					i += 2
				} else {
					i++
				}
				files = append(files, f)
			} else {
				i++
			}

		case 'u':
			// Unmerged entry: "u XY sub m1 m2 m3 hH h1 h2 h3 path"
			if f := parseUnmergedEntry(entry); f != nil {
				files = append(files, f)
			}
			i++

		case '?':
			// Untracked: "? path"
			if len(entry) > 2 {
				files = append(files, &leapmuxv1.GitFileStatusEntry{
					Path:           entry[2:],
					StagedStatus:   leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNSPECIFIED,
					UnstagedStatus: leapmuxv1.GitFileStatusCode_GIT_FILE_STATUS_CODE_UNTRACKED,
				})
			}
			i++

		default:
			i++
		}
	}

	return files
}

func parseOrdinaryEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "1 XY sub mH mI mW hH hI path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 8)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

func parseRenameEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "2 XY sub mH mI mW hH hI Xscore path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 9)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

func parseUnmergedEntry(entry string) *leapmuxv1.GitFileStatusEntry {
	// Format: "u XY sub m1 m2 m3 hH h1 h2 h3 path"
	if len(entry) < 4 {
		return nil
	}
	path := nthField(entry, 10)
	if path == "" {
		return nil
	}
	return &leapmuxv1.GitFileStatusEntry{
		Path:           path,
		StagedStatus:   parseGitStatusCode(entry[2]),
		UnstagedStatus: parseGitStatusCode(entry[3]),
	}
}

// nthField returns the content after the nth space in s (0-indexed).
func nthField(s string, n int) string {
	count := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			count++
			if count == n {
				return s[i+1:]
			}
		}
	}
	return ""
}

// applyNumstat parses NUL-delimited `git diff --numstat -z` output and applies
// line counts to the file map. When staged is true, it populates
// StagedLinesAdded/StagedLinesDeleted; otherwise LinesAdded/LinesDeleted.
func applyNumstat(data []byte, fileMap map[string]*leapmuxv1.GitFileStatusEntry, staged bool) {
	parts := splitNUL(data)
	i := 0
	for i < len(parts) {
		line := parts[i]
		if line == "" {
			i++
			continue
		}

		tabs := strings.SplitN(line, "\t", 3)
		if len(tabs) < 3 {
			i++
			continue
		}

		var addedVal, deletedVal int32
		if _, err := fmt.Sscanf(tabs[0], "%d", &addedVal); err != nil {
			addedVal = 0
		}
		if _, err := fmt.Sscanf(tabs[1], "%d", &deletedVal); err != nil {
			deletedVal = 0
		}

		filePath := tabs[2]
		if filePath == "" {
			// Rename: next two parts are oldpath and newpath.
			if i+2 < len(parts) {
				filePath = parts[i+2]
				i += 3
			} else {
				i++
				continue
			}
		} else {
			i++
		}

		if f, ok := fileMap[filePath]; ok {
			if staged {
				f.StagedLinesAdded = addedVal
				f.StagedLinesDeleted = deletedVal
			} else {
				f.LinesAdded = addedVal
				f.LinesDeleted = deletedVal
			}
		}
	}
}

// splitNUL splits data by NUL bytes, discarding a trailing empty element.
func splitNUL(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}
