package cli

import (
	"sync"
	"testing"
	"weak"

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

func TestNewLeaf_FlagCompletePanicsOnUndeclaredNilOrCollision(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	dummy := func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"alpha"}, cobra.ShellCompDirectiveNoFileComp
	}

	assert.PanicsWithValue(t, `cli: "list" registers completion for undeclared flag "missing"`, func() {
		NewLeaf(Leaf{
			Use:          "list",
			Short:        "List",
			FlagComplete: map[string]cobra.CompletionFunc{"missing": dummy},
			Run:          noop,
		})
	})

	assert.PanicsWithValue(t, `cli: "list" registers nil completion for flag "provider"`, func() {
		NewLeaf(Leaf{
			Use:   "list",
			Short: "List",
			Flags: func(fs *pflag.FlagSet) {
				fs.String("provider", "", "provider")
			},
			FlagComplete: map[string]cobra.CompletionFunc{"provider": nil},
			Run:          noop,
		})
	})

	assert.Panics(t, func() {
		NewLeaf(Leaf{
			Use:   "set",
			Short: "Set",
			Flags: func(fs *pflag.FlagSet) {
				fs.String("provider", "", "provider")
				SetEnum(fs, "provider", "altcha", "turnstile")
			},
			FlagComplete: map[string]cobra.CompletionFunc{"provider": dummy},
			Run:          noop,
		})
	}, "FlagComplete must panic when colliding with SetEnum")

	leaf := NewLeaf(Leaf{
		Use:   "list",
		Short: "List",
		Flags: func(fs *pflag.FlagSet) {
			fs.String("provider", "", "provider")
		},
		FlagComplete: map[string]cobra.CompletionFunc{"provider": dummy},
		Run:          noop,
	})
	fn, ok := leaf.GetFlagCompletionFunc("provider")
	require.True(t, ok)
	vals, dir := fn(leaf, nil, "")
	assert.Equal(t, []string{"alpha"}, vals)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)
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

func TestFlagSections_StatesWhatTheDeclarationAdds(t *testing.T) {
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

	assert.Equal(t, "\n\nFlags:\n"+
		"      --type string    the type of the tab (one of: agent, terminal)\n"+
		"      --worker string  the worker (default: $LEAPMUX_CONTROL_WORKER_ID)\n"+
		"      --name string    the name (required)\n"+
		"      --option string  an option (repeatable)\n"+
		"      --limit int      page size (default: 50)\n"+
		"  -q, --quiet          print ids only",
		flagSections(fs), "the help flag is left out, because every command has it")
}

func TestArgSpecUsage(t *testing.T) {
	assert.Equal(t, "<agent>", ArgSpec{Name: "agent", Required: true}.Usage())
	assert.Equal(t, "[agent]", ArgSpec{Name: "agent"}.Usage())
	assert.Equal(t, "<agent>...", ArgSpec{Name: "agent", Required: true, Variadic: true}.Usage())
	assert.Equal(t, "[agent]...", ArgSpec{Name: "agent", Variadic: true}.Usage())
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

// pflag's GetStringArray reads the values back from their CSV form, which
// states one empty value as "[]", so a lone `--x ""` came back as no value.
func TestInvocation_StringArrayKeepsEveryValueAsGiven(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want []string
	}{
		"a lone empty value":     {args: []string{"--x", ""}, want: []string{""}},
		"an empty value and one": {args: []string{"--x", "", "--x", "a"}, want: []string{"", "a"}},
		"a comma stays inside":   {args: []string{"--x", "a,b"}, want: []string{"a,b"}},
		"a quote stays inside":   {args: []string{"--x", `"a"`}, want: []string{`"a"`}},
		"no value":               {args: nil, want: []string{}},
	} {
		t.Run(name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "set"}
			cmd.Flags().StringArray("x", nil, "")
			require.NoError(t, cmd.Flags().Parse(tc.args))
			inv := &Invocation{Command: cmd}
			assert.Equal(t, tc.want, inv.StringArray("x"))
		})
	}
}

