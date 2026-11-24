package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	leapmuxv1connect "github.com/leapmux/leapmux/generated/proto/leapmux/v1/leapmuxv1connect"
	"github.com/leapmux/leapmux/internal/cli"
)

func newFileSearchCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "search <pattern> [path]",
		Aliases: []string{"grep"},
		Short:   "Search text files in a directory on a worker over E2EE",
		Long: `Search text files under [path] (default: the working directory of this tab)
directly on the worker over E2EE. Binary files, .git, and node_modules are
skipped automatically.`,
		Examples: []cli.Example{
			{Command: `leapmux file search "NewLeaf" /path/to/repo/backend/internal/cli`},
			{Command: `leapmux file search "TODO|FIXME" --regex --glob "**/*.go" -i`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("glob", "", `restrict matches to paths matching this glob (such as "**/*.go")`)
			fs.Bool("regex", false, "treat <pattern> as an RE2 regular expression")
			fs.BoolP("ignore-case", "i", false, "match case-insensitively")
			fs.Int("limit", 100, "maximum number of matching lines to return (max 1000)")
			addWorkerFlag(fs)
		},
		Run: runFileSearch,
	})
}

func newFileGlobCommand() *cobra.Command {
	return cli.NewLeaf(cli.Leaf{
		Use:     "glob <pattern> [path]",
		Aliases: []string{"find"},
		Short:   "List files matching a glob pattern on a worker over E2EE",
		Long: `List relative file paths under [path] (default: the working directory of this
tab) matching <pattern> (supports *, ?, [...], and ** multi-directory globs).`,
		Examples: []cli.Example{
			{Command: `leapmux file glob "**/*.go"`},
			{Command: `leapmux file glob "*.proto" /path/to/repo/proto/leapmux/v1`},
		},
		Flags: func(fs *pflag.FlagSet) {
			fs.Int("limit", 200, "maximum number of paths to return (max 2000)")
			addWorkerFlag(fs)
		},
		Run: runFileGlob,
	})
}

type fileSearchMatch struct {
	Path string `json:"path" table:"PATH"`
	Line int32  `json:"line" table:"LINE"`
	Text string `json:"text" table:"MATCH"`
}

type fileSearchResult struct {
	Path      string            `json:"path"`
	Hits      []fileSearchMatch `json:"matches"`
	Truncated bool              `json:"truncated,omitempty"`
}

func (r fileSearchResult) RenderText(w io.Writer) error {
	for _, m := range r.Hits {
		if _, err := fmt.Fprintf(w, "%s:%d:%s\n", cli.SanitizeSingleLine(m.Path), m.Line, cli.SanitizeSingleLine(m.Text)); err != nil {
			return err
		}
	}
	if r.Truncated {
		if _, err := fmt.Fprintf(w, "(truncated at %d matches; pass --limit to show more)\n", len(r.Hits)); err != nil {
			return err
		}
	}
	return nil
}

func (r fileSearchResult) QuietLines() []string {
	out := make([]string, 0, len(r.Hits))
	for _, m := range r.Hits {
		out = append(out, fmt.Sprintf("%s:%d", m.Path, m.Line))
	}
	return out
}

func runFileSearch(inv *cli.Invocation) (any, error) {
	pattern := inv.Arg("pattern")
	if pattern == "" {
		return nil, cli.Usagef("pattern is empty")
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
			return nil, cli.Usagef("missing [path]: pass a directory path, or run inside a LeapMux tab")
		}
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 1000 {
		return nil, cli.Usagef("--limit must be between 1 and 1000")
	}
	var resp leapmuxv1.SearchFilesResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceSearchFilesProcedure, &leapmuxv1.SearchFilesRequest{
		Path:       path,
		Pattern:    pattern,
		Glob:       stringFlag(inv, "glob"),
		Regex:      boolFlag(inv, "regex"),
		IgnoreCase: boolFlag(inv, "ignore-case"),
		Limit:      clampInt32(limit),
	}, &resp); err != nil {
		return nil, err
	}
	matches := make([]fileSearchMatch, 0, len(resp.GetMatches()))
	for _, m := range resp.GetMatches() {
		matches = append(matches, fileSearchMatch{
			Path: m.GetPath(),
			Line: m.GetLine(),
			Text: m.GetText(),
		})
	}
	return fileSearchResult{
		Path:      path,
		Hits:      matches,
		Truncated: resp.GetTruncated(),
	}, nil
}

type fileGlobResult struct {
	Path      string   `json:"path"`
	Paths     []string `json:"paths"`
	Truncated bool     `json:"truncated,omitempty"`
}

func (r fileGlobResult) RenderText(w io.Writer) error {
	for _, p := range r.Paths {
		if _, err := fmt.Fprintln(w, cli.SanitizeSingleLine(p)); err != nil {
			return err
		}
	}
	if r.Truncated {
		if _, err := fmt.Fprintf(w, "(truncated at %d paths; pass --limit to show more)\n", len(r.Paths)); err != nil {
			return err
		}
	}
	return nil
}

func (r fileGlobResult) QuietLines() []string {
	return r.Paths
}

func runFileGlob(inv *cli.Invocation) (any, error) {
	pattern := inv.Arg("pattern")
	if pattern == "" {
		return nil, cli.Usagef("pattern is empty")
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
			return nil, cli.Usagef("missing [path]: pass a directory path, or run inside a LeapMux tab")
		}
	}
	limit, err := inv.Command.Flags().GetInt("limit")
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 2000 {
		return nil, cli.Usagef("--limit must be between 1 and 2000")
	}
	var resp leapmuxv1.GlobFilesResponse
	if err := callOnWorker(inv, leapmuxv1connect.WorkerFileServiceGlobFilesProcedure, &leapmuxv1.GlobFilesRequest{
		Path:    path,
		Pattern: pattern,
		Limit:   clampInt32(limit),
	}, &resp); err != nil {
		return nil, err
	}
	paths := resp.GetPaths()
	if paths == nil {
		paths = []string{}
	}
	return fileGlobResult{
		Path:      path,
		Paths:     paths,
		Truncated: resp.GetTruncated(),
	}, nil
}
