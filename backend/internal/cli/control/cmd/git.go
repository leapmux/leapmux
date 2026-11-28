package cmd

import (
	"io"
	"os"
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
	"github.com/leapmux/leapmux/locallisten"
)

// The `git` group reads and changes the git state of a directory on a
// Worker. Commands default --dir to the tab's working directory on the same
// worker, or to the local current working directory when targeting a local Hub
// or tab without $LEAPMUX_CONTROL_WORKING_DIR.
func newGitCommand() *cobra.Command {
	workingDirFlags := func(fs *pflag.FlagSet) {
		addWorkingDirFlag(fs, "a directory inside the git working tree")
		addWorkerFlag(fs)
	}
	return cli.Group("git", "Read and change the git state of a directory on a worker",
		cli.NewLeaf(cli.Leaf{
			Use:              "status",
			Short:            "Show the branch and the changed files of a working tree",
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux git status"},
				{Command: "leapmux git status --dir ~/src/app --worker name:build-box"},
			},
			Flags: workingDirFlags,
			Run:   runGitStatus,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "read <path>",
			Aliases:          []string{"cat"},
			Short:            "Print a file as the last commit or the index holds it",
			QuietUnsupported: true,
			Long: `Print a file as the last commit or the index holds it. The path is the file's
absolute path on the worker; the worker finds its repository.`,
			Examples: []cli.Example{
				{Command: "leapmux git read ~/src/app/main.go"},
				{Command: "leapmux git read ~/src/app/main.go --ref staged"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("ref", "head", "the version to read")
				cli.SetEnum(fs, "ref", "head", "staged")
				fs.Int64("limit", 0, readLimitUsage)
				addWorkerFlag(fs)
			},
			Run: runGitRead,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "checkout <branch>",
			Short:            "Check out a branch",
			Mutates:          true,
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux git checkout main"},
				{Comment: "Check out a remote branch.", Command: "leapmux git checkout origin/feature"},
			},
			Flags: workingDirFlags,
			Run: func(inv *cli.Invocation) (any, error) {
				branch, err := requireNonEmptyArg(inv, "branch")
				if err != nil {
					return nil, err
				}
				return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure, func(dir string) proto.Message {
					return &leapmuxv1.CheckoutBranchRequest{Path: dir, Branch: branch}
				}, &leapmuxv1.CheckoutBranchResponse{})
			},
		}),
		cli.Group("branch", "List, create, inspect and delete branches",
			cli.NewLeaf(cli.Leaf{
				Use:     "list",
				Aliases: []string{"ls"},
				Short:   "List the branches of a repository",
				Examples: []cli.Example{
					{Command: "leapmux git branch list"},
				},
				Flags: workingDirFlags,
				Run:   runGitBranches,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:              "create <name>",
				Short:            "Create a branch and check it out",
				Mutates:          true,
				QuietUnsupported: true,
				Examples: []cli.Example{
					{Command: "leapmux git branch create fix-login --base main"},
				},
				Flags: func(fs *pflag.FlagSet) {
					workingDirFlags(fs)
					fs.String("base", "", "the branch to start from (default: the current branch)")
				},
				FlagComplete: map[string]cobra.CompletionFunc{
					"base": completeGitBranchFlag,
				},
				Run: func(inv *cli.Invocation) (any, error) {
					name, err := requireNonEmptyArg(inv, "name")
					if err != nil {
						return nil, err
					}
					base := strings.TrimSpace(stringFlag(inv, "base"))
					if inv.Changed("base") && base == "" {
						return nil, cli.Usagef("--base is empty")
					}
					return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceCreateBranchProcedure, func(dir string) proto.Message {
						return &leapmuxv1.CreateBranchRequest{Path: dir, NewBranch: name, BaseBranch: base}
					}, &leapmuxv1.CreateBranchResponse{})
				},
			}),
			cli.NewLeaf(cli.Leaf{
				Use:              "delete <name>",
				Aliases:          []string{"rm"},
				Short:            "Check out another branch, then delete this one",
				Mutates:          true,
				Destructive:      true,
				QuietUnsupported: true,
				Examples: []cli.Example{
					{Command: "leapmux git branch delete fix-login"},
					{Comment: "Delete the current branch and switch to develop.", Command: "leapmux git branch delete fix-login --switch-to develop"},
				},
				Flags: func(fs *pflag.FlagSet) {
					workingDirFlags(fs)
					fs.String("switch-to", "", "the branch to check out before deleting the current branch (default: keep HEAD if deleting another branch, else main/master)")
				},
				FlagComplete: map[string]cobra.CompletionFunc{
					"switch-to": completeGitBranchFlag,
				},
				Run: runGitBranchDelete,
			}),
			cli.NewLeaf(cli.Leaf{
				Use:              "inspect [branch]",
				Short:            "Check whether the current branch or worktree has uncommitted or unpushed work before deletion",
				QuietUnsupported: true,
				Examples: []cli.Example{
					{Command: "leapmux git branch inspect"},
					{Command: "leapmux git branch inspect fix-login"},
				},
				Flags: workingDirFlags,
				Run:   runGitBranchInspect,
			}),
		),
		cli.NewLeaf(cli.Leaf{
			Use:              "push",
			Short:            "Push the current branch, with a work-in-progress commit when the tree has changes",
			Mutates:          true,
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux git push"},
			},
			Flags: workingDirFlags,
			Run: func(inv *cli.Invocation) (any, error) {
				return runGitWrite(inv, leapmuxv1connect.WorkerGitServicePushBranchProcedure, func(dir string) proto.Message {
					return &leapmuxv1.PushBranchRequest{WorkingDir: dir}
				}, &leapmuxv1.PushBranchResponse{})
			},
		}),
		cli.NewLeaf(cli.Leaf{
			Use:              "diff [path]...",
			Short:            "Show unstaged, staged, or base-ref git diff for a working tree",
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux git diff"},
				{Command: "leapmux git diff --staged"},
				{Command: "leapmux git diff --base main backend/cmd/leapmux"},
			},
			Flags: func(fs *pflag.FlagSet) {
				workingDirFlags(fs)
				fs.Bool("staged", false, "show staged changes (--cached)")
				fs.String("base", "", "diff against this base ref (such as main or HEAD~1)")
				fs.Int64("limit", 0, readLimitUsage)
			},
			FlagComplete: map[string]cobra.CompletionFunc{
				"base": completeGitBranchFlag,
			},
			Run: runGitDiff,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "log",
			Short: "Show recent commits of a repository",
			Examples: []cli.Example{
				{Command: "leapmux git log"},
				{Command: "leapmux git log -n 10 --ref main"},
			},
			Flags: func(fs *pflag.FlagSet) {
				workingDirFlags(fs)
				fs.IntP("limit", "n", 20, "maximum number of commits to show (max 200)")
				fs.String("ref", "", "starting ref (default: HEAD)")
			},
			Run: runGitLog,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "commit",
			Short:   "Create a git commit in a working tree",
			Mutates: true,
			Examples: []cli.Example{
				{Command: `leapmux git commit -m "Fix timeout handling"`},
				{Command: `leapmux git commit -a -m "Checkpoint changes"`},
			},
			Flags: func(fs *pflag.FlagSet) {
				workingDirFlags(fs)
				fs.StringP("message", "m", "", "commit message")
				cli.MarkRequired(fs, "message")
				fs.BoolP("all", "a", false, "stage all modified and untracked files (git add -A) before committing")
			},
			Run: runGitCommit,
		}),
	)
}