// A command that reads a list flag that it does not declare, or a flag that
// holds one value, has a bug that no command line can trigger, so the read
// panics rather than return nothing.
func TestInvocation_StringArrayPanicsOnAFlagThatIsNoList(t *testing.T) {
	cmd := &cobra.Command{Use: "set"}
	cmd.Flags().String("one", "", "")
	require.NoError(t, cmd.Flags().Parse(nil))
	inv := &Invocation{Command: cmd}

	assert.PanicsWithValue(t, `cli: set declares no flag "missing"`, func() { inv.StringArray("missing") })
	assert.PanicsWithValue(t, `cli: flag "one" of set is not a list`, func() { inv.StringArray("one") })
}

func TestExecute_RejectsEmptyFieldsFlag(t *testing.T) {
	for _, value := range []string{"", "   ", ",", " , ", ",,,"} {
		t.Run("rejects "+value, func(t *testing.T) {
			code, _, stderr := execute(t, testTree(&[]testInvocation{}), "workspace", "list", "--fields", value)
			assert.Equal(t, ExitUsage, code)
			assert.Contains(t, stderr, "--fields is empty")
		})
	}
	var seen []testInvocation
	code, _, stderr := execute(t, testTree(&seen), "workspace", "list", "--fields", "id, state")
	assert.Equal(t, ExitOK, code, "stderr: %s", stderr)
}

func TestMarkLocalOnly_AndDefensiveMetadataCopies(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	inExamples := []Example{{Command: "tool local status"}}
	inArgs := map[string]string{"id": "Original arg help."}
	inNext := []string{"tool local logs"}

	grandchild := NewLeaf(Leaf{Use: "deep", Short: "Deep leaf", Run: noop})
	subGroup := Group("sub", "Nested local group", grandchild)
	leaf := NewLeaf(Leaf{
		Use:      "status [id]",
		Short:    "Show status",
		Examples: inExamples,
		Args:     inArgs,
		Next:     inNext,
		Run:      noop,
	})
	group := Group("local", "Local group", leaf, subGroup)
	other := NewLeaf(Leaf{Use: "remote", Short: "Remote leaf", Run: noop})
	root := Group("tool", "Root", group, other)

	// Mutating caller input slices and maps after NewLeaf must not alter stored metadata.
	inExamples[0].Command = "caller-mutated"
	inArgs["id"] = "caller-mutated"
	inNext[0] = "caller-mutated"
	assert.Equal(t, "tool local status", ExamplesOf(leaf)[0].Command)
	assert.Equal(t, "Original arg help.", ArgsOf(leaf)[0].Help)
	assert.Equal(t, "tool local logs", NextOf(leaf)[0])

	assert.False(t, IsLocalOnly(nil))
	assert.False(t, IsLocalOnly(group))
	assert.False(t, IsLocalOnly(leaf))
	assert.False(t, IsLocalOnly(grandchild))

	MarkLocalOnly(group)
	assert.True(t, IsLocalOnly(group))
	assert.True(t, IsLocalOnly(leaf), "direct children inherit local-only status")
	assert.True(t, IsLocalOnly(grandchild), "multi-level descendants inherit local-only status")
	assert.False(t, IsLocalOnly(other))
	assert.False(t, IsLocalOnly(root))

	// Mutating slices returned by ExamplesOf and NextOf must not affect stored metadata.
	examples := ExamplesOf(leaf)
	require.Len(t, examples, 1)
	examples[0].Command = "mutated"
	assert.Equal(t, "tool local status", ExamplesOf(leaf)[0].Command)

	next := NextOf(leaf)
	require.Len(t, next, 1)
	next[0] = "mutated"
	assert.Equal(t, "tool local logs", NextOf(leaf)[0])

	// Nil-command calls across metadata helpers return zero values without panicking.
	assert.Empty(t, CommandGroupOf(nil))
	assert.Empty(t, footerOf(nil))
	assert.Equal(t, defaultAbbreviationNote, abbreviationNoteOf(nil))
	assert.False(t, IsInheritedFlagDisallowed(nil, "hub"))
	assert.Nil(t, ExamplesOf(nil))
	assert.Nil(t, ArgsOf(nil))
	assert.False(t, IsMutating(nil))
	assert.False(t, IsDestructive(nil))
	assert.False(t, IsQuietUnsupported(nil))
	assert.Nil(t, NextOf(nil))
	assert.NotPanics(t, func() {
		MarkLocalOnly(nil)
		SetFooter(nil, "ignored")
		SetAbbreviationNote(nil, "ignored")
		SetCommandGroup(nil, "ignored")
		DisallowInheritedFlags(nil, "hub")
		DescribeArgs(nil, map[string]string{"id": "ignored"})
		DescribeArgs(root, nil)
	})
}

