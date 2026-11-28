package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSharedGlobalFlagsAndWriteReferenceForGroups(t *testing.T) {
	root := testTree(&[]testInvocation{})
	root.PersistentFlags().String("secret", "", "hidden flag")
	require.NoError(t, root.PersistentFlags().MarkHidden("secret"))
	prepare(root)
	tree := Describe(root)

	global := SharedGlobalFlags(root, map[string]bool{"fail": true})
	require.NotEmpty(t, global)
	names := flagNameSet(global)
	assert.True(t, names["hub"])
	assert.True(t, names["quiet"])
	assert.False(t, names["secret"], "hidden persistent flags are omitted")

	var flagsOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&flagsOut, global, "Every control command takes these flags."))
	assert.Contains(t, flagsOut.String(), "## Flags of every command\n\nEvery control command takes these flags.\n\n")
	assert.Contains(t, flagsOut.String(), "| `--hub string` | hub URL |\n")
	assert.Contains(t, flagsOut.String(), "| `-q, --quiet` | print only the ids of the result, one per line |\n")

	var noIntroOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&noIntroOut, global, ""))
	assert.Contains(t, noIntroOut.String(), "## Flags of every command\n\n| Flag | Description |\n")

	var emptyFlagsOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&emptyFlagsOut, nil, "ignored"))
	assert.Empty(t, emptyFlagsOut.String())

	// Ordering follows topLevelNames (send before workspace), omits global flags,
	// preserves local flags, argument tables, mutates marker, and excludes unlisted top-level commands.
	var groupOut bytes.Buffer
	require.NoError(t, WriteReferenceForGroups(&groupOut, tree, []string{"send", "workspace"}, global))
	page := groupOut.String()

	sendPos := bytes.Index(groupOut.Bytes(), []byte("## `tool send`\n"))
	wsPos := bytes.Index(groupOut.Bytes(), []byte("## `tool workspace`\n"))
	require.GreaterOrEqual(t, sendPos, 0)
	require.Greater(t, wsPos, sendPos, "output order must follow topLevelNames")
	assert.Contains(t, page, "| `-n, --count int` | how many times (default: 1) |\n")
	assert.Contains(t, page, "### `tool workspace get`\n\nA leaf.\n\n```text\ntool workspace get [workspace] [flags]\n```\n\n")
	assert.Contains(t, page, "| `[workspace]` | A workspace id. |\n")
	assert.Contains(t, page, "### `tool workspace rename`\n\nA leaf.\n\n_Mutates state._\n\n")
	assert.NotContains(t, page, "tool worker")
	assert.NotContains(t, page, "tool fail")
	assert.NotContains(t, page, "| `--hub string` |")
	assert.NotContains(t, page, "## Flags of every command")

	// Error paths: empty list, duplicate name, unknown top-level name, non-top-level path.
	var errOut bytes.Buffer
	assert.Error(t, WriteReferenceForGroups(&errOut, tree, nil, global))
	assert.Error(t, WriteReferenceForGroups(&errOut, tree, []string{"workspace", "workspace"}, global))
	assert.Error(t, WriteReferenceForGroups(&errOut, tree, []string{"missing"}, global))
	assert.Error(t, WriteReferenceForGroups(&errOut, tree, []string{"workspace get"}, global))

	// SharedGlobalFlags on nil or empty root returns nil.
	assert.Empty(t, SharedGlobalFlags(nil, nil))
	emptyRoot := &cobra.Command{Use: "empty"}
	emptyRoot.PersistentFlags().String("hub", "", "hub URL")
	assert.Empty(t, SharedGlobalFlags(emptyRoot, nil))
}

func TestSharedGlobalFlags_SkipsFlagsDisallowedEverywhere(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	offline := Group("offline", "Offline commands",
		NewLeaf(Leaf{Use: "status", Short: "Show status", Run: noop}),
	)
	DisallowInheritedFlags(offline, "hub")

	online := Group("online", "Online commands",
		NewLeaf(Leaf{Use: "list", Short: "List items", Run: noop}),
	)

	root := Group("tool", "Root", offline, online)
	root.PersistentFlags().String("hub", "", "hub URL")
	root.PersistentFlags().Bool("json", false, "print JSON")

	// When online is included, --hub still applies to at least one command.
	both := flagNameSet(SharedGlobalFlags(root, nil))
	assert.True(t, both["hub"])
	assert.True(t, both["json"])

	// When online is excluded, every remaining command disallows --hub, so --hub is skipped.
	offlineOnly := flagNameSet(SharedGlobalFlags(root, map[string]bool{"online": true}))
	assert.False(t, offlineOnly["hub"])
	assert.True(t, offlineOnly["json"])
}