// workingDirEnv returns the spawning tab's working directory as
// injected by the worker on tab spawn. It is the default of --dir, so a
// user running from inside a remote tab doesn't have to repeat the path on
// every invocation.
func workingDirEnv() string {
	return os.Getenv(controlenv.WorkingDirVariable)
}

// isTabEnvWorker reports whether workerID refers to the tab environment's own
// worker. When $LEAPMUX_CONTROL_WORKER_ID is set, workerID must match it;
// otherwise an explicit --worker flag targets another worker.
func isTabEnvWorker(workerID string, explicitWorker bool) bool {
	envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable))
	if envWorkerID != "" {
		return workerID == envWorkerID
	}
	return !explicitWorker
}

// tabWorkingDirForWorker returns $LEAPMUX_CONTROL_WORKING_DIR when workerID
// matches the calling tab's worker, or "" when targeting another worker.
func tabWorkingDirForWorker(workerID string, explicitWorker bool) string {
	if !isTabEnvWorker(workerID, explicitWorker) {
		return ""
	}
	return strings.TrimSpace(workingDirEnv())
}

// inTabEnv reports whether the current process carries LeapMux tab environment
// variables injected when a Worker spawns a tab.
func inTabEnv() bool {
	return strings.TrimSpace(os.Getenv(controlenv.SocketVariable)) != "" ||
		strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)) != "" ||
		strings.TrimSpace(os.Getenv(controlenv.TabIDVariable)) != ""
}

