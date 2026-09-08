package cmd

import (
	"context"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
)

// resolveWorker is shared by file / git handlers: bind the universal
// entity flag set and run the resolver to derive the worker id from
// whichever subset of --tab-id / --worker-id / --workspace-id /
// --tile-id the caller supplied. Returns the resolved worker plus the
// client so the call sites can dispatch the worker-bound inner RPC.
//
// Scripts can pass --tab-id <agent> on `file list` to scan the
// directory of the worker hosting that agent — no manual worker
// lookup required.
func resolveWorker(hub string, in resolve.Inputs) (*control.Client, string, error) {
	c, err := requireClient(hub)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := rpcDeadline(context.Background())
	defer cancel()
	got, err := runResolve(ctx, c, resolve.Need{WorkerID: true}, in)
	if err != nil {
		return nil, "", err
	}
	return c, got.WorkerID, nil
}

func RunFileList(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, false, "path to list (required)")
	var maxDepth int
	var dirsOnly bool
	var fromRoot string
	f.FS.IntVar(&maxDepth, "max-depth", 0, "merge single-child directories up to depth")
	f.FS.BoolVar(&dirsOnly, "dirs-only", false, "directories only")
	f.FS.StringVar(&fromRoot, "from-root", "", "also list every directory from this ancestor down to --path")
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if err := f.Require(""); err != nil {
		return err
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ListDirectoryResponse
	return workerUnaryEmitOn(c, workerID, "ListDirectory",
		&leapmuxv1.ListDirectoryRequest{
			WorkerId: workerID,
			Path:     f.Path,
			MaxDepth: int32(maxDepth),
			DirsOnly: dirsOnly,
			FromRoot: fromRoot,
		}, &resp,
		// A list, even without --from-root, where it holds one entry. One
		// output shape means a script does not branch on which flags it
		// passed.
		func() any { return map[string]any{"listings": resp.GetListings()} })
}

// RunFileRoots prints the worker's filesystem roots: ["/"] on POSIX, the drive
// roots on Windows.
//
// It cannot use bindPathCmd, which requires --path. This command asks about
// the machine, not about a path.
func RunFileRoots(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	var hub string
	var in resolve.Inputs
	fs := flagSet(cmd, &hub)
	resolve.BindEntityFlags(fs, &in, resolve.FlagOptions{})
	if err := parseFlags(fs, args, cmd.Description()); err != nil {
		return err
	}
	c, workerID, err := resolveWorker(hub, in)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ListFilesystemRootsResponse
	return workerUnaryEmitOn(c, workerID, "ListFilesystemRoots",
		&leapmuxv1.ListFilesystemRootsRequest{}, &resp,
		func() any { return map[string]any{"roots": resp.GetRoots()} })
}

func RunFileRead(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, false, "path to read (required)")
	var offset, limit int64
	f.FS.Int64Var(&offset, "offset", 0, "byte offset")
	f.FS.Int64Var(&limit, "limit", 0, "max bytes (0 = default 64KB)")
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if err := f.Require(""); err != nil {
		return err
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.ReadFileResponse
	return workerUnaryEmitOn(c, workerID, "ReadFile",
		&leapmuxv1.ReadFileRequest{WorkerId: workerID, Path: f.Path, Offset: offset, Limit: limit}, &resp,
		func() any {
			return map[string]any{
				"path":       resp.GetPath(),
				"total_size": resp.GetTotalSize(),
				"content":    string(resp.GetContent()),
			}
		})
}

func RunFileStat(rawCtx any, args []string) error {
	cmd := asCtx(rawCtx)
	f := bindPathCmd(cmd, false, "path to stat (required)")
	if err := parseFlags(f.FS, args, cmd.Description()); err != nil {
		return err
	}
	if err := f.Require(""); err != nil {
		return err
	}
	c, workerID, err := resolveWorker(f.Hub, f.In)
	if err != nil {
		return err
	}
	var resp leapmuxv1.StatFileResponse
	return workerUnaryEmitOn(c, workerID, "StatFile",
		&leapmuxv1.StatFileRequest{WorkerId: workerID, Path: f.Path}, &resp,
		func() any { return resp.GetInfo() })
}