func TestEscapeCell(t *testing.T) {
	assert.Equal(t, `one of: keep\|push, &lt;tab&gt; &amp; more`, escapeCell("one of: keep|push, <tab> & more"))
	assert.Equal(t, "two lines", escapeCell("two\nlines"))
	assert.Equal(t, "requires `--wait` and `--output-schema`, keeps `--no-focus`", escapeCell("requires --wait and --output-schema, keeps `--no-focus`"))
}

func TestFormatProse(t *testing.T) {
	wrapped := "First line wrapped at\r\ncolumn 78 for terminal.\r\n\r\n- bullet one\r\n  continued\r\n* bullet two\r\nParagraph right after bullet with <tag> & > and --request-id."
	got := formatProse(wrapped)
	assert.Equal(t, "First line wrapped at column 78 for terminal.\n\n- bullet one continued\n* bullet two\n\nParagraph right after bullet with &lt;tag&gt; &amp; &gt; and `--request-id`.", got)
	assert.Empty(t, formatProse("   \r\n\t  "))
}

func TestBacktickFlags(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
			in:   "",
			want: "",
		},
		{
			name: "bare flags in prose",
			in:   "--resume with --wait, --output-schema, and (--no-focus).",
			want: "`--resume` with `--wait`, `--output-schema`, and (`--no-focus`).",
		},
		{
			name: "already backticked single and multi tick spans",
			in:   "keep `--wait`, ``--output-schema``, and `leapmux agent send --request-id abc` untouched; wrap --all",
			want: "keep `--wait`, ``--output-schema``, and `leapmux agent send --request-id abc` untouched; wrap `--all`",
		},
		{
			name: "standalone separator and dashes",
			in:   `pass -- or "--" before text; ignore --- and --foo- and --foo--bar and foo--bar`,
			want: `pass -- or "--" before text; ignore --- and --foo- and --foo--bar and foo--bar`,
		},
		{
			name: "urls paths and flag equals values",
			in:   `see https://example.com/--wait, /tmp/--bar, https://example.com/?q=--baz, --format=json, and --title="hi"`,
			want: `see https://example.com/--wait, /tmp/--bar, https://example.com/?q=--baz, --format=json, and --title="hi"`,
		},
		{
			name: "unmatched backtick does not block later flag",
			in:   "stray ` tick then --wait",
			want: "stray ` tick then `--wait`",
		},
		{
			name: "escaped angle brackets stay intact",
			in:   "pass --worker &lt;worker&gt; with --auto-sync",
			want: "pass `--worker` &lt;worker&gt; with `--auto-sync`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, backtickFlags(tc.in))
		})
	}
}

func TestSharedGlobalFlags_LocalOverrideAndHiddenSubcommands(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	override := NewLeaf(Leaf{
		Use:   "connect",
		Short: "Connect with local hub",
		Flags: func(fs *pflag.FlagSet) {
			fs.String("hub", "", "local hub")
		},
		Run: noop,
	})
	hidden := NewLeaf(Leaf{Use: "internal", Short: "Internal", Run: noop})
	hidden.Hidden = true
	DisallowInheritedFlags(override, "token")

	root := Group("tool", "Root", override, hidden)
	root.PersistentFlags().String("hub", "", "global hub")
	root.PersistentFlags().String("token", "", "global token")
	root.PersistentFlags().Bool("json", false, "print JSON")

	names := flagNameSet(SharedGlobalFlags(root, nil))
	assert.False(t, names["hub"], "persistent flag shadowed locally by every visible command is omitted")
	assert.False(t, names["token"], "hidden command must not pull universally disallowed flag into shared flags")
	assert.True(t, names["json"])
}

type failingReferenceWriter struct {
	err error
}

func (f failingReferenceWriter) Write([]byte) (int, error) { return 0, f.err }