// tabWorkingDir returns $LEAPMUX_CONTROL_WORKING_DIR when s targets the
// calling tab's own worker over local tab IPC (or over a local Hub without
// an explicit remote --hub). Targeting a remote Hub never reuses a local
// tab's working directory.
func (s *session) tabWorkingDir(workerID string) string {
	explicitWorker := s.inv.Changed(workerFlag)
	switch c := s.c.(type) {
	case *control.WorkerIPCClient:
		return tabWorkingDirForWorker(workerID, explicitWorker)
	case *control.HubClient:
		if !locallisten.IsLocal(c.HubURL) {
			return ""
		}
		if s.inv != nil && s.inv.Command != nil {
			if hub := explicitHub(s.inv.Command); hub != "" && !locallisten.IsLocal(hub) {
				return ""
			}
		}
		if envWorkerID := strings.TrimSpace(os.Getenv(controlenv.WorkerIDVariable)); envWorkerID != "" {
			if workerID != envWorkerID {
				return ""
			}
			return strings.TrimSpace(workingDirEnv())
		}
		if !s.canUseLocalCwdFallback(s.inv, !explicitWorker) {
			return ""
		}
		return strings.TrimSpace(workingDirEnv())
	default:
		return ""
	}
}

// checkGitDirInput validates --dir before opening a session. An explicit blank
// --dir, a whitespace-only $LEAPMUX_CONTROL_WORKING_DIR, an explicit remote
// --hub without --dir, or an explicit --worker outside a tab without --dir all
// fail before dialing. Otherwise, an omitted --dir resolves after opening the
// session.
func checkGitDirInput(inv *cli.Invocation) error {
	if inv.Changed("dir") {
		if strings.TrimSpace(stringFlag(inv, "dir")) == "" {
			return cli.Usagef("--dir is empty")
		}
		return nil
	}
	if inv.Command != nil {
		if hub := explicitHub(inv.Command); hub != "" && !locallisten.IsLocal(hub) {
			return cli.Usagef("--dir is required (pass --dir <path> or run inside a tab)")
		}
	}
	rawEnv := workingDirEnv()
	if rawEnv != "" && strings.TrimSpace(rawEnv) == "" {
		return cli.Usagef("--dir is empty")
	}
	if inv.Changed(workerFlag) && strings.TrimSpace(stringFlag(inv, workerFlag)) != "" && !inTabEnv() {
		return cli.Usagef("--dir is required when targeting another worker")
	}
	return nil
}

// defaultWorkerDir returns the implicit working directory for workerID:
// $LEAPMUX_CONTROL_WORKING_DIR when s targets the calling tab's worker,
// else the local current working directory when canUseLocalCwdFallback holds.
func (s *session) defaultWorkerDir(workerID string) string {
	if dir := s.tabWorkingDir(workerID); dir != "" {
		return dir
	}
	if s.canUseLocalCwdFallback(s.inv, isTabEnvWorker(workerID, s.inv.Changed(workerFlag))) {
		if cwd, err := os.Getwd(); err == nil && cwd != "" {
			return cwd
		}
	}
	return ""
}

// resolveGitDir resolves the directory of a git or quake command on workerID:
//  1. Explicit --dir (rejecting blank values while preserving literal spaces).
//  2. $LEAPMUX_CONTROL_WORKING_DIR when connected over local tab IPC (or a
//     local Hub) and workerID matches the calling tab's worker.
//  3. The local current working directory when targeting the local IPC worker
//     or a local Hub without an explicit or saved remote worker.
//  4. Otherwise fails cleanly with a usage error.
func (s *session) resolveGitDir(workerID string) (string, error) {
	if s.inv.Changed("dir") {
		dir := stringFlag(s.inv, "dir")
		if strings.TrimSpace(dir) == "" {
			return "", cli.Usagef("--dir is empty")
		}
		return dir, nil
	}
	if !isTabEnvWorker(workerID, s.inv.Changed(workerFlag)) {
		return "", cli.Usagef("--dir is required when targeting another worker")
	}
	if rawEnv := workingDirEnv(); rawEnv != "" && strings.TrimSpace(rawEnv) == "" {
		return "", cli.Usagef("--dir is empty")
	}
	if dir := s.defaultWorkerDir(workerID); dir != "" {
		return dir, nil
	}
	return "", cli.Usagef("--dir is required (pass --dir <path> or run inside a tab)")
}

