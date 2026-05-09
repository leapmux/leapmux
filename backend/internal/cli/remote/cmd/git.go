package cmd

import (
	"context"
	"errors"
	"os"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/remote"
	"github.com/leapmux/leapmux/internal/cli/remote/resolve"
	"github.com/leapmux/leapmux/tunnel"
)

// workingDirEnv returns the spawning tab's working directory as
// injected by the worker on tab spawn. Used as the default value
// for --path on `git status` / `git branches` / `git worktrees`
// so a user running from inside a remote tab doesn't have to
// repeat the path on every invocation. `git read` takes a FILE
// path, not a directory, so it keeps --path required.
func workingDirEnv() string {
	return os.Getenv("LEAPMUX_REMOTE_WORKING_DIR")
}

const gitDirPathUsage = "directory inside a git working tree (defaults to $LEAPMUX_REMOTE_WORKING_DIR)"

// emitMissingPathErr returns the canonical empty-path error.
// Differentiates between "no flag and no env var" (give the user
// both options) and "explicit empty --path".
func emitMissingPathErr() error {
	if workingDirEnv() == "" {
		return remote.EmitError("invalid_request",
			"--path is required (and $LEAPMUX_REMOTE_WORKING_DIR is not set, so there's no default)")
	}
	return remote.EmitError("invalid_request", "--path must not be empty")
}

func RunGitStatus(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, true, gitDirPathUsage)
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if f.Path == "" {
		return emitMissingPathErr()
	}
	return resolveAndEmit(f.Hub, resolve.Need{WorkerID: true}, f.In, func(ctx context.Context, c *remote.Client, got resolve.Resolved) error {
		if err := maybePreflightWorker(ctx, c, got.WorkerID); err != nil {
			return err
		}
		// `git status` issues TWO inner-RPCs (GetGitInfo + GetGitFileStatus)
		// against the same worker. Share one Noise_NK channel across both
		// so the handshake cost is paid once.
		var infoResp leapmuxv1.GetGitInfoResponse
		var fileResp leapmuxv1.GetGitFileStatusResponse
		if err := withWorkerChannel(ctx, c, got.WorkerID, func(ch *tunnel.Channel) error {
			if err := callInnerRPCOnChannelMarshal(ctx, ch, c, got.WorkerID, "GetGitInfo", &leapmuxv1.GetGitInfoRequest{
				WorkerId: got.WorkerID,
				Path:     f.Path,
			}, &infoResp); err != nil {
				return err
			}
			return callInnerRPCOnChannelMarshal(ctx, ch, c, got.WorkerID, "GetGitFileStatus", &leapmuxv1.GetGitFileStatusRequest{
				WorkerId: got.WorkerID,
				Path:     f.Path,
			}, &fileResp)
		}); err != nil {
			var coded *codedRPCError
			if errors.As(err, &coded) {
				return remote.EmitErrorWith(coded.Code, coded.Cause)
			}
			return remote.EmitErrorWith("rpc_failed", err)
		}
		return remote.EmitData(map[string]any{
			"info":  &infoResp,
			"files": fileResp.GetFiles(),
		})
	})
}

func RunGitBranches(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, true, gitDirPathUsage)
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if f.Path == "" {
		return emitMissingPathErr()
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ListGitBranchesResponse
	return workerUnaryEmitOn(c, workerID, "ListGitBranches",
		&leapmuxv1.ListGitBranchesRequest{WorkerId: workerID, Path: f.Path}, &resp,
		func() any { return resp.GetBranches() })
}

func RunGitWorktrees(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, true, gitDirPathUsage)
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if f.Path == "" {
		return emitMissingPathErr()
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ListGitWorktreesResponse
	return workerUnaryEmitOn(c, workerID, "ListGitWorktrees",
		&leapmuxv1.ListGitWorktreesRequest{WorkerId: workerID, Path: f.Path}, &resp,
		func() any { return resp.GetWorktrees() })
}

func RunGitRead(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, false, `absolute file path to read; the worker resolves the file's repo, computes the path relative to the repo root, and runs "git show <ref>:<rel-path>" (required)`)
	var refStr string
	f.FS.StringVar(&refStr, "ref", "head", `which version to read: "head" (last committed) or "staged" (index)`)
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if f.Path == "" {
		// `git read` takes a FILE path (not a directory), so
		// $LEAPMUX_REMOTE_WORKING_DIR isn't a useful default —
		// keep the flag explicitly required.
		return remote.EmitError("invalid_request", "--path is required (must be a file path; the working-dir default only applies to git status/branches/worktrees)")
	}
	var ref leapmuxv1.GitFileRef
	switch refStr {
	case "", "head":
		ref = leapmuxv1.GitFileRef_GIT_FILE_REF_HEAD
	case "staged":
		ref = leapmuxv1.GitFileRef_GIT_FILE_REF_STAGED
	default:
		return remote.EmitError("invalid_request", "--ref must be head or staged")
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ReadGitFileResponse
	return workerUnaryEmitOn(c, workerID, "ReadGitFile",
		&leapmuxv1.ReadGitFileRequest{WorkerId: workerID, Path: f.Path, Ref: ref}, &resp,
		func() any {
			return map[string]any{
				"ref":     refStr,
				"path":    f.Path,
				"content": string(resp.GetContent()),
			}
		})
}
