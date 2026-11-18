package cli

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestExpandCommandPath(t *testing.T) {
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
			got, err := ExpandCommandPath(root, tc.args)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	t.Run("an ambiguous prefix fails with the candidates", func(t *testing.T) {
		root := testTree(&[]testInvocation{})
		prepare(root)
		_, err := ExpandCommandPath(root, []string{"wor"})
		e := FromError(err)
		assert.Equal(t, CodeUsage, e.Code)
		assert.Equal(t, `ambiguous command "wor" for "tool"; it matches: workspace, worker`, e.Message)
	})

	t.Run("an unknown name suggests the close one", func(t *testing.T) {
		root := testTree(&[]testInvocation{})
		prepare(root)
		_, err := ExpandCommandPath(root, []string{"workspce"})
		e := FromError(err)
		assert.Equal(t, CodeUsage, e.Code)
		assert.Equal(t, `unknown command "workspce" for "tool"`, e.Message)
		assert.Contains(t, strings.Join(e.Hints, "\n"), "did you mean workspace?")
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
	}
	for _, tc := range cases {
		consumesNext, known := scanFlag(send, tc.token)
		assert.Equal(t, tc.known, known, "%s known", tc.token)
		assert.Equal(t, tc.consumesNext, consumesNext, "%s consumes the next argument", tc.token)
	}
}

func TestExecute_RunsALeafAndPrintsItsResult(t *testing.T) {
	var seen []testInvocation
	code, stdout, stderr := execute(t, testTree(&seen), "works", "ren", "w-1", "New title")
	require.Equal(t, ExitOK, code, "stderr: %s", stderr)
	require.Len(t, seen, 1)
	assert.Equal(t, testInvocation{path: "tool workspace rename", args: []string{"w-1", "New title"}}, seen[0])
	assert.Equal(t, "w-1\nNew title\n", stdout, "an object that holds one list prints the bare list")

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
	assert.Contains(t, stdout, abbreviationNote)
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
	assert.NotContains(t, stdout, abbreviationNote, "a leaf has no command name to shorten")

	_, stdout, _ = execute(t, testTree(&[]testInvocation{}), "worker", "key", "--help")
	assert.Contains(t, stdout, "--name string  the name of the key (required)")

	_, stdout, _ = execute(t, testTree(&[]testInvocation{}), "--help")
	assert.True(t, strings.HasSuffix(stdout, "\nRead the guide first.\n"), "the footer prints last: %q", stdout)
}
