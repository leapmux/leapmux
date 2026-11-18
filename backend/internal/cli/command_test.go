package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseArgSpecs(t *testing.T) {
	assert.Equal(t, []ArgSpec{
		{Name: "agent", Required: true},
		{Name: "text"},
	}, ParseArgSpecs("send <agent> [text]"))
	assert.Equal(t, []ArgSpec{
		{Name: "agent", Required: true, Variadic: true},
	}, ParseArgSpecs("wait <agent>..."))
	assert.Equal(t, []ArgSpec{
		{Name: "files", Variadic: true},
	}, ParseArgSpecs("add [files]..."))
	assert.Equal(t, []ArgSpec{
		{Name: "target", Required: true},
		{Name: "text", Variadic: true},
	}, ParseArgSpecs("send <target> [text...]"), "the repetition mark may stand inside the brackets")
	assert.Equal(t, []ArgSpec{
		{Name: "agent", Required: true, Variadic: true},
	}, ParseArgSpecs("wait <agent...>"))
	assert.Nil(t, ParseArgSpecs("list"))
	assert.Nil(t, ParseArgSpecs(""))
	assert.Nil(t, ParseArgSpecs("list plain-word"), "only <x> and [x] declare an argument")
}

func TestNewLeaf_RefusesTheDescriptionOfAnUndeclaredArgument(t *testing.T) {
	assert.PanicsWithValue(t, `cli: "get [tab]" describes the argument "agent", which its Use line does not declare`, func() {
		NewLeaf(Leaf{Use: "get [tab]", Short: "x", Args: map[string]string{"agent": "An agent."}, Run: func(*Invocation) (any, error) { return nil, nil }})
	})
}

func TestInvocation_RefusesAnUndeclaredArgumentName(t *testing.T) {
	inv := &Invocation{Command: &cobra.Command{Use: "get"}, specs: ParseArgSpecs("get [tab]")}
	assert.Panics(t, func() { inv.Arg("agent") })
	assert.Panics(t, func() { inv.HasArg("agent") })
	assert.Panics(t, func() { inv.Variadic("tab") }, "a single argument is not variadic")
	assert.Empty(t, inv.Arg("tab"), "an omitted optional argument reads as empty")
	assert.False(t, inv.HasArg("tab"))
}

func TestDescribeArgs_ALeafsOwnDescriptionWins(t *testing.T) {
	run := func(*Invocation) (any, error) { return nil, nil }
	own := NewLeaf(Leaf{Use: "get <tab>", Short: "x", Args: map[string]string{"tab": "Its own words."}, Run: run})
	plain := NewLeaf(Leaf{Use: "close <tab> [reason]", Short: "x", Run: run})
	root := Group("tool", "x", Group("tab", "x", own, plain))

	DescribeArgs(root, map[string]string{"tab": "The tree's words.", "unused": "Nothing declares it."})

	assert.Equal(t, []ArgSpec{{Name: "tab", Required: true, Help: "Its own words."}}, ArgsOf(own))
	assert.Equal(t, []ArgSpec{
		{Name: "tab", Required: true, Help: "The tree's words."},
		{Name: "reason"},
	}, ArgsOf(plain), "an argument the map does not describe stays without help")

	// A second pass for a subtree runs first in practice; the leaf's text
	// from the first pass then counts as its own.
	DescribeArgs(root, map[string]string{"tab": "Later words."})
	assert.Equal(t, "Its own words.", ArgsOf(own)[0].Help)
	assert.Equal(t, "The tree's words.", ArgsOf(plain)[0].Help)
}

func TestFlagUsages_StatesWhatTheDeclarationAdds(t *testing.T) {
	fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
	// A leaf lists its flags in the order it declares them (NewLeaf turns
	// the sorting off), so the help reads in the order the author chose.
	fs.SortFlags = false
	fs.String("type", "", "the type of the tab")
	SetEnum(fs, "type", "agent", "terminal")
	fs.String("worker", "", "the worker")
	SetEnvDefault(fs, "worker", "LEAPMUX_CONTROL_WORKER_ID")
	fs.String("name", "", "the name")
	MarkRequired(fs, "name")
	fs.StringArray("option", nil, "an option")
	fs.Int("limit", 50, "page size")
	fs.BoolP("quiet", "q", false, "print ids only")
	fs.Bool("help", false, "help")

	assert.Equal(t, ""+
		"      --type string    the type of the tab (one of: agent, terminal)\n"+
		"      --worker string  the worker (default: $LEAPMUX_CONTROL_WORKER_ID)\n"+
		"      --name string    the name (required)\n"+
		"      --option string  an option (repeatable)\n"+
		"      --limit int      page size (default: 50)\n"+
		"  -q, --quiet          print ids only",
		flagUsages(fs), "the help flag is left out, because every command has it")
}

func TestDescribe_ListsEveryCommandWithItsDeclaration(t *testing.T) {
	root := testTree(&[]testInvocation{})
	prepare(root)
	tree := Describe(root)

	byPath := map[string]CommandInfo{}
	for _, c := range tree.Commands {
		byPath[c.Path] = c
	}
	assert.NotContains(t, byPath, "help", "the help command is not part of the grammar")
	assert.NotContains(t, byPath, "", "the root itself is not listed")

	ws := byPath["workspace"]
	assert.True(t, ws.Group)
	assert.Empty(t, ws.Args, "a group takes a command, not an argument")

	rename := byPath["workspace rename"]
	assert.True(t, rename.Mutates)
	assert.Equal(t, "tool workspace rename <workspace> <title> [flags]", rename.Usage)
	assert.Equal(t, []ArgSpec{{Name: "workspace", Required: true}, {Name: "title", Required: true}}, rename.Args)

	get := byPath["workspace get"]
	assert.Equal(t, "A workspace id.", get.Args[0].Help)

	key := byPath["worker key"]
	var name, hub FlagInfo
	for _, f := range key.Flags {
		switch f.Name {
		case "name":
			name = f
		case "hub":
			hub = f
		}
	}
	assert.True(t, name.Required)
	assert.False(t, name.Global)
	assert.True(t, hub.Global, "a flag of an ancestor is global")

	list := byPath["workspace list"]
	assert.Equal(t, []string{"ls"}, list.Aliases)

	// The order is the order of the help: depth first, in declaration order.
	var paths []string
	for _, c := range tree.Commands {
		paths = append(paths, c.Path)
	}
	require.GreaterOrEqual(t, len(paths), 4)
	assert.Equal(t, []string{"workspace", "workspace list", "workspace get", "workspace rename"}, paths[:4])
}