// gitBranchInspectResult is the result of `git branch inspect`.
type gitBranchInspectResult struct {
	IsWorktree                   bool        `json:"is_worktree"`
	WorktreePath                 string      `json:"worktree_path,omitempty"`
	WorktreeID                   string      `json:"worktree_id,omitempty"`
	BranchName                   string      `json:"branch_name,omitempty"`
	HasUncommittedChanges        bool        `json:"has_uncommitted_changes"`
	DiffAdded                    int32       `json:"diff_added,omitempty"`
	DiffDeleted                  int32       `json:"diff_deleted,omitempty"`
	DiffUntracked                int32       `json:"diff_untracked,omitempty"`
	UnpushedCommits              int32       `json:"unpushed_commits,omitempty"`
	UpstreamExists               bool        `json:"upstream_exists"`
	OriginExists                 bool        `json:"origin_exists"`
	CanPush                      bool        `json:"can_push"`
	WorktreeRemovalBlockedReason string      `json:"worktree_removal_blocked_reason,omitempty"`
	ErrorHint                    string      `json:"error_hint,omitempty"`
	SwitchBranches               []gitBranch `json:"switch_branches,omitempty"`
}

func runGitBranchInspect(inv *cli.Invocation) (any, error) {
	var resp leapmuxv1.InspectBranchDeletionResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceInspectBranchDeletionProcedure, func(dir string) proto.Message {
		return &leapmuxv1.InspectBranchDeletionRequest{
			Path:           dir,
			BranchNameHint: inv.Arg("branch"),
		}
	}, &resp); err != nil {
		return nil, err
	}
	gs := resp.GetGitState()
	branches := make([]gitBranch, 0, len(resp.GetBranches()))
	for _, b := range resp.GetBranches() {
		branches = append(branches, gitBranch{Name: b.GetName(), Remote: b.GetIsRemote()})
	}
	return gitBranchInspectResult{
		IsWorktree:                   resp.GetIsWorktree(),
		WorktreePath:                 resp.GetWorktreePath(),
		WorktreeID:                   resp.GetWorktreeId(),
		BranchName:                   resp.GetBranchName(),
		HasUncommittedChanges:        gs.GetHasUncommittedChanges(),
		DiffAdded:                    gs.GetDiffAdded(),
		DiffDeleted:                  gs.GetDiffDeleted(),
		DiffUntracked:                gs.GetDiffUntracked(),
		UnpushedCommits:              gs.GetUnpushedCommitCount(),
		UpstreamExists:               gs.GetUpstreamExists(),
		OriginExists:                 gs.GetOriginExists(),
		CanPush:                      gs.GetCanPush(),
		WorktreeRemovalBlockedReason: resp.GetWorktreeRemovalBlockedReason(),
		ErrorHint:                    resp.GetErrorHint(),
		SwitchBranches:               branches,
	}, nil
}

// gitStatusResult is the result of `git status`.
type gitStatusResult struct {
	IsRepo           bool            `json:"is_repo"`
	RepoRoot         string          `json:"repo_root,omitempty"`
	Toplevel         string          `json:"toplevel,omitempty"`
	MainWorktreeRoot string          `json:"main_worktree_root,omitempty"`
	IsWorktree       bool            `json:"is_worktree,omitempty"`
	Branch           string          `json:"branch,omitempty"`
	AttachedBranch   string          `json:"attached_branch,omitempty"`
	OriginURL        string          `json:"origin_url,omitempty"`
	Ahead            int32           `json:"ahead,omitempty"`
	Behind           int32           `json:"behind,omitempty"`
	Conflicted       bool            `json:"conflicted,omitempty"`
	Stashed          bool            `json:"stashed,omitempty"`
	ErrorHint        string          `json:"error_hint,omitempty"`
	Files            []gitFileStatus `json:"files"`
}

// gitFileStatus is one changed file.
type gitFileStatus struct {
	Path               string `json:"path" table:"PATH"`
	Staged             string `json:"staged,omitempty" table:"STAGED"`
	Unstaged           string `json:"unstaged,omitempty" table:"UNSTAGED"`
	LinesAdded         int32  `json:"lines_added,omitempty" table:"ADDED"`
	LinesDeleted       int32  `json:"lines_deleted,omitempty" table:"DELETED"`
	StagedLinesAdded   int32  `json:"staged_lines_added,omitempty"`
	StagedLinesDeleted int32  `json:"staged_lines_deleted,omitempty"`
	OldPath            string `json:"old_path,omitempty"`
}

