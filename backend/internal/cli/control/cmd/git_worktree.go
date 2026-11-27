package cmd

import (
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func newWorktreeCommand() *cobra.Command {
	readFlags := func(fs *pflag.FlagSet) {
		addWorkingDirFlag(fs, "a directory inside the git working tree")
		addWorkerFlag(fs)
	}
	return cli.SetAliases(cli.Group("worktree", "List, inspect, create, and remove git worktrees on a worker",
		cli.NewLeaf(cli.Leaf{
			Use:     "list",
			Aliases: []string{"ls"},
			Short:   "List the worktrees of a repository",
			Examples: []cli.Example{
				{Command: "leapmux worktree list"},
				{Command: "leapmux worktree list --dir ~/src/app"},
			},
			Flags: readFlags,
			Run:   runGitWorktrees,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "get [selector]",
			Short: "Show a worktree by path, attached branch, or calling tab (default: self)",
			Long: `Use a bare path, path:<path>, branch:<branch>, or self (the default). The
worker matches the attached branch in the repository selected by --dir. A local
tab can supply the repository directory for a branch on the same worker. An
explicit path can select its own repository. Use self to find the calling local
tab's checkout.`,
			Examples: []cli.Example{
				{Command: "leapmux worktree get"},
				{Command: "leapmux worktree get branch:feature/login --dir ~/src/app"},
				{Command: "leapmux worktree get /path/to/worktree"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addWorkingDirFlag(fs, "repository directory on the selected worker")
				addWorkerFlag(fs)
			},
			Run: runGitWorktreeGet,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "create <branch>",
			Short:   "Create a new linked git worktree on a new branch",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux worktree create feat/cli-v2 --dir ~/src/app"},
				{Command: "leapmux worktree create feat/cli-v2 --base main --path ~/wt/cli-v2 --dir ~/src/app"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("dir", "", "repository directory on the worker")
				cli.MarkRequired(fs, "dir")
				fs.String("base", "", "base branch or commit to branch from (default: current branch)")
				fs.String("path", "", "explicit filesystem path for the new worktree (default: worker managed location)")
				addWorkerFlag(fs)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"base": completeGitBranchFlag,
			},
			Run: runGitWorktreeCreate,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:         "remove <path>",
			Aliases:     []string{"rm", "delete"},
			Short:       "Remove a linked git worktree from disk",
			Mutates:     true,
			Destructive: true,
			Examples: []cli.Example{
				{Command: "leapmux worktree remove ~/wt/cli-v2"},
				{Command: "leapmux worktree remove ~/wt/cli-v2 --force"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Bool("force", false, "remove even if the worktree contains modified or untracked files")
				fs.Bool("delete-branch", false, "also delete the local branch checked out in the removed worktree")
				fs.String("dir", "", "repository directory when resolving a branch:<branch> selector")
				addWorkerFlag(fs)
			},
			Run: runGitWorktreeRemove,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "inspect [path]",
			Short:            "Check whether a worktree is locked, dirty, or has unpushed commits before removal",
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux worktree inspect"},
				{Command: "leapmux worktree inspect ~/wt/cli-v2"},
			},
			Flags: readFlags,
			Run:   runGitWorktreeInspect,
		})), "wt")
}

// gitWorktreeList is the result of `worktree list`.
type gitWorktreeList struct {
	Worktrees []GitWorktreeRow `json:"worktrees"`
}

func (l gitWorktreeList) QuietLines() []string {
	out := make([]string, 0, len(l.Worktrees))
	for _, w := range l.Worktrees {
		out = append(out, w.Path)
	}
	return out
}

// GitWorktreeRow is the display row shared by worktree listing and lookup.
type GitWorktreeRow struct {
	Path       string `json:"path" table:"PATH"`
	Branch     string `json:"branch" table:"BRANCH"`
	Main       bool   `json:"main" table:"MAIN"`
	Locked     bool   `json:"locked" table:"LOCKED"`
	LockReason string `json:"lock_reason,omitempty"`
}

func gitWorktreeOf(worktree *leapmuxv1.GitWorktreeEntry) GitWorktreeRow {
	return GitWorktreeRow{Path: worktree.GetPath(), Branch: worktree.GetBranch(), Main: worktree.GetIsMain(), Locked: worktree.GetLocked(), LockReason: worktree.GetLockReason()}
}

func runGitWorktrees(inv *cli.Invocation) (any, error) {
	var resp leapmuxv1.ListGitWorktreesResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, func(dir string) proto.Message {
		return &leapmuxv1.ListGitWorktreesRequest{Path: dir}
	}, &resp); err != nil {
		return nil, err
	}
	out := gitWorktreeList{Worktrees: make([]GitWorktreeRow, 0, len(resp.GetWorktrees()))}
	for _, w := range resp.GetWorktrees() {
		out.Worktrees = append(out.Worktrees, gitWorktreeOf(w))
	}
	return out, nil
}

func parseWorktreeSelector(raw string) (*leapmuxv1.GitWorktreeSelector, error) {
	if branch, ok := strings.CutPrefix(raw, "branch:"); ok {
		branch = strings.TrimSpace(branch)
		if branch == "" {
			return nil, cli.Usagef("the worktree branch is empty")
		}
		return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Branch{Branch: branch}}, nil
	}
	path, _ := strings.CutPrefix(raw, "path:")
	if strings.TrimSpace(path) == "" {
		return nil, cli.Usagef("the worktree path is empty")
	}
	return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Path{Path: path}}, nil
}

