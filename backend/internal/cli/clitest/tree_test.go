package clitest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/cli"
)

// recorder collects the failures that CheckTree reports.
type recorder struct {
	failures []string
}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) Helper() {}

func (r *recorder) all() string { return strings.Join(r.failures, "\n---\n") }

var rules = TreeRules{Targets: []string{"tab"}}

func noop(*cli.Invocation) (any, error) { return nil, nil }

// tree builds `tool tab <leaf>` with the leaf under test, beside a sibling
// leaf that keeps every rule.
func tree(leaf cli.Leaf) *cobra.Command {
	if leaf.Run == nil {
		leaf.Run = noop
	}
	good := cli.NewLeaf(cli.Leaf{
		Use:      "get [tab]",
		Short:    "Show a tab",
		Args:     map[string]string{"tab": "A tab id."},
		Examples: []cli.Example{{Comment: "Show this tab.", Command: "tool tab get"}},
		Run:      noop,
	})
	root := cli.Group("tool", "A tool", cli.Group("tab", "Tabs", good, cli.NewLeaf(leaf)))
	root.PersistentFlags().Bool("json", false, "print JSON")
	return root
}

func check(leaf cli.Leaf) *recorder {
	r := &recorder{}
	CheckTree(r, tree(leaf), rules)
	return r
}

func TestCheckTree_PassesALeafThatKeepsEveryRule(t *testing.T) {
	r := check(cli.Leaf{
		Use:     "close <tab>",
		Short:   "Close a tab",
		Mutates: true,
		Args:    map[string]string{"tab": "A tab id."},
		Flags: func(fs *pflag.FlagSet) {
			fs.String("worktree", "", "what happens to the worktree")
			fs.String("url", "", "URL of the hub")
		},
		Examples: []cli.Example{
			{Command: "tool tab close 87a19d5f --worktree keep"},
			{Comment: "Close the tab that a list printed.", Command: `tool tab get --json | tool tab close "87a19d5f" --json`},
			{Command: "ID=$(tool tab close 87a19d5f --json)"},
		},
	})
	assert.Empty(t, r.failures, r.all())
}

func TestCheckTree_ReportsEachBrokenRule(t *testing.T) {
	base := cli.Leaf{
		Use:      "close <tab>",
		Short:    "Close a tab",
		Args:     map[string]string{"tab": "A tab id."},
		Examples: []cli.Example{{Command: "tool tab close 87a19d5f"}},
	}
	cases := []struct {
		name  string
		edit  func(l *cli.Leaf)
		wants string
	}{
		{"no example", func(l *cli.Leaf) { l.Examples = nil }, "has no example"},
		{"an example of another command", func(l *cli.Leaf) {
			l.Examples = []cli.Example{{Command: "tool tab get"}}
		}, "does not run it with full command names"},
		{"an abbreviated example", func(l *cli.Leaf) {
			l.Examples = []cli.Example{{Command: "tool tab clo 87a19d5f"}}
		}, "does not run it with full command names"},
		{"an example with an unknown flag", func(l *cli.Leaf) {
			l.Examples = []cli.Example{{Command: "tool tab close 87a19d5f --tab-id x"}}
		}, "does not parse"},
		{"an example with too few arguments", func(l *cli.Leaf) {
			l.Examples = []cli.Example{{Command: "tool tab close"}}
		}, "has the wrong arguments"},
		{"a comment that is no sentence", func(l *cli.Leaf) {
			l.Examples = []cli.Example{{Comment: "close it", Command: "tool tab close 87a19d5f"}}
		}, "is a sentence"},
		{"a summary in lower case", func(l *cli.Leaf) { l.Short = "close a tab" }, "starts with a capital letter"},
		{"a summary with a period", func(l *cli.Leaf) { l.Short = "Close a tab." }, "ends without a period"},
		{"flag help in capitals", func(l *cli.Leaf) {
			l.Flags = func(fs *pflag.FlagSet) { fs.String("worktree", "", "What happens") }
		}, "starts with a lowercase letter"},
		{"flag help with a period", func(l *cli.Leaf) {
			l.Flags = func(fs *pflag.FlagSet) { fs.String("worktree", "", "what happens.") }
		}, "ends without a period"},
		{"a target without its forms", func(l *cli.Leaf) { l.Args = nil }, "states no forms"},
		{"an optional target on a command that changes state", func(l *cli.Leaf) {
			l.Use, l.Mutates = "close [tab]", true
			l.Examples = []cli.Example{{Command: "tool tab close"}}
		}, "must be required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			leaf := base
			tc.edit(&leaf)
			r := check(leaf)
			assert.Contains(t, r.all(), tc.wants)
		})
	}
}

func TestStartsWithCapitalizedWord(t *testing.T) {
	for text, want := range map[string]bool{
		"Path to the file": true,
		"P":                true,
		"path to the file": false,
		"URL of the hub":   false,
		"OAuth client ID":  false,
		"":                 false,
	} {
		assert.Equal(t, want, startsWithCapitalizedWord(text), "%q", text)
	}
}

func TestCommandRuns(t *testing.T) {
	assert.Equal(t, [][]string{{"tab", "close", "a b", "it's"}},
		commandRuns(`tool tab close "a b" "it's"`, "tool"))
	assert.Equal(t, [][]string{{"tab", "get", "--json"}, {"tab", "close", "-"}},
		commandRuns(`tool tab get --json | tool tab close -`, "tool"))
	assert.Equal(t, [][]string{{"tab", "close", "x"}},
		commandRuns(`T=$(tool tab close x) && echo "$T"`, "tool"))
	assert.Equal(t, [][]string{{"tab", "close", "x"}},
		commandRuns(`tool tab close x > out.json`, "tool"))
	assert.Nil(t, commandRuns("echo hello", "tool"))
}