func runGitStatus(inv *cli.Invocation) (any, error) {
	s, workerID, dir, err := openWorkerSessionWithDir(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	// `git status` issues two inner RPCs (GetGitInfo and GetGitFileStatus)
	// against the same worker. Share one Noise_NK channel and run both calls
	// concurrently so the handshake and round-trip latency are paid once.
	var info leapmuxv1.GetGitInfoResponse
	var files leapmuxv1.GetGitFileStatusResponse
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		g, gctx := errgroup.WithContext(s.ctx)
		g.Go(func() error {
			return w.Call(gctx, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: dir}, &info)
		})
		g.Go(func() error {
			return w.Call(gctx, leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, &leapmuxv1.GetGitFileStatusRequest{Path: dir}, &files)
		})
		return g.Wait()
	}); err != nil {
		return nil, err
	}
	errHint := info.GetErrorHint()
	if errHint == "" {
		errHint = files.GetErrorHint()
	}
	repoStatus := files.GetStatus()
	out := gitStatusResult{
		IsRepo:           info.GetIsGitRepo(),
		RepoRoot:         info.GetRepoRoot(),
		Toplevel:         repoStatus.GetToplevel(),
		MainWorktreeRoot: info.GetMainWorktreeRoot(),
		IsWorktree:       info.GetIsWorktree(),
		Branch:           info.GetCurrentBranch(),
		AttachedBranch:   info.GetAttachedBranch(),
		OriginURL:        info.GetOriginUrl(),
		Ahead:            repoStatus.GetAhead(),
		Behind:           repoStatus.GetBehind(),
		Conflicted:       repoStatus.GetConflicted(),
		Stashed:          repoStatus.GetStashed(),
		ErrorHint:        errHint,
		Files:            make([]gitFileStatus, 0, len(files.GetFiles())),
	}
	for _, f := range files.GetFiles() {
		out.Files = append(out.Files, gitFileStatus{
			Path:               f.GetPath(),
			Staged:             enumToken(f.GetStagedStatus()),
			Unstaged:           enumToken(f.GetUnstagedStatus()),
			LinesAdded:         f.GetLinesAdded(),
			LinesDeleted:       f.GetLinesDeleted(),
			StagedLinesAdded:   f.GetStagedLinesAdded(),
			StagedLinesDeleted: f.GetStagedLinesDeleted(),
			OldPath:            f.GetOldPath(),
		})
	}
	return out, nil
}

// openWorkerSession opens a control session, resolves the target worker, and
// runs the worker preflight check. The caller closes the returned session.
func openWorkerSession(inv *cli.Invocation) (*session, string, error) {
	s, err := openSession(inv)
	if err != nil {
		return nil, "", err
	}
	workerID, err := s.workerScope()
	if err != nil {
		s.Close()
		return nil, "", err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		s.Close()
		return nil, "", err
	}
	return s, workerID, nil
}

// callOnWorker resolves the worker of a command that runs on one worker, and makes the
// call.
func callOnWorker(inv *cli.Invocation, method string, req, resp proto.Message) error {
	_, err := callOnWorkerWithID(inv, method, req, resp)
	return err
}

func callOnWorkerWithID(inv *cli.Invocation, method string, req, resp proto.Message) (string, error) {
	s, workerID, err := openWorkerSession(inv)
	if err != nil {
		return "", err
	}
	defer s.Close()
	return workerID, callInnerRPC(s.ctx, s.c, workerID, method, req, resp)
}

// openWorkerSessionWithDir validates --dir before dialing, opens a session and
// resolves the target worker, resolves the directory for that worker, and runs
// the worker preflight check. The caller closes the returned session.
func openWorkerSessionWithDir(inv *cli.Invocation) (*session, string, string, error) {
	if err := checkGitDirInput(inv); err != nil {
		return nil, "", "", err
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, "", "", err
	}
	workerID, err := s.workerScope()
	if err != nil {
		s.Close()
		return nil, "", "", err
	}
	dir, err := s.resolveGitDir(workerID)
	if err != nil {
		s.Close()
		return nil, "", "", err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		s.Close()
		return nil, "", "", err
	}
	return s, workerID, dir, nil
}

// callOnWorkerWithDir validates --dir before dialing, resolves the target
// worker and its git directory, and makes the inner RPC built from dir.
func callOnWorkerWithDir(inv *cli.Invocation, method string, build func(dir string) proto.Message, resp proto.Message) error {
	s, workerID, dir, err := openWorkerSessionWithDir(inv)
	if err != nil {
		return err
	}
	defer s.Close()
	return callInnerRPC(s.ctx, s.c, workerID, method, build(dir), resp)
}