func TestCommandMeta_CleanupAndConcurrentAccess(t *testing.T) {
	cmd := &cobra.Command{Use: "ephemeral"}
	SetFooter(cmd, "temporary footer")
	MarkLocalOnly(cmd)
	assert.Equal(t, "temporary footer", footerOf(cmd))
	assert.True(t, IsLocalOnly(cmd))

	// cleanupCommandMeta removes the weak-pointer entry so later reads return zero values.
	cleanupCommandMeta(weak.Make(cmd))
	assert.Empty(t, footerOf(cmd))
	assert.False(t, IsLocalOnly(cmd))

	// Concurrent edits, reads, and cleanups must be data-race free.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := &cobra.Command{Use: "worker"}
			wp := weak.Make(local)
			for range 25 {
				SetCommandGroup(local, "Ops")
				_ = CommandGroupOf(local)
				MarkLocalOnly(local)
				_ = IsLocalOnly(local)
				cleanupCommandMeta(wp)
			}
		}()
	}
	wg.Wait()
}

func TestDisallowInheritedFlags_WhitespaceAndLocalOverride(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	override := NewLeaf(Leaf{
		Use:   "override",
		Short: "Local hub override",
		Flags: func(fs *pflag.FlagSet) {
			fs.String("hub", "", "local hub")
		},
		Run: noop,
	})
	plain := NewLeaf(Leaf{Use: "plain", Short: "Plain child", Run: noop})
	parent := Group("local", "Local commands", override, plain)

	// Empty lists and whitespace-only names are no-ops.
	DisallowInheritedFlags(parent)
	DisallowInheritedFlags(parent, "", "   ")
	assert.False(t, IsInheritedFlagDisallowed(plain, ""))

	// Leading/trailing spaces are trimmed; descendants inherit, local flags override.
	DisallowInheritedFlags(parent, " hub ", "  quiet")
	assert.True(t, IsInheritedFlagDisallowed(parent, "hub"))
	assert.True(t, IsInheritedFlagDisallowed(plain, "hub"))
	assert.True(t, IsInheritedFlagDisallowed(plain, "quiet"))
	assert.False(t, IsInheritedFlagDisallowed(override, "hub"), "local flag of the same name is not disallowed")
	assert.True(t, IsInheritedFlagDisallowed(override, "quiet"))
}

func TestInvocation_VariadicChangedAndDurationPanics(t *testing.T) {
	cmd := &cobra.Command{Use: "run <target> [items...]"}
	cmd.Flags().String("mode", "fast", "run mode")
	require.NoError(t, cmd.Flags().Parse([]string{"--mode", "slow"}))

	inv := &Invocation{
		Command: cmd,
		Args:    []string{"t1", "a", "b"},
		specs:   ParseArgSpecs(cmd.Use),
	}
	assert.Equal(t, []string{"a", "b"}, inv.Variadic("items"))
	assert.True(t, inv.HasArg("items"))
	assert.True(t, inv.Changed("mode"))
	assert.False(t, inv.Changed("missing"))
	assert.Panics(t, func() { inv.Variadic("missing") })
	assert.PanicsWithValue(t, `cli: run declares no flag "missing"`, func() { inv.Duration("missing") })
	assert.Panics(t, func() { inv.Duration("mode") })

	emptyVariadic := &Invocation{
		Command: cmd,
		Args:    []string{"t1"},
		specs:   ParseArgSpecs(cmd.Use),
	}
	assert.Nil(t, emptyVariadic.Variadic("items"))
	assert.False(t, emptyVariadic.HasArg("items"))

	unsetCmd := &cobra.Command{Use: "run"}
	unsetCmd.Flags().String("mode", "fast", "run mode")
	require.NoError(t, unsetCmd.Flags().Parse(nil))
	assert.False(t, (&Invocation{Command: unsetCmd}).Changed("mode"))
}

