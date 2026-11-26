package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/leapmux/leapmux/generated/contracts"
	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
	"github.com/leapmux/leapmux/internal/cli/control"
	"github.com/leapmux/leapmux/internal/cli/control/resolve"
	"github.com/leapmux/leapmux/locallisten"
)

// readLimitUsage is the help text of the --limit flag of `file read` and
// `git read`. scripts/generate-contracts.mjs keeps the default a whole number
// of KiB, so the division drops nothing.
var readLimitUsage = fmt.Sprintf("the most bytes to read (0 reads the default of %d KiB)", contracts.FileReadLimitBytes/1024)

func newFileCommand() *cobra.Command {
	return cli.Group("file", "List, read, search, glob, and open files on a worker",
		cli.NewLeaf(cli.Leaf{
			Use:     "list [path]",
			Aliases: []string{"ls"},
			Short:   "List a directory (default: the working directory of this tab)",
			Examples: []cli.Example{
				{Command: "leapmux file list"},
				{Command: "leapmux file list ~/src/app"},
				{Comment: "List only the directories, and every directory from the home directory down.", Command: "leapmux file list ~/src/app --dirs-only --from-root ~"},
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
			Use:              "read <path>",
			Aliases:          []string{"cat"},
			Short:            "Print a file",
			QuietUnsupported: true,
			Examples: []cli.Example{
				{Command: "leapmux file read ~/src/app/go.mod"},
				{Comment: "Read 4 KiB from byte 1024.", Command: "leapmux file read ~/src/app/log.txt --offset 1024 --limit 4096"},
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
				{Command: "leapmux file stat ~/src/app/go.mod"},
			},
			Flags: addWorkerFlag,
			Run:   runFileStat,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:   "roots",
			Short: "List the filesystem roots of a worker",
			Examples: []cli.Example{
				{Command: "leapmux file roots"},
			},
			Flags: addWorkerFlag,
			Run:   runFileRoots,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "open <path>",
			Mutates: true,
			Short:   "Open a file in a new tab",
			Examples: []cli.Example{
				{Command: "leapmux file open ~/src/app/README.md"},
				{Comment: "Open a file as its staged diff.", Command: "leapmux file open ~/src/app/main.go --view-mode unified_diff"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("display-mode", "", "how a document shows at first")
				cli.SetEnum(fs, "display-mode", enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED)...)
				fs.String("view-mode", "", "which version of the file shows at first")
				cli.SetEnum(fs, "view-mode", enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED)...)
				fs.String("diff-base", "", "base git ref or revision for diff view modes")
				for _, name := range []string{"display-mode", "view-mode", "diff-base"} {
					cli.SetFlagGroup(fs, name, "Display")
				}
				addWorkingDirFlag(fs, "the git context of the tab; outside a LeapMux tab, the directory of the file")
				cli.SetFlagGroup(fs, "dir", "Git & Directory")
				addPlacementFlags(fs)
			},
			Run: runFileOpen,
		}),
		cli.NewLeaf(cli.Leaf{
			Use:     "set <tab>",
			Short:   "Change a file tab's display mode, view mode, or diff base",
			Mutates: true,
			Examples: []cli.Example{
				{Command: "leapmux file set 11aa22bb --view-mode unified_diff --diff-base HEAD"},
				{Command: "leapmux file set 11aa22bb --display-mode split"},
			},
			Flags: func(fs *pflag.FlagSet) {
				fs.String("display-mode", "", "how the document renders")
				cli.SetEnum(fs, "display-mode", enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED)...)
				fs.String("view-mode", "", "which version or diff view of the file shows")
				cli.SetEnum(fs, "view-mode", enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED)...)
				fs.String("diff-base", "", "base git ref or revision for diff view modes")
				addTargetWorkspaceFlag(fs)
			},
			Complete: completeTargets,
			Run:      runFileSet,
		}),
		newFileSearchCommand(),
		newFileGlobCommand(),
	)
}