// gitBranchList is the result of `git branch list`.
type gitBranchList struct {
	Current  string      `json:"current,omitempty"`
	Attached string      `json:"attached,omitempty"`
	Branches []gitBranch `json:"branches"`
}

func (l gitBranchList) QuietLines() []string {
	out := make([]string, 0, len(l.Branches))
	for _, b := range l.Branches {
		out = append(out, b.Name)
	}
	return out
}

type gitBranch struct {
	Name   string `json:"name" table:"NAME"`
	Remote bool   `json:"remote" table:"REMOTE"`
}

func runGitBranches(inv *cli.Invocation) (any, error) {
	var resp leapmuxv1.ListGitBranchesResponse
	if err := callOnWorkerWithDir(inv, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, func(dir string) proto.Message {
		return &leapmuxv1.ListGitBranchesRequest{Path: dir}
	}, &resp); err != nil {
		return nil, err
	}
	out := gitBranchList{Current: resp.GetCurrentBranch(), Attached: resp.GetAttachedBranch(), Branches: make([]gitBranch, 0, len(resp.GetBranches()))}
	for _, b := range resp.GetBranches() {
		out.Branches = append(out.Branches, gitBranch{Name: b.GetName(), Remote: b.GetIsRemote()})
	}
	return out, nil
}

// fileContent is a file's content in a result. Its text form writes the
// content as it is.
type fileContent struct {
	Path      string `json:"path"`
	Ref       string `json:"ref,omitempty"`
	Exists    *bool  `json:"exists,omitempty"`
	TotalSize int64  `json:"total_size"`
	ModTime   string `json:"mod_time,omitempty"`
	Content   string `json:"content"`
}

func (f fileContent) RenderText(w io.Writer) error {
	_, err := io.WriteString(w, f.Content)
	return err
}

// runGitRead reads a file at HEAD or in the index. The worker resolves the
// file's repository, computes the path relative to its root, resolves
// "<ref>:<rel-path>" with "git rev-parse --verify", and reads that blob
// with "git cat-file --batch".
func runGitRead(inv *cli.Invocation) (any, error) {
	path, err := requireNonEmptyArg(inv, "path")
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
	refName := strings.ToLower(stringFlag(inv, "ref"))
	var ref leapmuxv1.GitFileRef
	switch refName {
	case "head":
		ref = leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD
	case "staged":
		ref = leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED
	default:
		return nil, cli.Usagef(`--ref must be "head" or "staged"; got %q`, refName)
	}
	var resp leapmuxv1.ReadGitFileResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceReadGitFileProcedure, &leapmuxv1.ReadGitFileRequest{Path: path, Ref: ref, Limit: limit}, &resp); err != nil {
		return nil, err
	}
	exists := resp.GetExists()
	return fileContent{Path: path, Ref: refName, Exists: &exists, TotalSize: resp.GetTotalSize(), Content: string(resp.GetContent())}, nil
}

// gitWriteResult is the result of a git command that changes the state.
type gitWriteResult struct {
	Dir string `json:"dir"`
	OK  bool   `json:"ok"`
}

// runGitWrite makes one git write on the target worker. It resolves --dir via
// openWorkerSessionWithDir, preserving literal spaces in explicit --dir values.
func runGitWrite(inv *cli.Invocation, method string, build func(dir string) proto.Message, resp proto.Message) (any, error) {
	s, workerID, dir, err := openWorkerSessionWithDir(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if err := callInnerRPC(s.ctx, s.c, workerID, method, build(dir), resp); err != nil {
		return nil, err
	}
	return gitWriteResult{Dir: dir, OK: true}, nil
}

// runGitBranchDelete deletes <name>. When --switch-to is omitted, it passes an
// empty switch_to_branch so the worker deletes non-current branches directly or
// chooses a local fallback branch atomically under the repository index lock.
func runGitBranchDelete(inv *cli.Invocation) (any, error) {
	name, err := requireNonEmptyArg(inv, "name")
	if err != nil {
		return nil, err
	}
	var switchTo string
	if inv.Changed("switch-to") {
		switchTo = strings.TrimSpace(stringFlag(inv, "switch-to"))
		if switchTo == "" {
			return nil, cli.Usagef("--switch-to is empty")
		}
	}
	return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure, func(dir string) proto.Message {
		return &leapmuxv1.DeleteBranchRequest{
			Path:           dir,
			BranchToDelete: name,
			SwitchToBranch: switchTo,
		}
	}, &leapmuxv1.DeleteBranchResponse{})
}