func TestFlagAnnotations_NilPanicsAndStringSliceEnumCompletion(t *testing.T) {
	assert.Nil(t, EnumOf(nil))
	assert.Empty(t, FlagGroupOf(nil))

	fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
	assert.Panics(t, func() { MarkRequired(fs, "missing") })
	assert.Panics(t, func() { SetEnum(fs, "missing", "a") })
	assert.Panics(t, func() { SetEnvDefault(fs, "missing", "ENV") })
	assert.Panics(t, func() { SetFlagGroup(fs, "missing", "Grp") })

	leaf := NewLeaf(Leaf{
		Use:   "filter",
		Short: "Filter items",
		Flags: func(fs *pflag.FlagSet) {
			fs.StringSlice("states", nil, "states to include")
			SetEnum(fs, "states", "idle", "working", "waiting")
		},
		Run: func(*Invocation) (any, error) { return nil, nil },
	})
	fn, ok := leaf.GetFlagCompletionFunc("states")
	require.True(t, ok)

	vals, dir := fn(leaf, nil, "idle,w")
	assert.Equal(t, []string{"idle,working", "idle,waiting"}, vals)
	assert.Equal(t, cobra.ShellCompDirectiveNoFileComp, dir)

	vals, _ = fn(leaf, nil, "i")
	assert.Equal(t, []string{"idle"}, vals)
}

func TestExecute_PrinterFlagConflictsAndRequiredPlural(t *testing.T) {
	t.Run("rejects conflicting output flags and invalid enum values", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			args []string
			want string
		}{
			{"json and format text", []string{"workspace", "list", "--json", "--format", "text"}, "--json and --format text cannot be used together"},
			{"json and format tsv", []string{"workspace", "list", "--json", "--format", "tsv"}, "--json and --format tsv cannot be used together"},
			{"json and quiet", []string{"workspace", "list", "--json", "--quiet"}, "--json and --quiet cannot be used together"},
			{"format tsv and quiet", []string{"workspace", "list", "--format", "tsv", "-q"}, "--format and --quiet cannot be used together"},
			{"invalid format", []string{"workspace", "list", "--format", "yaml"}, "--format must be one of"},
			{"invalid color", []string{"workspace", "list", "--color", "rainbow"}, "--color must be one of"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				code, _, stderr := execute(t, testTree(&[]testInvocation{}), tc.args...)
				assert.Equal(t, ExitUsage, code)
				assert.Contains(t, stderr, tc.want)
			})
		}

		// --json combined with --format json is consistent and allowed.
		code, stdout, stderr := execute(t, testTree(&[]testInvocation{}), "workspace", "list", "--json", "--format", "json")
		assert.Equal(t, ExitOK, code, stderr)
		assert.Contains(t, stdout, `"data"`)
	})

	t.Run("multiple missing required flags use plural message", func(t *testing.T) {
		root := Group("tool", "Root",
			NewLeaf(Leaf{
				Use:   "create",
				Short: "Create item",
				Flags: func(fs *pflag.FlagSet) {
					fs.String("name", "", "item name")
					MarkRequired(fs, "name")
					fs.String("owner", "", "item owner")
					MarkRequired(fs, "owner")
				},
				Run: func(*Invocation) (any, error) { return nil, nil },
			}),
		)
		code, _, stderr := execute(t, root, "create")
		assert.Equal(t, ExitUsage, code)
		assert.Contains(t, stderr, "missing required flags --name, --owner")
	})
}