func TestWriteReferenceForGroups_DeepHeadingsLiftedFlagsAndErrors(t *testing.T) {
	const longFlagHelp = "Filter results by worker identifier or a unique prefix returned by worker list"
	const longMixedArgHelp = "Target identifier accepted as a full UUID or a unique prefix returned by list."
	noop := func(*Invocation) (any, error) { return nil, nil }
	addWorkerFlag := func(fs *pflag.FlagSet) {
		fs.String("worker", "", longFlagHelp)
	}

	deepLeaf := NewLeaf(Leaf{
		Use:              "inspect <target>",
		Aliases:          []string{"show"},
		Short:            "Inspect item",
		Long:             "Long description for <target> & details.",
		Mutates:          true,
		QuietUnsupported: true,
		Args:             map[string]string{"target": longMixedArgHelp},
		Flags:            addWorkerFlag,
		Examples: []Example{
			{Comment: "Inspect by id.", Command: "tool grp sub nested inspect t1"},
			{Command: "tool grp sub nested inspect t2"},
			{Comment: "Inspect as JSON.", Command: "tool grp sub nested inspect t3 --json"},
		},
		Run: noop,
	})
	nested := Group("nested", "Nested subgroup", deepLeaf)
	sub := SetAliases(Group("sub", "Second level subgroup",
		nested,
		NewLeaf(Leaf{Use: "read [target]", Short: "Read item", Args: map[string]string{"target": longMixedArgHelp}, Flags: addWorkerFlag, Run: noop}),
		NewLeaf(Leaf{Use: "check <target>", Short: "Check item", Args: map[string]string{"target": longMixedArgHelp}, Flags: addWorkerFlag, Run: noop}),
	), "s")
	grp := Group("grp", "Top group", sub)
	root := Group("tool", "Tool", grp)
	prepare(root)

	var out bytes.Buffer
	require.NoError(t, WriteReferenceForGroups(&out, Describe(root), []string{"grp"}, nil))
	page := out.String()

	// Depth headings: depth 1 -> ##, depth 2 -> ###, depth 3 -> ####, depth 4 -> #####.
	assert.Contains(t, page, "## `tool grp`\n")
	assert.Contains(t, page, "### `tool grp sub`\n")
	assert.Contains(t, page, "Aliases: `s`.\n")
	assert.Contains(t, page, "#### `tool grp sub nested`\n")
	assert.Contains(t, page, "##### `tool grp sub nested inspect`\n")

	// Leaf renders Long description (escaped), combined metadata line, and multiple examples.
	assert.Contains(t, page, "Long description for &lt;target&gt; &amp; details.")
	assert.Contains(t, page, "_Aliases: `show` · Mutates state · Does not support `-q` / `--quiet`._")
	assert.Contains(t, page, "# Inspect by id.\ntool grp sub nested inspect t1\ntool grp sub nested inspect t2\n\n# Inspect as JSON.\ntool grp sub nested inspect t3 --json\n")

	// Mixed <target> and [target] lifts under fallback label <target>, and repeated long flag lifts too.
	// Because every command has only common args and flags, per-command tables are omitted in favor of the compact strip.
	assert.Contains(t, page, "| `<target>` | "+longMixedArgHelp+" |\n")
	assert.Contains(t, page, "| `--worker string` | "+longFlagHelp+" |\n")
	assert.NotContains(t, page, "| Argument | Description |")
	assert.NotContains(t, page, "| Flag | Description |")
	assert.Contains(t, page, "_Common: `<target>`, `--worker` (see [common arguments and flags](#common-arguments-and-flags))._\n\n```sh\n")
	assert.Contains(t, page, "_Common: `[target]`, `--worker` (see [common arguments and flags](#common-arguments-and-flags))._\n")

	// Writer errors propagate from both WriteSharedFlags and WriteReferenceForGroups.
	writeErr := errors.New("write failed")
	assert.ErrorIs(t, WriteSharedFlags(failingReferenceWriter{err: writeErr}, []FlagInfo{{Name: "json", Type: "bool", Usage: "print JSON"}}, "intro"), writeErr)
	assert.ErrorIs(t, WriteReferenceForGroups(failingReferenceWriter{err: writeErr}, Describe(root), []string{"grp"}, nil), writeErr)
}

func TestWriteReferenceForGroups_LiftedOptionalAndVariadicArgs(t *testing.T) {
	const longOptionalHelp = "An optional request handle printed by list, or a unique prefix when omitted."
	const longVariadicHelp = "One or more target files to include in the operation, separated by spaces."
	noop := func(*Invocation) (any, error) { return nil, nil }
	root := Group("tool", "Tool",
		Group("item", "Items",
			NewLeaf(Leaf{Use: "approve [request]", Short: "Approve", Args: map[string]string{"request": longOptionalHelp}, Run: noop}),
			NewLeaf(Leaf{Use: "deny [request]", Short: "Deny", Args: map[string]string{"request": longOptionalHelp}, Run: noop}),
			NewLeaf(Leaf{Use: "answer [request]", Short: "Answer", Args: map[string]string{"request": longOptionalHelp}, Run: noop}),
			NewLeaf(Leaf{Use: "add <file>...", Short: "Add", Args: map[string]string{"file": longVariadicHelp}, Run: noop}),
			NewLeaf(Leaf{Use: "check <file>...", Short: "Check", Args: map[string]string{"file": longVariadicHelp}, Run: noop}),
			NewLeaf(Leaf{Use: "drop <file>...", Short: "Drop", Args: map[string]string{"file": longVariadicHelp}, Run: noop}),
		),
	)
	prepare(root)

	var out bytes.Buffer
	require.NoError(t, WriteReferenceForGroups(&out, Describe(root), []string{"item"}, nil))
	page := out.String()
	assert.Contains(t, page, "| `[request]` | "+longOptionalHelp+" |\n")
	assert.Contains(t, page, "| `<file>...` | "+longVariadicHelp+" |\n")
	assert.NotContains(t, page, "| Argument | Description |")
	assert.Contains(t, page, "_Common: `[request]` (see [common arguments and flags](#common-arguments-and-flags))._\n")
	assert.Contains(t, page, "_Common: `<file>...` (see [common arguments and flags](#common-arguments-and-flags))._\n")
}

