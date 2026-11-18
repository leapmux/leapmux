package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

// readLimitUsage is the help text of the --limit flag of `file read` and
// `git read`. scripts/generate-contracts.mjs keeps the default a whole number
// of KiB, so the division drops nothing.
var readLimitUsage = fmt.Sprintf("the most bytes to read (0 reads the default of %d KiB)", contracts.FileReadLimitBytes/1024)

func newFileCommand() *cobra.Command {
	return cli.Group("file", "Read the files of a worker, and open them in tabs",
		cli.NewLeaf(cli.Leaf{
			Use:   "list <path>",
			Short: "List a directory",
			Examples: []cli.Example{
				{Command: "leapmux control file list ~/src/app"},
				{Comment: "List only the directories, and every directory from the home directory down.", Command: "leapmux control file list ~/src/app --dirs-only --from-root ~"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Int("max-depth", 0, "merge a chain of directories that each hold one directory, up to this depth")
				fs.Bool("dirs-only", false, "list only directories")
				fs.String("from-root", "", "also list every directory from this ancestor down to <path>")
				addWorkerFlag(fs)
			},
			Run: runFileList,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "read <path>",
			Short: "Print a file",
			Examples: []cli.Example{
				{Command: "leapmux control file read ~/src/app/go.mod"},
				{Comment: "Read 4 KiB from byte 1024.", Command: "leapmux control file read ~/src/app/log.txt --offset 1024 --limit 4096"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.Int64("offset", 0, "the byte to start at")
				fs.Int64("limit", 0, readLimitUsage)
				addWorkerFlag(fs)
			},
			Run: runFileRead,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "stat <path>",
			Short: "Show a file's size, time and permissions",
			Examples: []cli.Example{
				{Command: "leapmux control file stat ~/src/app/go.mod"},
			},
			Flags: addWorkerFlag,
			Run:   runFileStat,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "roots",
			Short: "List the filesystem roots of a worker",
			Examples: []cli.Example{
				{Command: "leapmux control file roots"},
			},
			Flags: addWorkerFlag,
			Run:   runFileRoots,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "open <path>",
			Mutates: true,
			Short:   "Open a file in a new tab",
			Examples: []cli.Example{
				{Command: "leapmux control file open ~/src/app/README.md"},
				{Comment: "Open a file as its staged diff.", Command: "leapmux control file open ~/src/app/main.go --view-mode unified_diff"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("display-mode", "", "how a document shows at first")
				cli.SetEnum(fs, "display-mode", enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED)...)
				fs.String("view-mode", "", "which version of the file shows at first")
				cli.SetEnum(fs, "view-mode", enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED)...)
				addWorkingDirFlag(fs, "the git context of the tab; outside a LeapMux tab, the directory of the file")
				addPlacementFlags(fs)
			},
			Run: runFileOpen,
		}),
	)
}

// fileInfo is one file or directory in a result.
type fileInfo struct {
	Name        string `json:"name" table:"NAME"`
	Path        string `json:"path"`
	IsDir       bool   `json:"is_dir" table:"DIR"`
	Size        int64  `json:"size" table:"SIZE"`
	ModTime     string `json:"mod_time,omitempty" table:"MODIFIED"`
	Permissions string `json:"permissions,omitempty" table:"MODE"`
	Hidden      bool   `json:"hidden,omitempty"`
}

func fileInfoOf(f *leapmuxv1.FileInfo) fileInfo {
	return fileInfo{
		Name:        f.GetName(),
		Path:        f.GetPath(),
		IsDir:       f.GetIsDir(),
		Size:        f.GetSize(),
		ModTime:     f.GetModTime(),
		Permissions: f.GetPermissions(),
		Hidden:      f.GetHidden(),
	}
}

// directoryListing is one listed directory. A list result holds one
// listing per directory, even without --from-root, so a script does not
// branch on which flags it passed.
type directoryListing struct {
	Path string `json:"path"`
	// Truncated and TotalEntries print even when false and zero, so a
	// script that reads them gets a value, never null.
	Truncated    bool       `json:"truncated"`
	TotalEntries int32      `json:"total_entries"`
	Entries      []fileInfo `json:"entries"`
}

// fileListResult is the result of `file list`.
type fileListResult struct {
	Listings []directoryListing `json:"listings"`
}

func runFileList(inv *cli.Invocation) (any, error) {
	maxDepth, err := inv.Command.Flags().GetInt("max-depth")
	if err != nil {
		return nil, err
	}
	var resp leapmuxv1.ListDirectoryResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, &leapmuxv1.ListDirectoryRequest{
		Path:     inv.Arg("path"),
		MaxDepth: clampInt32(maxDepth),
		DirsOnly: boolFlag(inv, "dirs-only"),
		FromRoot: stringFlag(inv, "from-root"),
	}, &resp); err != nil {
		return nil, err
	}
	out := fileListResult{Listings: make([]directoryListing, 0, len(resp.GetListings()))}
	for _, l := range resp.GetListings() {
		entries := make([]fileInfo, 0, len(l.GetEntries()))
		for _, e := range l.GetEntries() {
			entries = append(entries, fileInfoOf(e))
		}
		out.Listings = append(out.Listings, directoryListing{Path: l.GetPath(), Truncated: l.GetTruncated(), TotalEntries: l.GetTotalEntries(), Entries: entries})
	}
	return out, nil
}

// rootList is the result of `file roots`: ["/"] on POSIX, the drive roots
// on Windows.
type rootList struct {
	Roots []string `json:"roots"`
}

func runFileRoots(inv *cli.Invocation) (any, error) {
	var resp leapmuxv1.ListFilesystemRootsResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceListFilesystemRootsProcedure, &leapmuxv1.ListFilesystemRootsRequest{}, &resp); err != nil {
		return nil, err
	}
	return rootList{Roots: resp.GetRoots()}, nil
}

func runFileRead(inv *cli.Invocation) (any, error) {
	offset, err := inv.Command.Flags().GetInt64("offset")
	if err != nil {
		return nil, err
	}
	limit, err := inv.Command.Flags().GetInt64("limit")
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, cli.Usagef("--offset must not be negative")
	}
	var resp leapmuxv1.ReadFileResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceReadFileProcedure, &leapmuxv1.ReadFileRequest{Path: inv.Arg("path"), Offset: offset, Limit: limit}, &resp); err != nil {
		return nil, err
	}
	return fileContent{Path: resp.GetPath(), TotalSize: resp.GetTotalSize(), ModTime: resp.GetModTime(), Content: string(resp.GetContent())}, nil
}

func runFileStat(inv *cli.Invocation) (any, error) {
	var resp leapmuxv1.StatFileResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceStatFileProcedure, &leapmuxv1.StatFileRequest{Path: inv.Arg("path")}, &resp); err != nil {
		return nil, err
	}
	return fileInfoOf(resp.GetInfo()), nil
}
