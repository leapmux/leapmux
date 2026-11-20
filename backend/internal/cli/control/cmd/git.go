package cmd

import (
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"google.golang.org/protobuf/proto"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
)

// The `git` group reads and changes the git state of a directory on a
// Worker. A read defaults --dir to the directory of the tab that runs the
// command. A command that changes the state requires --dir, so that it
// never acts on the caller's own directory by default.
func newGitCommand() *cobra.Command {
	readFlags := func(fs *pflag.FlagSet) {
		addWorkingDirFlag(fs, "a directory inside the git working tree")
		addWorkerFlag(fs)
	}
	writeFlags := func(fs *pflag.FlagSet) {
		fs.String("dir", "", "a directory inside the git working tree")
		cli.MarkRequired(fs, "dir")
		addWorkerFlag(fs)
	}
	return cli.Group("git", "Read and change the git state of a directory on a worker",
		cli.NewLeaf(cli.Leaf{
			Use:   "status",
			Short: "Show the branch and the changed files of a working tree",
			Examples: []cli.Example{
				{Command: "leapmux control git status"},
				{Command: "leapmux control git status --dir ~/src/app --worker name:build-box"},
			},
			Flags: readFlags,
			Run:   runGitStatus,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "branches",
			Short: "List the branches of a repository",
			Examples: []cli.Example{
				{Command: "leapmux control git branches"},
			},
			Flags: readFlags,
			Run:   runGitBranches,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "worktrees",
			Short: "List the worktrees of a repository",
			Examples: []cli.Example{
				{Command: "leapmux control git worktrees"},
			},
			Flags: readFlags,
			Run:   runGitWorktrees,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "read <path>",
			Short: "Print a file as the last commit or the index holds it",
			Long: `Print a file as the last commit or the index holds it. The path is the file's
absolute path on the worker; the worker finds its repository.`,
			Examples: []cli.Example{
				{Command: "leapmux control git read ~/src/app/main.go"},
				{Command: "leapmux control git read ~/src/app/main.go --ref staged"},
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
			Use:     "checkout <branch>",
			Short:   "Check out a branch",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control git checkout main --dir ~/src/app"},
				{Comment: "Check out a remote branch.", Command: "leapmux control git checkout origin/feature --dir ~/src/app"},
			},
			Flags: writeFlags,
			Run: func(inv *cli.Invocation) (any, error) {
				return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceCheckoutBranchProcedure,
					&leapmuxv1.CheckoutBranchRequest{Path: stringFlag(inv, "dir"), Branch: inv.Arg("branch")}, &leapmuxv1.CheckoutBranchResponse{})
			},
		}),
		cli.Group("branch", "Create and delete branches",
			cli.NewLeaf(cli.Leaf{
				Use:     "create <name>",
				Short:   "Create a branch and check it out",
				Mutates: true,
				Examples: []cli.Example{
					{Command: "leapmux control git branch create fix-login --base main --dir ~/src/app"},
				},
				Flags: func(fs *pflag.FlagSet) {
					writeFlags(fs)
					fs.String("base", "", "the branch to start from (default: the current branch)")
				},
				Run: func(inv *cli.Invocation) (any, error) {
					return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceCreateBranchProcedure,
						&leapmuxv1.CreateBranchRequest{Path: stringFlag(inv, "dir"), NewBranch: inv.Arg("name"), BaseBranch: stringFlag(inv, "base")}, &leapmuxv1.CreateBranchResponse{})
				},
			}),
			cli.NewLeaf(cli.Leaf{
				Use:     "delete <name>",
				Short:   "Check out another branch, then delete this one",
				Mutates: true,
				Examples: []cli.Example{
					{Command: "leapmux control git branch delete fix-login --switch-to main --dir ~/src/app"},
				},
				Flags: func(fs *pflag.FlagSet) {
					writeFlags(fs)
					fs.String("switch-to", "", "the branch to check out before the deletion")
					cli.MarkRequired(fs, "switch-to")
				},
				Run: func(inv *cli.Invocation) (any, error) {
					return runGitWrite(inv, leapmuxv1connect.WorkerGitServiceDeleteBranchProcedure,
						&leapmuxv1.DeleteBranchRequest{Path: stringFlag(inv, "dir"), BranchToDelete: inv.Arg("name"), SwitchToBranch: stringFlag(inv, "switch-to")}, &leapmuxv1.DeleteBranchResponse{})
				},
			}),
		),
		cli.NewLeaf(cli.Leaf{
			Use:     "push",
			Short:   "Push the current branch, with a work-in-progress commit when the tree has changes",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux control git push --dir ~/src/app"},
			},
			Flags: writeFlags,
			Run: func(inv *cli.Invocation) (any, error) {
				return runGitWrite(inv, leapmuxv1connect.WorkerGitServicePushBranchProcedure,
					&leapmuxv1.PushBranchRequest{WorkingDir: stringFlag(inv, "dir")}, &leapmuxv1.PushBranchResponse{})
			},
		}),
		newGitWorktreeCommand(),
	)
}

// workingDirEnv returns the spawning tab's working directory as
// injected by the worker on tab spawn. It is the default of --dir, so a
// user running from inside a remote tab doesn't have to repeat the path on
// every invocation.
func workingDirEnv() string {
	return os.Getenv(controlenv.WorkingDirVariable)
}