func ambiguousWorktreeError(raw string, paths []string) error {
	sorted := slices.Clone(paths)
	slices.Sort(sorted)
	err := cli.Newf(cli.CodeAmbiguousTarget, "worktree selector %q matches %d checkouts", raw, len(sorted)).
		WithHint("give one of the path selectors")
	for _, path := range sorted {
		err.Candidates = append(err.Candidates, cli.Candidate{ID: "path:" + path, Kind: "worktree"})
	}
	return err
}

func (s *session) worktreeRepoDir(workerID string, selector *leapmuxv1.GitWorktreeSelector) (string, error) {
	if s.inv.Changed("dir") {
		dir := strings.TrimSpace(stringFlag(s.inv, "dir"))
		if dir == "" {
			return "", cli.Usagef("--dir is empty")
		}
		return dir, nil
	}
	if selector.GetBranch() == "" {
		return "", nil
	}
	if _, isIPC := s.c.(*control.WorkerIPCClient); isIPC {
		if dir := tabWorkingDirForWorker(workerID, s.inv.Changed(workerFlag)); dir != "" {
			return dir, nil
		}
	}
	return "", cli.Usagef("branch selection requires --dir on the selected worker")
}

type gitWorktreeLookup struct {
	GitWorktreeRow
	WorkerID string `json:"worker_id" table:"WORKER"`
	RepoRoot string `json:"repo_root" table:"REPOSITORY"`
}

func (l gitWorktreeLookup) QuietLines() []string { return []string{l.Path} }

func runGitWorktreeGet(inv *cli.Invocation) (any, error) {
	var request leapmuxv1.GetGitWorktreeRequest
	raw := "self"
	if inv.HasArg("selector") {
		raw = strings.TrimSpace(inv.Arg("selector"))
		if raw == "" {
			return nil, cli.Usagef("the worktree selector is empty")
		}
	}
	if raw == "self" {
		if inv.Changed("dir") {
			return nil, cli.Usagef("self takes its directory from the calling tab")
		}
		request.Target = &leapmuxv1.GetGitWorktreeRequest_Self{Self: true}
	} else {
		selector, err := parseWorktreeSelector(raw)
		if err != nil {
			return nil, err
		}
		request.Target = &leapmuxv1.GetGitWorktreeRequest_Selector{Selector: selector}
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
	if request.GetSelf() {
		_, isIPC := s.c.(*control.WorkerIPCClient)
		if !isIPC || workerID != strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)) {
			return nil, cli.Usagef("self requires a calling tab on the selected worker")
		}
	} else {
		request.RepoDir, err = s.worktreeRepoDir(workerID, request.GetSelector())
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	var response leapmuxv1.GetGitWorktreeResponse
	if err := callInnerRPC(s.ctx, s.c, workerID, leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, &request, &response); err != nil {
		return nil, err
	}
	if paths := response.GetAmbiguousPaths(); len(paths) > 0 {
		return nil, ambiguousWorktreeError(raw, paths)
	}
	if response.GetWorktree() == nil {
		return nil, cli.Newf(cli.CodeInternal, "the worker returned no worktree result")
	}
	return gitWorktreeLookup{GitWorktreeRow: gitWorktreeOf(response.GetWorktree()), WorkerID: workerID, RepoRoot: response.GetRepoRoot()}, nil
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
	selector, err := parseWorktreeSelector(raw)
	if err != nil {
		return nil, err
	}
	s, workerID, err := openWorkerSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	repoDir, err := s.worktreeRepoDir(workerID, selector)
	if err != nil {
		return nil, err
	}
	var path string
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		var rerr error
		path, rerr = resolveWorktreeSelectorPath(s, w, selector, repoDir, raw)
		if rerr != nil {
			return rerr
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceRemoveGitWorktreeProcedure, &leapmuxv1.RemoveGitWorktreeRequest{
			Path:         path,
			Force:        boolFlag(inv, "force"),
			DeleteBranch: boolFlag(inv, "delete-branch"),
		}, &leapmuxv1.RemoveGitWorktreeResponse{})
	}); err != nil {
		return nil, err
	}
	return worktreeRemoveResult{Path: path, Removed: true}, nil
}

