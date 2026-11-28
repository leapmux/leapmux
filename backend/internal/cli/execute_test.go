package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testInvocation records what a leaf of testTree received.
type testInvocation struct {
	path string
	args []string
}

// testTree builds a small tree that holds every shape the framework handles:
// two groups whose names share a prefix, a leaf with a required and an
// optional argument, a leaf with a variadic argument, a leaf with a required
// flag, and an alias.
func testTree(seen *[]testInvocation) *cobra.Command {
	record := func(inv *Invocation) (any, error) {
		*seen = append(*seen, testInvocation{path: inv.Command.CommandPath(), args: inv.Args})
		return map[string]any{"args": inv.Args}, nil
	}
	leaf := func(l Leaf) *cobra.Command {
		if l.Short == "" {
			l.Short = "A leaf"
		}
		if l.Run == nil {
			l.Run = record
		}
		return NewLeaf(l)
	}
	root := Group("tool", "A tool under test",
		Group("workspace", "Manage workspaces",
			leaf(Leaf{Use: "list", Aliases: []string{"ls"}}),
			leaf(Leaf{Use: "get [workspace]", Args: map[string]string{"workspace": "A workspace id."}}),
			leaf(Leaf{Use: "rename <workspace> <title>", Mutates: true}),
		),
		Group("worker", "Manage workers",
			leaf(Leaf{Use: "list"}),
			leaf(Leaf{
				Use: "key",
				Flags: func(fs *pflag.FlagSet) {
					fs.String("name", "", "the name of the key")
					MarkRequired(fs, "name")
				},
			}),
		),
		leaf(Leaf{
			Use: "send <target> [text...]",
			Flags: func(fs *pflag.FlagSet) {
				fs.IntP("count", "n", 1, "how many times")
				fs.BoolP("loud", "l", false, "send it loudly")
			},
		}),
		leaf(Leaf{
			Use: "fail",
			Run: func(*Invocation) (any, error) { return nil, &ExitStatus{Code: 5} },
		}),
	)
	root.PersistentFlags().String("hub", "", "hub URL")
	AddOutputFlags(root)
	SetFooter(root, "Read the guide first.")
	return root
}