func TestWriteReferenceForGroups_CommonMinHelpLenAndMixedOwnAndCommon(t *testing.T) {
	// 58 chars (like admin's <user> selector) lifts under the 45-char threshold;
	// 44 chars stays inline per command.
	const userHelp58 = "A username, id:<id>, username:<name>, or a user ID prefix."
	const shortHelp44 = "Exactly forty-four characters stay inline!!."
	const workspaceFlagHelp = "Narrow target lookup to this workspace (id, prefix, or self)"
	require.Equal(t, 58, len(userHelp58))
	require.Equal(t, 44, len(shortHelp44))

	noop := func(*Invocation) (any, error) { return nil, nil }
	addCommonFlag := func(fs *pflag.FlagSet) {
		fs.StringP("workspace", "w", "", workspaceFlagHelp)
	}
	addMixedFlags := func(fs *pflag.FlagSet) {
		addCommonFlag(fs)
		fs.Bool("force", false, "skip confirmation prompt")
	}

	root := Group("tool", "Tool",
		Group("admin", "Admin",
			NewLeaf(Leaf{
				Use:      "grant <user> <role>",
				Short:    "Grant role",
				Args:     map[string]string{"user": userHelp58, "role": shortHelp44},
				Flags:    addMixedFlags,
				Examples: []Example{{Command: "tool admin grant alice editor"}},
				Run:      noop,
			}),
			NewLeaf(Leaf{
				Use:   "revoke <user> <role>",
				Short: "Revoke role",
				Args:  map[string]string{"user": userHelp58, "role": shortHelp44},
				Flags: addCommonFlag,
				Run:   noop,
			}),
			NewLeaf(Leaf{
				Use:   "show <user> <role>",
				Short: "Show user",
				Args:  map[string]string{"user": userHelp58, "role": shortHelp44},
				Flags: addCommonFlag,
				Run:   noop,
			}),
		),
	)
	prepare(root)

	var out bytes.Buffer
	require.NoError(t, WriteReferenceForGroups(&out, Describe(root), []string{"admin"}, nil))
	page := out.String()

	// Top common table lifts <user> (58 chars) and -w, --workspace string, but NOT <role> (44 chars).
	commonSection, _, ok := strings.Cut(page, "## `tool admin`")
	require.True(t, ok)
	assert.Contains(t, commonSection, "| `<user>` | A username, id:&lt;id&gt;, username:&lt;name&gt;, or a user ID prefix. |\n")
	assert.Contains(t, commonSection, "| `-w, --workspace string` | "+workspaceFlagHelp+" |\n")
	assert.NotContains(t, commonSection, "`<role>`")

	// Mixed command ("grant"): prints own <role> table, own --force table, then compact _Common:_ strip before examples.
	assert.Contains(t, page,
		"| Argument | Description |\n|---|---|\n| `<role>` | "+shortHelp44+" |\n\n"+
			"| Flag | Description |\n|---|---|\n| `--force` | skip confirmation prompt |\n\n"+
			"_Common: `<user>`, `-w, --workspace` (see [common arguments and flags](#common-arguments-and-flags))._\n\n"+
			"```sh\ntool admin grant alice editor\n```")

	// Command with only own arg and common flag ("revoke"): no Flag table, compact strip lists both common arg and shorthand flag without type.
	assert.Contains(t, page,
		"### `tool admin revoke`\n\nRevoke role.\n\n```text\ntool admin revoke <user> <role> [flags]\n```\n\n"+
			"| Argument | Description |\n|---|---|\n| `<role>` | "+shortHelp44+" |\n\n"+
			"_Common: `<user>`, `-w, --workspace` (see [common arguments and flags](#common-arguments-and-flags))._\n")
}
