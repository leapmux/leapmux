package cmd

import (
	"fmt"
	"io"
	"strings"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

type gitDiffResult struct {
	Diff      string `json:"diff"`
	Truncated bool   `json:"truncated,omitempty"`
	TotalSize int64  `json:"total_size"`
}

func (r gitDiffResult) RenderText(w io.Writer) error {
	if r.Diff != "" {
		clean := cli.SanitizeTerminalText(r.Diff)
		if _, err := io.WriteString(w, clean); err != nil {
			return err
		}
		if !strings.HasSuffix(clean, "\n") {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
	}
	if r.Truncated {
		if _, err := fmt.Fprintf(w, "\n(diff truncated at %d of %d bytes; pass --limit to show more)\n", len(r.Diff), r.TotalSize); err != nil {
			return err
		}
	}
	return nil
}

func runGitDiff(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	limit, err := inv.Command.Flags().GetInt64("limit")
	if err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, cli.Usagef("--limit must not be negative")
	}
	var resp leapmuxv1.GetGitDiffResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceGetGitDiffProcedure, &leapmuxv1.GetGitDiffRequest{
		Path:      dir,
		Staged:    boolFlag(inv, "staged"),
		BaseRef:   stringFlag(inv, "base"),
		FilePaths: inv.Variadic("path"),
		Limit:     limit,
	}, &resp); err != nil {
		return nil, err
	}
	return gitDiffResult{
		Diff:      resp.GetDiff(),
		Truncated: resp.GetTruncated(),
		TotalSize: resp.GetTotalSize(),
	}, nil
}

type gitCommitRow struct {
	Hash        string `json:"hash"`
	ShortHash   string `json:"short_hash" table:"HASH"`
	AuthorName  string `json:"author_name" table:"AUTHOR"`
	AuthorEmail string `json:"author_email,omitempty"`
	AuthoredAt  string `json:"authored_at" table:"DATE"`
	Subject     string `json:"subject" table:"SUBJECT"`
}

type gitLogResult struct {
	Commits []gitCommitRow `json:"commits"`
}

func (r gitLogResult) QuietLines() []string {
	out := make([]string, 0, len(r.Commits))
	for _, c := range r.Commits {
		out = append(out, c.Hash)
	}
	return out
}

func runGitLog(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		return nil, cli.Usagef("--limit must be between 1 and 200")
	}
	var resp leapmuxv1.GetGitLogResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceGetGitLogProcedure, &leapmuxv1.GetGitLogRequest{
		Path:  dir,
		Limit: clampInt32(limit),
		Ref:   stringFlag(inv, "ref"),
	}, &resp); err != nil {
		return nil, err
	}
	rows := make([]gitCommitRow, 0, len(resp.GetCommits()))
	for _, c := range resp.GetCommits() {
		rows = append(rows, gitCommitRow{
			Hash:        c.GetHash(),
			ShortHash:   c.GetShortHash(),
			AuthorName:  c.GetAuthorName(),
			AuthorEmail: c.GetAuthorEmail(),
			AuthoredAt:  c.GetAuthoredAt(),
			Subject:     c.GetSubject(),
		})
	}
	return gitLogResult{Commits: rows}, nil
}

type gitCommitResult struct {
	Hash    string `json:"hash"`
	Subject string `json:"subject"`
}

func (r gitCommitResult) QuietLines() []string {
	if r.Hash == "" {
		return nil
	}
	return []string{r.Hash}
}

func runGitCommit(inv *cli.Invocation) (any, error) {
	dir := strings.TrimSpace(stringFlag(inv, "dir"))
	if dir == "" {
		return nil, cli.Usagef("--dir is empty")
	}
	msg := strings.TrimSpace(stringFlag(inv, "message"))
	if msg == "" {
		return nil, cli.Usagef("--message is empty")
	}
	var resp leapmuxv1.GitCommitResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceGitCommitProcedure, &leapmuxv1.GitCommitRequest{
		Path:     dir,
		Message:  msg,
		StageAll: boolFlag(inv, "all"),
	}, &resp); err != nil {
		return nil, err
	}
	return gitCommitResult{
		Hash:    resp.GetHash(),
		Subject: resp.GetSubject(),
	}, nil
}

type worktreeCreateResult struct {
	Path     string `json:"path"`
	Branch   string `json:"branch"`
	RepoRoot string `json:"repo_root,omitempty"`
}

func (r worktreeCreateResult) QuietLines() []string {
	if r.Path == "" {
		return nil
	}
	return []string{r.Path}
}

func runGitWorktreeCreate(inv *cli.Invocation) (any, error) {
	dir := strings.TrimSpace(stringFlag(inv, "dir"))
	if dir == "" {
		return nil, cli.Usagef("--dir is empty")
	}
	branch := strings.TrimSpace(inv.Arg("branch"))
	if branch == "" {
		return nil, cli.Usagef("branch is empty")
	}
	var resp leapmuxv1.CreateGitWorktreeResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceCreateGitWorktreeProcedure, &leapmuxv1.CreateGitWorktreeRequest{
		Path:         dir,
		Branch:       branch,
		BaseBranch:   stringFlag(inv, "base"),
		WorktreePath: stringFlag(inv, "path"),
	}, &resp); err != nil {
		return nil, err
	}
	return worktreeCreateResult{
		Path:     resp.GetWorktreePath(),
		Branch:   resp.GetBranch(),
		RepoRoot: resp.GetRepoRoot(),
	}, nil
}