// missingDirError returns the error for an empty directory flag.
//
// It tells "no flag and no env var" (give the user both options) apart from
// "an explicit empty flag". A single message for both told a user who typed
// `--dir=` inside a spawn that $LEAPMUX_CONTROL_WORKING_DIR was not set,
// which is false there and sends them to look at the wrong thing.
func missingDirError(flagName string) error {
	if workingDirEnv() == "" {
		return cli.Usagef("%s is required, because $%s is not set", flagName, controlenv.WorkingDirVariable)
	}
	return cli.Usagef("%s is empty", flagName)
}

// dirOf returns the directory of a git read: --dir, else the directory of
// the tab that runs the command.
func dirOf(inv *cli.Invocation) (string, error) {
	dir := workingDir(inv)
	if dir == "" {
		return "", missingDirError("--dir")
	}
	return dir, nil
}

// gitStatusResult is the result of `git status`.
type gitStatusResult struct {
	IsRepo         bool            `json:"is_repo"`
	RepoRoot       string          `json:"repo_root,omitempty"`
	IsWorktree     bool            `json:"is_worktree,omitempty"`
	Branch         string          `json:"branch,omitempty"`
	AttachedBranch string          `json:"attached_branch,omitempty"`
	OriginURL      string          `json:"origin_url,omitempty"`
	ErrorHint      string          `json:"error_hint,omitempty"`
	Files          []gitFileStatus `json:"files"`
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
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
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
	// `git status` issues TWO inner-RPCs (GetGitInfo + GetGitFileStatus)
	// against the same worker. Share one Noise_NK channel across both
	// so the handshake cost is paid once.
	var info leapmuxv1.GetGitInfoResponse
	var files leapmuxv1.GetGitFileStatusResponse
	if err := withWorkerChannel(s.ctx, s.c, workerID, func(w workerCall) error {
		if err := w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceGetGitInfoProcedure, &leapmuxv1.GetGitInfoRequest{Path: dir}, &info); err != nil {
			return err
		}
		return w.Call(s.ctx, leapmuxv1connect.WorkerGitServiceGetGitFileStatusProcedure, &leapmuxv1.GetGitFileStatusRequest{Path: dir}, &files)
	}); err != nil {
		return nil, err
	}
	out := gitStatusResult{
		IsRepo:         info.GetIsGitRepo(),
		RepoRoot:       info.GetRepoRoot(),
		IsWorktree:     info.GetIsWorktree(),
		Branch:         info.GetCurrentBranch(),
		AttachedBranch: info.GetAttachedBranch(),
		OriginURL:      info.GetOriginUrl(),
		ErrorHint:      info.GetErrorHint(),
		Files:          make([]gitFileStatus, 0, len(files.GetFiles())),
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

// callOnWorker resolves the worker of a command that runs on one worker, and makes the
// call.
func callOnWorker(inv *cli.Invocation, method string, req, resp proto.Message) error {
	s, err := openSession(inv)
	if err != nil {
		return err
	}
	defer s.Close()
	workerID, err := s.workerScope()
	if err != nil {
		return err
	}
	if err := maybePreflightWorker(s.ctx, s.c, workerID); err != nil {
		return err
	}
	return callInnerRPC(s.ctx, s.c, workerID, method, req, resp)
}

// gitBranchList is the result of `git branches`.
type gitBranchList struct {
	Current  string      `json:"current,omitempty"`
	Attached string      `json:"attached,omitempty"`
	Branches []gitBranch `json:"branches"`
}

type gitBranch struct {
	Name   string `json:"name" table:"NAME"`
	Remote bool   `json:"remote" table:"REMOTE"`
}

func runGitBranches(inv *cli.Invocation) (any, error) {
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListGitBranchesResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceListGitBranchesProcedure, &leapmuxv1.ListGitBranchesRequest{Path: dir}, &resp); err != nil {
		return nil, err
	}
	out := gitBranchList{Current: resp.GetCurrentBranch(), Attached: resp.GetAttachedBranch(), Branches: make([]gitBranch, 0, len(resp.GetBranches()))}
	for _, b := range resp.GetBranches() {
		out.Branches = append(out.Branches, gitBranch{Name: b.GetName(), Remote: b.GetIsRemote()})
	}
	return out, nil
}

// gitWorktreeList is the result of `git worktrees`.
type gitWorktreeList struct {
	Worktrees []GitWorktreeRow `json:"worktrees"`
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
	dir, err := dirOf(inv)
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListGitWorktreesResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerGitServiceListGitWorktreesProcedure, &leapmuxv1.ListGitWorktreesRequest{Path: dir}, &resp); err != nil {
		return nil, err
	}
	out := gitWorktreeList{Worktrees: make([]GitWorktreeRow, 0, len(resp.GetWorktrees()))}
	for _, w := range resp.GetWorktrees() {
		out.Worktrees = append(out.Worktrees, gitWorktreeOf(w))
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
	path := inv.Arg("path")
	limit, err := inv.Command.Flags().GetInt64("limit")
	if err != nil {
		return nil, err
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

// runGitWrite makes one git write on the worker of --worker.
func runGitWrite(inv *cli.Invocation, method string, req, resp proto.Message) (any, error) {
	dir := strings.TrimSpace(stringFlag(inv, "dir"))
	if dir == "" {
		return nil, cli.Usagef("--dir is empty")
	}
	if err := callOnWorker(inv, method, req, resp); err != nil {
		return nil, err
	}
	return gitWriteResult{Dir: dir, OK: true}, nil
}