// execute runs `tool <args...>` and returns the exit status and the two
// streams.
func execute(t *testing.T, root *cobra.Command, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), root, args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestInspectCommandLine_ExpandsTheCommandPath(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"keeps full names", []string{"workspace", "list"}, []string{"workspace", "list"}},
		{"expands a unique prefix at every level", []string{"works", "l"}, []string{"workspace", "list"}},
		{"an exact alias resolves to its command", []string{"works", "ls"}, []string{"workspace", "list"}},
		{"skips the value of a flag", []string{"--hub", "wor", "works", "l"}, []string{"--hub", "wor", "workspace", "list"}},
		{"skips a flag with its value attached", []string{"--hub=wor", "works", "l"}, []string{"--hub=wor", "workspace", "list"}},
		{"skips a boolean flag alone", []string{"works", "-q", "l"}, []string{"workspace", "-q", "list"}},
		{"stops at an unknown flag", []string{"works", "--unknown", "l"}, []string{"workspace", "--unknown", "l"}},
		{"stops at the end of the flags", []string{"works", "--", "l"}, []string{"workspace", "--", "l"}},
		{"keeps the arguments of a leaf", []string{"se", "wor", "l"}, []string{"send", "wor", "l"}},
		{"keeps the word a completion completes", []string{"__complete", "works", "l"}, []string{"__complete", "workspace", "l"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := testTree(&[]testInvocation{})
			prepare(root)
			got, _, err := inspectCommandLine(root, tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("an ambiguous prefix fails with the candidates", func(t *testing.T) {
		root := testTree(&[]testInvocation{})
		prepare(root)
		_, _, err := inspectCommandLine(root, []string{"wor"})
		e := FromError(err)
		assert.Equal(t, CodeUsage, e.Code)
		assert.Equal(t, `ambiguous command "wor" for "tool"; it matches: workspace, worker`, e.Message)
	})

	t.Run("an unknown name suggests the close one", func(t *testing.T) {
		root := testTree(&[]testInvocation{})
		prepare(root)
		_, _, err := inspectCommandLine(root, []string{"workspce"})
		e := FromError(err)
		assert.Equal(t, CodeUsage, e.Code)
		assert.Equal(t, `unknown command "workspce" for "tool"`, e.Message)
		assert.Contains(t, strings.Join(e.Hints, "\n"), "did you mean workspace?")
	})
}

func TestTopLevelCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"reads a full name", []string{"workspace", "list"}, "workspace"},
		{"reads a unique prefix", []string{"works", "l"}, "workspace"},
		{"reads a leaf", []string{"send", "a", "b"}, "send"},
		{"reads only the first name", []string{"workspace", "nonexistent", "--unknown"}, "workspace"},
		{"skips a flag of root and its value", []string{"--hub", "workspace", "worker", "list"}, "worker"},
		{"skips a flag with its value attached", []string{"--hub=x", "worker"}, "worker"},
		{"skips the help flag", []string{"-h", "worker"}, "worker"},
		{"looks through the help command", []string{"help", "worker", "list"}, "worker"},
		{"looks through a flag of the help command", []string{"help", "--help", "worker"}, "worker"},
		{"answers the help command for help of help", []string{"help", "help", "worker"}, "help"},
		{"looks through a completion request", []string{"__complete", "worker", ""}, "worker"},
		{"looks through a completion request without descriptions", []string{"__completeNoDesc", "works", "l"}, "workspace"},
		{"finds nothing in no arguments", nil, ""},
		{"finds nothing in flags alone", []string{"--json"}, ""},
		{"finds nothing in the help command alone", []string{"help"}, ""},
		// cobra keeps the help command out of the prefix match, so Execute
		// refuses "he" as an unknown command too.
		{"finds nothing in a prefix of the help command", []string{"he", "worker"}, ""},
		{"finds nothing in a completion of the name itself", []string{"__complete", "worker"}, ""},
		{"finds nothing in a bare completion request", []string{"__complete"}, ""},
		{"finds nothing in an ambiguous prefix", []string{"wor", "list"}, ""},
		{"finds nothing in an unknown name", []string{"nonexistent", "worker"}, ""},
		{"finds nothing after an unknown flag", []string{"--unknown", "worker"}, ""},
		{"finds nothing after the end of the flags", []string{"--", "worker"}, ""},
		{"finds nothing in a single dash", []string{"-", "worker"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := testTree(&[]testInvocation{})
			prepare(root)
			got := TopLevelCommand(root, tc.args)
			if tc.want == "" {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, got.Name())
			assert.Same(t, root, got.Parent(), "the answer is a child of root")
		})
	}

	t.Run("works on a tree that Execute has not prepared", func(t *testing.T) {
		got := TopLevelCommand(testTree(&[]testInvocation{}), []string{"help", "worker"})
		require.NotNil(t, got)
		assert.Equal(t, "worker", got.Name())
	})
}

func TestScanFlag(t *testing.T) {
	root := testTree(&[]testInvocation{})
	prepare(root)
	send, _, err := root.Find([]string{"send"})
	require.NoError(t, err)

	cases := []struct {
		token        string
		consumesNext bool
		known        bool
	}{
		{"--count", true, true},
		{"--count=3", false, true},
		{"-n", true, true},
		{"-n5", false, true},
		{"-n=5", false, true},
		{"-ln", true, true},
		{"-l", false, true},
		{"--loud", false, true},
		{"--hub", true, true},
		{"--help", false, true},
		{"--nope", false, false},
		{"-x", false, false},
		{"-lx", false, false},
		{"-=foo", false, false},
	}
	for _, tc := range cases {
		consumesNext, known := scanFlag(send, tc.token)
		assert.Equal(t, tc.known, known, "%s known", tc.token)
		assert.Equal(t, tc.consumesNext, consumesNext, "%s consumes the next argument", tc.token)
	}
}

func TestMatchSubcommand_RejectsEmptyToken(t *testing.T) {
	single := Group("tool", "Root",
		NewLeaf(Leaf{Use: "only", Short: "Only leaf", Run: func(*Invocation) (any, error) { return nil, nil }}),
	)
	prepare(single)
	_, err := matchSubcommand(single, "")
	require.Error(t, err)
	assert.Equal(t, CodeUsage, FromError(err).Code)
	assert.Contains(t, FromError(err).Message, `unknown command ""`)
	assert.Nil(t, TopLevelCommand(single, []string{""}))

	multi := testTree(&[]testInvocation{})
	prepare(multi)
	_, err = matchSubcommand(multi, "")
	require.Error(t, err)
	assert.Equal(t, CodeUsage, FromError(err).Code)
	assert.Contains(t, FromError(err).Message, `unknown command ""`)
}

