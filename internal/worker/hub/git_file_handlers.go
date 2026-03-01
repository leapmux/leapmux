package hub

import (
	"os"
	"path/filepath"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

func (c *Client) handleGitFileStatus(requestID string, req *leapmuxv1.GitFileStatusRequest) {
	resp := &leapmuxv1.GitFileStatusResponse{}

	info, err := gitutil.GetGitInfo(req.GetPath())
	if err != nil {
		resp.Error = err.Error()
		c.sendGitFileStatusResponse(requestID, resp)
		return
	}

	if !info.IsGitRepo {
		resp.Error = "not a git repository"
		c.sendGitFileStatusResponse(requestID, resp)
		return
	}

	workDir := req.GetPath()
	// Compute repo root without symlink resolution so it matches the paths the
	// frontend file browser uses (e.g. /var/... vs /private/var/... on macOS).
	resp.RepoRoot = findRepoRootUnresolved(workDir, info.RepoRoot)

	files, err := gitutil.GetPerFileStatus(workDir)
	if err != nil {
		resp.Error = err.Error()
		c.sendGitFileStatusResponse(requestID, resp)
		return
	}

	for _, f := range files {
		entry := &leapmuxv1.GitFileStatusEntry{
			Path:               f.Path,
			StagedStatus:       toGitFileStatusCode(f.StagedStatus),
			UnstagedStatus:     toGitFileStatusCode(f.UnstagedStatus),
			LinesAdded:         int32(f.LinesAdded),
			LinesDeleted:       int32(f.LinesDeleted),
			StagedLinesAdded:   int32(f.StagedLinesAdded),
			StagedLinesDeleted: int32(f.StagedLinesDeleted),
			OldPath:            f.OldPath,
		}
		resp.Files = append(resp.Files, entry)
	}

	c.sendGitFileStatusResponse(requestID, resp)
}

func (c *Client) sendGitFileStatusResponse(requestID string, resp *leapmuxv1.GitFileStatusResponse) {
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitFileStatusResp{
			GitFileStatusResp: resp,
		},
	})
}

func (c *Client) handleGitFileRead(requestID string, req *leapmuxv1.GitFileReadRequest) {
	resp := &leapmuxv1.GitFileReadResponse{Path: req.GetPath()}

	absPath := req.GetPath()
	info, err := gitutil.GetGitInfo(filepath.Dir(absPath))
	if err != nil {
		resp.Error = err.Error()
		c.sendGitFileReadResponse(requestID, resp)
		return
	}

	if !info.IsGitRepo {
		resp.Error = "not a git repository"
		c.sendGitFileReadResponse(requestID, resp)
		return
	}

	// Compute the relative path from the repo root.
	// Use unresolved root to match the frontend's path conventions,
	// but fall back to the resolved root for the Rel computation.
	unresolvedRoot := findRepoRootUnresolved(filepath.Dir(absPath), info.RepoRoot)
	relPath, err := filepath.Rel(unresolvedRoot, absPath)
	if err != nil {
		// Retry with resolved root (symlink differences).
		relPath, err = filepath.Rel(info.RepoRoot, absPath)
		if err != nil {
			resp.Error = err.Error()
			c.sendGitFileReadResponse(requestID, resp)
			return
		}
	}

	var refStr string
	switch req.GetRef() {
	case leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD:
		refStr = "HEAD"
	case leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED:
		refStr = "STAGED"
	default:
		refStr = "HEAD"
	}

	// ReadFileAtRef needs the actual git repo root (resolved) to run git commands.
	content, exists, err := gitutil.ReadFileAtRef(info.RepoRoot, relPath, refStr)
	if err != nil {
		resp.Error = err.Error()
		c.sendGitFileReadResponse(requestID, resp)
		return
	}

	resp.Content = content
	resp.Exists = exists
	c.sendGitFileReadResponse(requestID, resp)
}

func (c *Client) sendGitFileReadResponse(requestID string, resp *leapmuxv1.GitFileReadResponse) {
	_ = c.Send(&leapmuxv1.ConnectRequest{
		RequestId: requestID,
		Payload: &leapmuxv1.ConnectRequest_GitFileReadResp{
			GitFileReadResp: resp,
		},
	})
}

// findRepoRootUnresolved walks up from dir (without resolving symlinks) to find
// the directory containing .git. This ensures the returned root uses the same
// path the caller provided, avoiding symlink mismatches (e.g. /var vs /private/var).
// Falls back to resolvedRoot if the walk fails.
func findRepoRootUnresolved(dir, resolvedRoot string) string {
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return resolvedRoot
	}
	d := absDir
	for {
		dotGit := filepath.Join(d, ".git")
		if fi, err := os.Lstat(dotGit); err == nil && (fi.IsDir() || fi.Mode().IsRegular()) {
			return d
		}
		parent := filepath.Dir(d)
		if parent == d {
			return resolvedRoot
		}
		d = parent
	}
}

func toGitFileStatusCode(b byte) leapmuxv1.GitFileStatusCode {
	switch b {
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
