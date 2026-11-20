package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

func newGitWorktreeCommand() *cobra.Command {
	return cli.Group("worktree", "Find an existing checked-out worktree",
		cli.NewLeaf(cli.Leaf{
			Use:   "get <selector>",
			Short: "Show a worktree by path, attached branch, or calling tab",
			Long: `Use path:<path>, branch:<branch>, or self. The worker matches the attached
branch in the repository selected by --dir. A local tab can supply the
repository directory for a branch on the same worker. An explicit path can
select its own repository. Use self to find the calling local tab's checkout.`,
			Examples: []cli.Example{
				{Command: "leapmux control git worktree get branch:feature/login --dir ~/src/app"},
				{Command: "leapmux control git worktree get self"},
			},
			Flags: func(fs *pflag.FlagSet) {
				addWorkingDirFlag(fs, "repository directory on the selected worker")
				addWorkerFlag(fs)
			},
			Run: runGitWorktreeGet,
		}))
}

func parseWorktreeSelector(raw string, allowLiteralPath bool) (*leapmuxv1.GitWorktreeSelector, error) {
	if branch, ok := strings.CutPrefix(raw, "branch:"); ok {
		if branch == "" {
			return nil, cli.Usagef("the worktree branch is empty")
		}
		return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Branch{Branch: branch}}, nil
	}
	path, explicit := strings.CutPrefix(raw, "path:")
	if !explicit && !allowLiteralPath {
		return nil, cli.Usagef("give a worktree as path:<path>, branch:<branch>, or self")
	}
	if strings.TrimSpace(path) == "" {
		return nil, cli.Usagef("the worktree path is empty")
	}
	return &leapmuxv1.GitWorktreeSelector{Selector: &leapmuxv1.GitWorktreeSelector_Path{Path: path}}, nil
}

func (s *session) worktreeRepoDir(workerID string, selector *leapmuxv1.GitWorktreeSelector) (string, error) {
	if s.inv.Changed("dir") {
		dir := stringFlag(s.inv, "dir")
		if strings.TrimSpace(dir) == "" {
			return "", cli.Usagef("--dir is empty")
		}
		return dir, nil
	}
	if selector.GetBranch() == "" {
		return "", nil
	}
	if s.c.IsWorkerIPC() && workerID == os.Getenv(controlenv.WorkerIDVariable) {
		if dir := workingDirEnv(); dir != "" {
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

func runGitWorktreeGet(inv *cli.Invocation) (any, error) {
	var request leapmuxv1.GetGitWorktreeRequest
	if raw := inv.Arg("selector"); raw == "self" {
		if inv.Changed("dir") {
			return nil, cli.Usagef("self takes its directory from the calling tab")
		}
		request.Target = &leapmuxv1.GetGitWorktreeRequest_Self{Self: true}
	} else {
		selector, err := parseWorktreeSelector(raw, false)
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
		if !s.c.IsWorkerIPC() || workerID != os.Getenv(controlenv.WorkerIDVariable) {
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
		ambiguity := cli.Newf(cli.CodeAmbiguousTarget, "worktree selector %q matches %d checkouts", inv.Arg("selector"), len(paths)).
			WithHint("give one of the path selectors")
		for _, path := range paths {
			ambiguity.Candidates = append(ambiguity.Candidates, cli.Candidate{ID: "path:" + path, Kind: "worktree"})
		}
		return nil, ambiguity
	}
	if response.GetWorktree() == nil {
		return nil, cli.Newf(cli.CodeInternal, "the worker returned no worktree result")
	}
	return gitWorktreeLookup{GitWorktreeRow: gitWorktreeOf(response.GetWorktree()), WorkerID: workerID, RepoRoot: response.GetRepoRoot()}, nil
}