func TestExecute_HelpExpandsAbbreviationsAndFailsOnUnknown(t *testing.T) {
	t.Run("expands subcommand abbreviations under help", func(t *testing.T) {
		code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "help", "works", "l")
		assert.Equal(t, ExitOK, code, "stderr: %s", stderr)
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "Usage:\n  tool workspace list [flags]\n")
	})

	t.Run("prints help of help", func(t *testing.T) {
		code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "help", "help")
		assert.Equal(t, ExitOK, code, "stderr: %s", stderr)
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "Print the help of a command")
	})

	t.Run("prints root help when called with no args", func(t *testing.T) {
		code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "help")
		assert.Equal(t, ExitOK, code, "stderr: %s", stderr)
		assert.Empty(t, stderr)
		assert.Contains(t, stdout, "Usage:\n  tool <command>\n")
	})

	t.Run("rejects unknown help topic with usage error and JSON envelope", func(t *testing.T) {
		for _, args := range [][]string{
			{"help", "nonexistent", "--json"},
			{"help", "--json", "--", "nonexistent"},
			{"help", "workspace", "list", "extra", "--json"},
		} {
			code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), args...)
			assert.Equal(t, ExitUsage, code, "args=%v", args)
			assert.Contains(t, stderr, "error: usage:", "args=%v", args)
			var env struct {
				Error *Error `json:"error"`
			}
			require.NoError(t, json.Unmarshal([]byte(stdout), &env), "args=%v stdout=%s", args, stdout)
			require.NotNil(t, env.Error, "args=%v", args)
			assert.Equal(t, CodeUsage, env.Error.Code, "args=%v", args)
		}
	})

	t.Run("rejects ambiguous prefix under help", func(t *testing.T) {
		code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "help", "wor")
		assert.Equal(t, ExitUsage, code)
		assert.Empty(t, stdout)
		assert.Contains(t, stderr, `ambiguous command "wor"`)
	})
}

func TestInspectCommandLine_ShortFlagClustersForQuiet(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		quiet bool
	}{
		{"-q alone", []string{"send", "-q"}, true},
		{"-qw cluster", []string{"send", "-qw"}, true},
		{"-wq cluster", []string{"send", "-wq"}, true},
		{"-lq cluster with local bool", []string{"send", "-lq"}, true},
		{"-nq stops at value-taking -n", []string{"send", "-nq"}, false},
		{"-qn sets quiet before value-taking -n", []string{"send", "-qn", "2"}, true},
		{"-wq=false disables quiet", []string{"send", "-q", "-wq=false"}, false},
		{"-q after unknown flag still works", []string{"send", "--bogus", "-wq"}, true},
		{"unknown shorthand before q does not set quiet", []string{"send", "-xq"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := testTree(&[]testInvocation{})
			prepare(root)
			_, opts, _ := inspectCommandLine(root, tc.args)
			assert.Equal(t, tc.quiet, opts.quiet)
		})
	}
}

func TestExecute_RunsALeafAndPrintsItsResult(t *testing.T) {
	var seen []testInvocation
	code, stdout, stderr := execute(t, testTree(&seen), "works", "ren", "w-1", "New title")
	require.Equal(t, ExitOK, code, "stderr: %s", stderr)
	require.Len(t, seen, 1)
	assert.Equal(t, testInvocation{path: "tool workspace rename", args: []string{"w-1", "New title"}}, seen[0])
	assert.Equal(t, "w-1\nNew title\n", stdout)

	code, stdout, _ = execute(t, testTree(&seen), "workspace", "rename", "w-1", "x", "--json")
	require.Equal(t, ExitOK, code)
	assert.JSONEq(t, `{"data":{"args":["w-1","x"]}}`, stdout)
}

func TestExecute_AGroupAlonePrintsItsHelp(t *testing.T) {
	code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "workspace")
	assert.Equal(t, ExitOK, code)
	assert.Empty(t, stderr)
	assert.Contains(t, stdout, "Usage:\n  tool workspace <command>\n")
	assert.Contains(t, stdout, "\n  rename ")
	assert.Contains(t, stdout, defaultAbbreviationNote)
}

// Every summary of a command list starts in one column, two spaces past the
// longest name, so a long name never runs into its summary.
func TestExecute_AlignsTheSummariesPastTheLongestName(t *testing.T) {
	_, stdout, _ := execute(t, testTree(&[]testInvocation{}), "workspace")
	assert.Contains(t, stdout, "\n  list    A leaf\n")
	assert.Contains(t, stdout, "\n  get     A leaf\n")
	assert.Contains(t, stdout, "\n  rename  A leaf\n", "the longest name keeps a gap of two spaces")
}