type worktreeRemoveResult struct {
	Path    string `json:"path"`
	Removed bool   `json:"removed"`
}

func (r worktreeRemoveResult) QuietLines() []string {
	if r.Path == "" {
		return nil
	}
	return []string{r.Path}
}

func runGitWorktreeRemove(inv *cli.Invocation) (any, error) {
	raw := strings.TrimSpace(inv.Arg("path"))
	if raw == "" {
		return nil, cli.Usagef("path is empty")
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	path, err := resolveWorktreeSelectorPath(s, workerID, raw)
	if err != nil {
		return nil, err
	}
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
		Path:         path,
		Force:        boolFlag(inv, "force"),
		DeleteBranch: boolFlag(inv, "delete-branch"),
	}, &leapmuxv1.RemoveGitWorktreeResponse{}); err != nil {
		return nil, err
	}
	return worktreeRemoveResult{Path: path, Removed: true}, nil
}

// resolveWorktreeSelectorPath turns a bare path, "path:<path>", or
// "branch:<branch>" selector into a concrete worktree path on workerID.
func resolveWorktreeSelectorPath(s *session, workerID, raw string) (string, error) {
	selector, err := parseWorktreeSelector(raw, true)
	if err != nil {
		return "", err
	}
	if p := selector.GetPath(); p != "" {
		return p, nil
	}
	repoDir, err := s.worktreeRepoDir(workerID, selector)
	if err != nil {
		return "", err
	}
	var resp leapmuxv1.GetGitWorktreeResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, &leapmuxv1.GetGitWorktreeRequest{
		RepoDir: repoDir,
		Target:  &leapmuxv1.GetGitWorktreeRequest_Selector{Selector: selector},
	}, &resp); err != nil {
		return "", err
	}
	if wt := resp.GetWorktree().GetPath(); wt != "" {
		return wt, nil
	}
	if paths := resp.GetAmbiguousPaths(); len(paths) > 0 {
		e := cli.Newf(cli.CodeAmbiguousTarget, "%d worktrees match %q", len(paths), raw).
			WithHint("pass the exact worktree path instead")
		for _, p := range paths {
			e.Candidates = append(e.Candidates, cli.Candidate{ID: p, Kind: "worktree"})
		}
		return "", e
	}
	return "", cli.Newf(cli.CodeNotFound, "no worktree matches %q", raw)
}

type worktreeInspectResult struct {
	Path                  string `json:"path"`
	IsWorktree            bool   `json:"is_worktree"`
	WorktreePath          string `json:"worktree_path,omitempty"`
	WorktreeID            string `json:"worktree_id,omitempty"`
	BranchName            string `json:"branch_name,omitempty"`
	RemovalBlockedReason  string `json:"removal_blocked_reason,omitempty"`
	HasUncommittedChanges bool   `json:"has_uncommitted_changes"`
	DiffAdded             int32  `json:"diff_added,omitempty"`
	DiffDeleted           int32  `json:"diff_deleted,omitempty"`
	DiffUntracked         int32  `json:"diff_untracked,omitempty"`
	UnpushedCommits       int32  `json:"unpushed_commits,omitempty"`
	CanPush               bool   `json:"can_push"`
	ErrorHint             string `json:"error_hint,omitempty"`
}

func runGitWorktreeInspect(inv *cli.Invocation) (any, error) {
	path := strings.TrimSpace(inv.Arg("path"))
	if path == "" {
		var err error
		path, err = dirOf(inv)
		if err != nil {
			return nil, err
		}
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return nil, err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	if strings.HasPrefix(path, "path:") || strings.HasPrefix(path, "branch:") {
		path, err = resolveWorktreeSelectorPath(s, workerID, path)
		if err != nil {
			return nil, err
		}
	}
	var rmResp leapmuxv1.InspectWorktreeRemovalResponse
	var brResp leapmuxv1.InspectBranchDeletionResponse
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, &leapmuxv1.InspectWorktreeRemovalRequest{Path: path}, &rmResp); err != nil {
			return err
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, &leapmuxv1.InspectBranchDeletionRequest{Path: path}, &brResp)
	}); err != nil {
		return nil, err
	}
	blocked := rmResp.GetBlockedReason()
	if blocked == "" {
		blocked = brResp.GetWorktreeRemovalBlockedReason()
	}
	gs := brResp.GetGitState()
	return worktreeInspectResult{
		Path:                  path,
		IsWorktree:            brResp.GetIsWorktree(),
		WorktreePath:          brResp.GetWorktreePath(),
		WorktreeID:            brResp.GetWorktreeId(),
		BranchName:            brResp.GetBranchName(),
		RemovalBlockedReason:  blocked,
		HasUncommittedChanges: gs.GetHasUncommittedChanges(),
		DiffAdded:             gs.GetDiffAdded(),
		DiffDeleted:           gs.GetDiffDeleted(),
		DiffUntracked:         gs.GetDiffUntracked(),
		UnpushedCommits:       gs.GetUnpushedCommitCount(),
		CanPush:               gs.GetCanPush(),
		ErrorHint:             brResp.GetErrorHint(),
	}, nil
}