// resolveWorktreeSelectorPath resolves a parsed bare path, "path:<path>", or
// "branch:<branch>" selector into a concrete worktree path over the open
// workerCall w.
func resolveWorktreeSelectorPath(s *session, w workerCall, selector *leapmuxv1.GitWorktreeSelector, repoDir, raw string) (string, error) {
	if p := selector.GetPath(); p != "" {
		return p, nil
	}
	var resp leapmuxv1.GetGitWorktreeResponse
	if err := w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceGetGitWorktreeProcedure, &leapmuxv1.GetGitWorktreeRequest{
		RepoDir: repoDir,
		Target:  &leapmuxv1.GetGitWorktreeRequest_Selector{Selector: selector},
	}, &resp); err != nil {
		return "", err
	}
	if wt := resp.GetWorktree().GetPath(); wt != "" {
		return wt, nil
	}
	if paths := resp.GetAmbiguousPaths(); len(paths) > 0 {
		return "", ambiguousWorktreeError(raw, paths)
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
	var raw string
	var selector *leapmuxv1.GitWorktreeSelector
	hasPathArg := inv.HasArg("path")
	if hasPathArg {
		raw = strings.TrimSpace(inv.Arg("path"))
		if raw == "" {
			return nil, cli.Usagef("<path> is empty")
		}
		if strings.HasPrefix(raw, "path:") || strings.HasPrefix(raw, "branch:") {
			var err error
			selector, err = parseWorktreeSelector(raw)
			if err != nil {
				return nil, err
			}
		}
	} else {
		if err := checkGitDirInput(inv); err != nil {
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
	if !hasPathArg {
		raw, err = s.resolveGitDir(workerID)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(raw, "path:") || strings.HasPrefix(raw, "branch:") {
			selector, err = parseWorktreeSelector(raw)
			if err != nil {
				return nil, err
			}
		}
	}
	var repoDir string
	if selector != nil {
		repoDir, err = s.worktreeRepoDir(workerID, selector)
		if err != nil {
			return nil, err
		}
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return nil, err
	}
	path := raw
	var rmResp leapmuxv1.InspectWorktreeRemovalResponse
	var brResp leapmuxv1.InspectBranchDeletionResponse
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		if selector != nil {
			var rerr error
			path, rerr = resolveWorktreeSelectorPath(s, w, selector, repoDir, raw)
			if rerr != nil {
				return rerr
			}
		}
		g, gctx := errgroup.WithContext(s.ctx)
		g.Go(func() error {
			return w.Call(gctx, leapmuxv1connect.WorkerGitServiceInspectWorktreeRemovalProcedure, &leapmuxv1.InspectWorktreeRemovalRequest{Path: path}, &rmResp)
		})
		g.Go(func() error {
			return w.Call(gctx, leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, &leapmuxv1.InspectBranchDeletionRequest{Path: path}, &brResp)
		})
		return g.Wait()
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