func TestExecute_RefusesAnUnknownCommandUnderAGroup(t *testing.T) {
	code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "workspace", "xyz")
	assert.Equal(t, ExitUsage, code)
	assert.Empty(t, stdout)
	assert.Contains(t, stderr, `error: usage: unknown command "xyz" for "tool workspace"`)
}

func TestExecute_TheUseLineSetsTheArgumentCount(t *testing.T) {
	code, _, stderr := execute(t, testTree(&[]testInvocation{}), "workspace", "rename", "w-1")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, "missing argument <title>")
	assert.Contains(t, stderr, `hint: run "tool workspace rename --help" for usage`)

	code, _, stderr = execute(t, testTree(&[]testInvocation{}), "workspace", "get", "a", "b")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `unexpected argument "b"`)

	var seen []testInvocation
	code, _, _ = execute(t, testTree(&seen), "send", "t", "a", "b", "c")
	require.Equal(t, ExitOK, code, "a variadic argument takes any count")
	assert.Equal(t, []string{"t", "a", "b", "c"}, seen[0].args)
}

func TestExecute_RefusesAMissingRequiredFlag(t *testing.T) {
	code, _, stderr := execute(t, testTree(&[]testInvocation{}), "worker", "key")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, "missing required flag --name")
}

func TestExecute_AnUnknownFlagStatesBothWaysOut(t *testing.T) {
	code, _, stderr := execute(t, testTree(&[]testInvocation{}), "send", "t", "-version")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, `a long flag takes two dashes`)
	assert.Contains(t, stderr, `a value that starts with "-" goes after "--"`)

	var seen []testInvocation
	code, _, _ = execute(t, testTree(&seen), "send", "t", "--", "-version")
	require.Equal(t, ExitOK, code, "after --, a dash starts a value")
	assert.Equal(t, []string{"t", "-version"}, seen[0].args)
}

// A group takes no flag of its own, so an unknown flag on a group states
// where the flag goes. An unknown flag on a leaf does not.
func TestExecute_AnUnknownFlagOnAGroupStatesWhereItGoes(t *testing.T) {
	code, _, stderr := execute(t, testTree(&[]testInvocation{}), "workspace", "--title", "x")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, "error: usage: unknown flag: --title\n")
	assert.Contains(t, stderr, `hint: a flag goes after the command that takes it, as in "tool workspace <command> [flags]"`)

	_, _, stderr = execute(t, testTree(&[]testInvocation{}), "workspace", "list", "--title", "x")
	assert.NotContains(t, stderr, "a flag goes after the command")
}