// fileInfo is one file or directory in a result.
type fileInfo struct {
	Name        string `json:"name" table:"NAME"`
	Path        string `json:"path"`
	IsDir       bool   `json:"is_dir" table:"DIR"`
	Size        int64  `json:"size" table:"SIZE"`
	ModTime     string `json:"mod_time,omitempty" table:"MODIFIED,dim"`
	Permissions string `json:"permissions,omitempty" table:"MODE"`
	Hidden      bool   `json:"hidden,omitempty"`
}

func (f fileInfo) QuietLines() []string {
	if f.Path != "" {
		return []string{f.Path}
	}
	return []string{f.Name}
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

type unreadableDirectory struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// fileListResult is the result of `file list`.
type fileListResult struct {
	Listings   []directoryListing   `json:"listings"`
	Unreadable *unreadableDirectory `json:"unreadable,omitempty"`
}

// RenderTextWithOptions writes the file table honoring --wide, --no-header,
// --color, and terminal width.
func (r fileListResult) RenderTextWithOptions(w io.Writer, opts cli.TableOptions) error {
	for i, l := range r.Listings {
		if len(r.Listings) > 1 {
			if i > 0 {
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
			}
			if _, err := fmt.Fprintf(w, "%s:\n", cli.SanitizeSingleLine(l.Path)); err != nil {
				return err
			}
		}
		if err := cli.WriteTableWithOptions(w, l.Entries, opts); err != nil {
			return err
		}
		if l.Truncated {
			if _, err := fmt.Fprintf(w, "(showing %d of %d entries)\n", len(l.Entries), l.TotalEntries); err != nil {
				return err
			}
		}
	}
	if r.Unreadable != nil {
		if len(r.Listings) > 0 {
			if _, err := fmt.Fprintln(w); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "unreadable: %s (%s)\n", cli.SanitizeSingleLine(r.Unreadable.Path), cli.SanitizeSingleLine(r.Unreadable.Reason)); err != nil {
			return err
		}
	}
	return nil
}

// RenderText writes the file table with default options.
func (r fileListResult) RenderText(w io.Writer) error {
	return r.RenderTextWithOptions(w, cli.TableOptions{})
}

// QuietLines prints the full path of every listed entry.
func (r fileListResult) QuietLines() []string {
	var paths []string
	for _, l := range r.Listings {
		for _, e := range l.Entries {
			paths = append(paths, e.Path)
		}
	}
	return paths
}

func localCwdFallback(inv *cli.Invocation) (string, bool) {
	if s, serr := openSession(inv); serr == nil {
		isLocal := false
		switch c := s.c.(type) {
		case *control.WorkerIPCClient:
			isLocal = true
		case *control.HubClient:
			isLocal = locallisten.IsLocal(c.HubURL)
		}
		s.Close()
		if isLocal {
			if cwd, gerr := os.Getwd(); gerr == nil && cwd != "" {
				return cwd, true
			}
		}
	}
	return "", false
}

func runFileList(inv *cli.Invocation) (any, error) {
	maxDepth, err := inv.Command.Flags().GetInt("max-depth")
	if err != nil {
		return nil, err
	}
	path := inv.Arg("path")
	if path == "" {
		path = workingDirEnv()
		if path == "" {
			if cwd, ok := localCwdFallback(inv); ok {
				path = cwd
			}
		}
		if path == "" {
			return nil, cli.Usagef("missing <path>: pass a directory path, or run inside a LeapMux tab")
		}
	}
	var resp leapmuxv1.ListDirectoryResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceListDirectoryProcedure, &leapmuxv1.ListDirectoryRequest{
		Path:     path,
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
	if u := resp.GetUnreadable(); u != nil {
		out.Unreadable = &unreadableDirectory{Path: u.GetPath(), Reason: u.GetReason()}
	}
	return out, nil
}

// rootList is the result of `file roots`: ["/"] on POSIX, the drive roots
// on Windows.
type rootList struct {
	Roots []string `json:"roots"`
}

func (r rootList) QuietLines() []string { return r.Roots }

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
	if limit < 0 {
		return nil, cli.Usagef("--limit must not be negative")
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

func runFileSet(inv *cli.Invocation) (any, error) {
	if !inv.Changed("display-mode") && !inv.Changed("view-mode") && !inv.Changed("diff-base") {
		return nil, cli.Usagef("give at least one of --display-mode, --view-mode, or --diff-base")
	}
	var displayMode leapmuxv1.TabDisplayMode
	var viewMode leapmuxv1.TabFileViewMode
	if inv.Changed("display-mode") {
		raw := stringFlag(inv, "display-mode")
		n, ok := parseEnumToken(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED, raw)
		if !ok {
			return nil, cli.Usagef("--display-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabDisplayMode_TAB_DISPLAY_MODE_UNSPECIFIED), ", "), raw)
		}
		displayMode = leapmuxv1.TabDisplayMode(n)
	}
	if inv.Changed("view-mode") {
		raw := stringFlag(inv, "view-mode")
		n, ok := parseEnumToken(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED, raw)
		if !ok {
			return nil, cli.Usagef("--view-mode must be one of %s; got %q", strings.Join(enumTokens(leapmuxv1.TabFileViewMode_TAB_FILE_VIEW_MODE_UNSPECIFIED), ", "), raw)
		}
		viewMode = leapmuxv1.TabFileViewMode(n)
	}
	s, err := openSession(inv)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	got, err := s.resolveTab(resolve.KindTab, inv.Arg("tab"), resolve.Need{WorkspaceID: true, Want: resolve.Wants{TabType: true}})
	if err != nil {
		return nil, err
	}
	if got.Type != leapmuxv1.TabType_TAB_TYPE_FILE {
		return nil, cli.Usagef("tab %s is a %s tab, not a file tab", got.ID, tabTypeName(got.Type))
	}
	cc, err := s.openCRDT(got.WorkspaceID)
	if err != nil {
		return nil, err
	}
	var ops []*leapmuxv1.CrdtOp
	tabIdent := &leapmuxv1.TabIdent{TabType: leapmuxv1.TabType_TAB_TYPE_FILE, TabId: got.ID}
	if inv.Changed("display-mode") {
		op := envelope(cc.bs)
		op.Body = &leapmuxv1.CrdtOp_SetTabRegister{
			SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   tabIdent,
				Field: &leapmuxv1.SetTabRegisterOp_DisplayMode{DisplayMode: int32(displayMode)},
			},
		}
		ops = append(ops, op)
	}
	if inv.Changed("view-mode") {
		op := envelope(cc.bs)
		op.Body = &leapmuxv1.CrdtOp_SetTabRegister{
			SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   tabIdent,
				Field: &leapmuxv1.SetTabRegisterOp_FileViewMode{FileViewMode: int32(viewMode)},
			},
		}
		ops = append(ops, op)
	}
	if inv.Changed("diff-base") {
		op := envelope(cc.bs)
		op.Body = &leapmuxv1.CrdtOp_SetTabRegister{
			SetTabRegister: &leapmuxv1.SetTabRegisterOp{
				Tab:   tabIdent,
				Field: &leapmuxv1.SetTabRegisterOp_FileDiffBase{FileDiffBase: stringFlag(inv, "diff-base")},
			},
		}
		ops = append(ops, op)
	}
	if err := cc.submitOps(ops); err != nil {
		return nil, err
	}
	return struct {
		ID          string `json:"id"`
		DisplayMode string `json:"display_mode,omitempty"`
		ViewMode    string `json:"view_mode,omitempty"`
		DiffBase    string `json:"diff_base,omitempty"`
	}{
		ID:          got.ID,
		DisplayMode: stringFlag(inv, "display-mode"),
		ViewMode:    stringFlag(inv, "view-mode"),
		DiffBase:    stringFlag(inv, "diff-base"),
	}, nil
}