func TestExecute_PrintsAFailureOnceInTheFormatAskedFor(t *testing.T) {
	code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "xyz", "--json")
	assert.Equal(t, ExitUsage, code)
	var env struct {
		Error struct {
			Code    string   `json:"code"`
			Message string   `json:"message"`
			Hints   []string `json:"hints"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &env), "stdout: %s", stdout)
	assert.Equal(t, "usage", env.Error.Code)
	assert.Equal(t, `unknown command "xyz" for "tool"`, env.Error.Message)
	assert.Contains(t, stderr, "error: usage: ", "a person watching the terminal still reads the failure")
}

func TestExecute_ReadsJSONFailureFormatAsAFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		json bool
	}{
		{name: "last flag disables JSON", args: []string{"send", "--json", "--json=false"}},
		{name: "last flag enables JSON", args: []string{"send", "--json=false", "--json"}, json: true},
		{name: "case insensitive boolean", args: []string{"send", "--json=TRUE"}, json: true},
		{name: "numeric boolean", args: []string{"send", "--json=1"}, json: true},
		{name: "inherited flag value", args: []string{"send", "--hub", "--json"}},
		{name: "local flag value after a positional", args: []string{"worker", "key", "unexpected", "--name", "--json"}},
		{name: "flag value before a misspelled command", args: []string{"--hub", "--json", "workspce"}},
		{name: "flag after a parse failure", args: []string{"send", "t", "--bogus", "--json"}, json: true},
		{name: "flag before the terminator", args: []string{"send", "--json", "--"}, json: true},
		{name: "literal after the terminator", args: []string{"worker", "list", "--", "--json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []testInvocation
			code, stdout, stderr := execute(t, testTree(&seen), tc.args...)
			assert.Equal(t, ExitUsage, code)
			assert.Empty(t, seen)
			assert.Equal(t, 1, strings.Count(stderr, "error: usage:"))
			if tc.json {
				var envelope struct {
					Error *Error `json:"error"`
				}
				require.NoError(t, json.Unmarshal([]byte(stdout), &envelope))
				require.NotNil(t, envelope.Error)
				assert.Equal(t, CodeUsage, envelope.Error.Code)
			} else {
				assert.Empty(t, stdout)
			}
		})
	}
}

func TestExecute_PassesAnExitStatusThroughUnprinted(t *testing.T) {
	code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "fail")
	assert.Equal(t, 5, code)
	assert.Empty(t, stdout)
	assert.Empty(t, stderr)
}

func TestExecute_PrintsTheHelpOfALeaf(t *testing.T) {
	code, stdout, _ := execute(t, testTree(&[]testInvocation{}), "workspace", "get", "--help")
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, stdout, "Usage:\n  tool workspace get [workspace] [flags]\n")
	assert.Contains(t, stdout, "\nArguments:\n  [workspace]  A workspace id.\n")
	assert.NotContains(t, stdout, "\nFlags:\n", "a leaf whose one flag is --help prints no Flags: section")
	assert.Contains(t, stdout, "\nGlobal Flags:\n")
	assert.Contains(t, stdout, "--hub string")
	assert.NotContains(t, stdout, defaultAbbreviationNote, "a leaf has no command name to shorten")

	_, stdout, _ = execute(t, testTree(&[]testInvocation{}), "worker", "key", "--help")
	assert.Contains(t, stdout, "--name string  the name of the key (required)")

	_, stdout, _ = execute(t, testTree(&[]testInvocation{}), "--help")
	assert.True(t, strings.HasSuffix(stdout, "\nRead the guide first.\n"), "the footer prints last: %q", stdout)
}

// Run returns the error of the command and prints nothing for it: the caller
// decides how a failure reads. A result still prints.
func TestRun_ReturnsTheErrorUnprinted(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), testTree(&[]testInvocation{}), []string{"workspace", "xyz"}, strings.NewReader(""), &stdout, &stderr)
	require.Error(t, err)
	assert.Equal(t, CodeUsage, FromError(err).Code)
	assert.Empty(t, stdout.String())
	assert.Empty(t, stderr.String())

	var seen []testInvocation
	stdout.Reset()
	require.NoError(t, Run(context.Background(), testTree(&seen), []string{"works", "ren", "w-1", "x"}, strings.NewReader(""), &stdout, &stderr),
		"Run expands an abbreviation as Execute does")
	require.Len(t, seen, 1)
	assert.Equal(t, "tool workspace rename", seen[0].path)
	assert.Equal(t, "w-1\nx\n", stdout.String())
}

// A leaf runs under a context that an interrupt ends, and it reports an
// error after an interrupt as the interrupted exit status. A leaf that
// handles the signals itself runs under the context of the caller, and its
// error stays its own: an error after a shutdown signal is a failure of the
// shutdown.
func TestNewLeaf_HandlesSignals(t *testing.T) {
	failure := errors.New("shutdown failed")
	run := func(handles bool) (context.Context, context.Context, error) {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(ErrInterrupted)
		var got context.Context
		leaf := NewLeaf(Leaf{
			Use:            "serve",
			Short:          "Serve",
			HandlesSignals: handles,
			Run: func(inv *Invocation) (any, error) {
				got = inv.Context
				return nil, failure
			},
		})
		err := Run(ctx, leaf, nil, strings.NewReader(""), io.Discard, io.Discard)
		return ctx, got, err
	}

	parent, got, err := run(false)
	assert.NotSame(t, parent, got, "a leaf listens for the signals under a context of its own")
	var status *ExitStatus
	require.ErrorAs(t, err, &status)
	assert.Equal(t, ExitInterrupted, status.Code)

	parent, got, err = run(true)
	assert.Equal(t, parent, got, "a leaf that handles the signals runs under the context of the caller")
	require.ErrorIs(t, err, failure)
	require.NotErrorAs(t, err, &status)
}

func TestDisallowInheritedFlags_GroupRejectionAndCompletionFiltering(t *testing.T) {
	newTree := func() *cobra.Command {
		noop := func(*Invocation) (any, error) { return map[string]any{"ok": true}, nil }
		localHubLeaf := NewLeaf(Leaf{
			Use:   "connect",
			Short: "Connect with local hub",
			Flags: func(fs *pflag.FlagSet) {
				fs.StringP("hub", "H", "", "local hub URL")
			},
			Run: noop,
		})
		nestedSub := Group("sub", "Nested daemon group",
			NewLeaf(Leaf{Use: "check", Short: "Check nested status", Run: noop}),
		)
		daemon := Group("daemon", "Manage local daemon",
			NewLeaf(Leaf{Use: "status", Short: "Show daemon status", Run: noop}),
			localHubLeaf,
			nestedSub,
		)
		DisallowInheritedFlags(daemon, "hub", "quiet")
		root := Group("tool", "A tool",
			daemon,
			Group("workspace", "Manage workspaces",
				NewLeaf(Leaf{Use: "list", Short: "List workspaces", Run: noop}),
			),
		)
		root.PersistentFlags().StringP("hub", "H", "", "hub URL")
		AddOutputFlags(root)
		return root
	}

	t.Run("group and nested subgroup reject disallowed inherited long and short flags", func(t *testing.T) {
		for _, args := range [][]string{
			{"daemon", "--hub", "https://example.com"},
			{"daemon", "-H", "https://example.com"},
			{"daemon", "-q"},
			{"daemon", "sub", "--hub", "https://example.com"},
			{"daemon", "sub", "-q"},
			{"daemon", "sub", "check", "--hub", "https://example.com"},
			{"daemon", "sub", "check", "-q"},
		} {
			code, stdout, stderr := execute(t, newTree(), args...)
			assert.Equal(t, ExitUsage, code, "args=%v", args)
			assert.Empty(t, stdout, "args=%v", args)
			assert.Contains(t, stderr, "unknown flag:", "args=%v", args)
		}
	})

	t.Run("__complete filters disallowed long and short flags on group, nested group, and leaf", func(t *testing.T) {
		for _, compCmd := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
			for _, prefix := range [][]string{{"daemon"}, {"daemon", "sub"}, {"daemon", "status"}, {"daemon", "sub", "check"}} {
				args := append(append([]string{compCmd}, prefix...), "--")
				code, stdout, stderr := execute(t, newTree(), args...)
				require.Equal(t, ExitOK, code, "args=%v stderr=%s", args, stderr)
				assert.NotContains(t, stdout, "--hub", "args=%v", args)
				assert.NotContains(t, stdout, "--quiet", "args=%v", args)
				assert.Contains(t, stdout, "--json", "args=%v", args)
				assert.Contains(t, stdout, "\n:", "args=%v", args)

				shortArgs := append(append([]string{compCmd}, prefix...), "-")
				code, stdout, stderr = execute(t, newTree(), shortArgs...)
				require.Equal(t, ExitOK, code, "args=%v stderr=%s", shortArgs, stderr)
				for line := range strings.SplitSeq(strings.TrimRight(stdout, "\n"), "\n") {
					cand, _, _ := strings.Cut(line, "\t")
					assert.NotEqual(t, "-q", cand, "args=%v must filter disallowed shorthand -q", shortArgs)
					assert.NotEqual(t, "-H", cand, "args=%v must filter disallowed shorthand -H", shortArgs)
					assert.NotEqual(t, "--quiet", cand, "args=%v", shortArgs)
					assert.NotEqual(t, "--hub", cand, "args=%v", shortArgs)
				}
			}
		}
	})

	t.Run("__complete keeps local flag and local shorthand matching disallowed inherited flag", func(t *testing.T) {
		for _, compCmd := range []string{cobra.ShellCompRequestCmd, cobra.ShellCompNoDescRequestCmd} {
			code, stdout, stderr := execute(t, newTree(), compCmd, "daemon", "connect", "--")
			require.Equal(t, ExitOK, code, "cmd=%s stderr=%s", compCmd, stderr)
			assert.Contains(t, stdout, "--hub", "cmd=%s", compCmd)
			assert.NotContains(t, stdout, "--quiet", "cmd=%s", compCmd)

			code, stdout, stderr = execute(t, newTree(), compCmd, "daemon", "connect", "-")
			require.Equal(t, ExitOK, code, "cmd=%s stderr=%s", compCmd, stderr)
			var candidates []string
			for line := range strings.SplitSeq(strings.TrimRight(stdout, "\n"), "\n") {
				cand, _, _ := strings.Cut(line, "\t")
				candidates = append(candidates, cand)
			}
			assert.Contains(t, candidates, "-H", "cmd=%s must keep local -H shorthand", compCmd)
			assert.NotContains(t, candidates, "-q", "cmd=%s must still filter disallowed -q", compCmd)
			assert.NotContains(t, candidates, "--quiet", "cmd=%s must still filter disallowed --quiet", compCmd)
		}
	})

	t.Run("__complete still offers inherited flags on commands that allow them", func(t *testing.T) {
		code, stdout, stderr := execute(t, newTree(), cobra.ShellCompRequestCmd, "workspace", "list", "--")
		require.Equal(t, ExitOK, code, "stderr=%s", stderr)
		assert.Contains(t, stdout, "--hub")
		assert.Contains(t, stdout, "--quiet")
	})

	t.Run("disallowedFlagsForCompletion stops after -- and filterCompletionOutput handles equals and no trailing newline", func(t *testing.T) {
		root := newTree()
		prepare(root)
		assert.Nil(t, disallowedFlagsForCompletion(root, []string{cobra.ShellCompRequestCmd, "daemon", "--", "--hub"}))
		assert.Nil(t, disallowedFlagsForCompletion(root, []string{cobra.ShellCompRequestCmd}))

		disallowed := map[string]bool{"--hub": true, "-q": true}
		raw := []byte("--hub=\thub url\n-q\tquiet\n--json\tjson\n:4")
		assert.Equal(t, "--json\tjson\n:4", string(filterCompletionOutput(raw, disallowed)))
		assert.Equal(t, raw, filterCompletionOutput(raw, nil))
		assert.Empty(t, filterCompletionOutput(nil, disallowed))
	})
}

func TestSetAbbreviationNote_OverridesGroupHelpNote(t *testing.T) {
	root := testTree(&[]testInvocation{})
	custom := "Short prefixes and aliases (ws, ls) work at every level."
	SetAbbreviationNote(root, custom)

	subCustom := "Workspace-specific abbreviation note."
	ws, _, err := root.Find([]string{"workspace"})
	require.NoError(t, err)
	SetAbbreviationNote(ws, subCustom)

	code, stdout, stderr := execute(t, root, "--help")
	require.Equal(t, ExitOK, code, stderr)
	assert.Contains(t, stdout, custom)
	assert.NotContains(t, stdout, defaultAbbreviationNote)

	code, stdout, stderr = execute(t, root, "workspace")
	require.Equal(t, ExitOK, code, stderr)
	assert.Contains(t, stdout, subCustom)
	assert.NotContains(t, stdout, custom)

	code, stdout, stderr = execute(t, root, "worker")
	require.Equal(t, ExitOK, code, stderr)
	assert.Contains(t, stdout, custom)
	assert.NotContains(t, stdout, subCustom)

	code, stdout, stderr = execute(t, root, "workspace", "list", "--help")
	require.Equal(t, ExitOK, code, stderr)
	assert.NotContains(t, stdout, custom)
	assert.NotContains(t, stdout, subCustom)
}

func TestInspectCommandLine_ErrorPrinterFormatColorAndQuiet(t *testing.T) {
	cases := []struct {
		name           string
		args           []string
		wantFormat     Format
		wantColor      ColorMode
		wantQuiet      bool
		wantResolvedFm Format
	}{
		{
			name:           "--json=false resets FormatJSON to FormatText",
			args:           []string{"send", "--format", "json", "--json=false"},
			wantFormat:     FormatText,
			wantResolvedFm: FormatText,
		},
		{
			name:           "--json=false preserves non-JSON format",
			args:           []string{"send", "--format", "ndjson", "--json=false"},
			wantFormat:     FormatNDJSON,
			wantResolvedFm: FormatNDJSON,
		},
		{
			name:           "--color always and --quiet resolve to FormatQuiet",
			args:           []string{"send", "--color", "always", "--quiet"},
			wantFormat:     FormatText,
			wantColor:      ColorAlways,
			wantQuiet:      true,
			wantResolvedFm: FormatQuiet,
		},
		{
			name:           "--color=never and --quiet=false",
			args:           []string{"send", "--quiet=true", "--quiet=false", "--color=never"},
			wantFormat:     FormatText,
			wantColor:      ColorNever,
			wantQuiet:      false,
			wantResolvedFm: FormatText,
		},
		{
			name:           "-q=false clears quiet",
			args:           []string{"send", "-q", "-q=false"},
			wantFormat:     FormatText,
			wantQuiet:      false,
			wantResolvedFm: FormatText,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := testTree(&[]testInvocation{})
			prepare(root)
			_, opts, _ := inspectCommandLine(root, tc.args)
			assert.Equal(t, tc.wantFormat, opts.format)
			assert.Equal(t, tc.wantColor, opts.color)
			assert.Equal(t, tc.wantQuiet, opts.quiet)
			assert.Equal(t, tc.wantResolvedFm, opts.resolvedFormat())
		})
	}
}

func TestIsDestructiveSuggestion_RequiresExplicitDestructiveFlag(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	root := Group("tool", "Root",
		Group("item", "Items",
			NewLeaf(Leaf{Use: "delete <id>", Short: "Delete without destructive marker", Run: noop}),
		),
	)
	// Edit distance 2 ("delat" vs "delete") is suggested when Destructive is false.
	code, _, stderr := execute(t, root, "item", "delat")
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, stderr, "did you mean delete?")
}

func TestMutatingLeaf_UsesStandardTextRendering(t *testing.T) {
	type sendResult struct {
		ID          string `json:"id"`
		InputID     string `json:"input_id"`
		RequestID   string `json:"request_id,omitempty"`
		ScheduledAt string `json:"scheduled_at,omitempty"`
	}
	type batchItem struct {
		ID    string `json:"id" table:"ID"`
		Title string `json:"title" table:"TITLE"`
	}
	type batchResult struct {
		Items []batchItem `json:"items"`
	}
	newRoot := func() (*cobra.Command, *optionsRenderedStub) {
		stub := &optionsRenderedStub{}
		root := Group("tool", "Root",
			NewLeaf(Leaf{
				Use:     "send",
				Short:   "Send a message",
				Mutates: true,
				Run: func(*Invocation) (any, error) {
					return sendResult{ID: "agent-1", InputID: "input-2", RequestID: "req-3"}, nil
				},
			}),
			NewLeaf(Leaf{
				Use:     "send-minimal",
				Short:   "Send a message without optional fields",
				Mutates: true,
				Run: func(*Invocation) (any, error) {
					return sendResult{ID: "agent-1", InputID: "input-2"}, nil
				},
			}),
			NewLeaf(Leaf{
				Use:     "empty",
				Short:   "Return nil result",
				Mutates: true,
				Run:     func(*Invocation) (any, error) { return nil, nil },
			}),
			NewLeaf(Leaf{
				Use:     "batch",
				Short:   "Batch update",
				Mutates: true,
				Run: func(*Invocation) (any, error) {
					return batchResult{Items: []batchItem{{ID: "a-1", Title: "Alpha"}, {ID: "a-2", Title: "Beta"}}}, nil
				},
			}),
			NewLeaf(Leaf{
				Use:     "custom",
				Short:   "Custom renderer mutation",
				Mutates: true,
				Run:     func(*Invocation) (any, error) { return stub, nil },
			}),
			NewLeaf(Leaf{
				Use:     "plain-custom",
				Short:   "Plain TextRenderer mutation",
				Mutates: true,
				Run:     func(*Invocation) (any, error) { return rendered("summary line\n"), nil },
			}),
		)
		AddOutputFlags(root)
		return root, stub
	}

	t.Run("object prints aligned key-value lines", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "send")
		require.Equal(t, ExitOK, code, stderr)
		assert.Equal(t, "id:         agent-1\ninput_id:   input-2\nrequest_id: req-3\n", stdout)
	})

	t.Run("omitted optional fields do not affect key alignment", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "send-minimal")
		require.Equal(t, ExitOK, code, stderr)
		assert.Equal(t, "id:       agent-1\ninput_id: input-2\n", stdout)
	})

	t.Run("fields flag projects aligned key-value lines", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "send", "--fields", "id,request_id")
		require.Equal(t, ExitOK, code, stderr)
		assert.Equal(t, "id:         agent-1\nrequest_id: req-3\n", stdout)
	})

	t.Run("nil mutation result prints nothing", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "empty")
		require.Equal(t, ExitOK, code, stderr)
		assert.Empty(t, stdout)
	})

	t.Run("single list wrapper prints a table", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "batch")
		require.Equal(t, ExitOK, code, stderr)
		assert.Contains(t, stdout, "ID")
		assert.Contains(t, stdout, "TITLE")
		assert.Contains(t, stdout, "a-1")
		assert.Contains(t, stdout, "Alpha")
		assert.NotContains(t, stdout, "items: [")
	})

	t.Run("OptionsTextRenderer still receives table options", func(t *testing.T) {
		root, stub := newRoot()
		code, stdout, stderr := execute(t, root, "custom", "--wide", "--color", "always")
		require.Equal(t, ExitOK, code, stderr)
		assert.True(t, stub.called)
		assert.Equal(t, "custom-options-output\n", stdout)
		assert.True(t, stub.seenOpts.opts.wide)
		assert.True(t, stub.seenOpts.opts.style.enabled)
	})

	t.Run("TextRenderer still controls custom text output", func(t *testing.T) {
		root, _ := newRoot()
		code, stdout, stderr := execute(t, root, "plain-custom")
		require.Equal(t, ExitOK, code, stderr)
		assert.Equal(t, "summary line\n", stdout)
	})
}
