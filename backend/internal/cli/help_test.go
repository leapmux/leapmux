package cli

import (
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFlagSections_MultipleGroupsAlignToOneColumn(t *testing.T) {
	fs := pflag.NewFlagSet("x", pflag.ContinueOnError)
	fs.SortFlags = false
	fs.String("alpha", "", "ungrouped flag")
	fs.String("very-long-flag", "", "grouped flag")
	SetFlagGroup(fs, "very-long-flag", "Advanced")
	fs.BoolP("force", "f", false, "force operation")
	SetFlagGroup(fs, "force", "Advanced")
	fs.Int("retries", 3, "retry count")
	SetFlagGroup(fs, "retries", "Network")

	got := flagSections(fs)
	want := "\n\nFlags:\n" +
		"      --alpha string           ungrouped flag\n\n" +
		"Advanced:\n" +
		"      --very-long-flag string  grouped flag\n" +
		"  -f, --force                  force operation\n\n" +
		"Network:\n" +
		"      --retries int            retry count (default: 3)"
	assert.Equal(t, want, got)
}

func TestCommandSections_UsesExplicitGroupsOnly(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	ver := NewLeaf(Leaf{Use: "version", Short: "Show version", Run: noop})
	SetCommandGroup(ver, "Meta")
	root := Group("tool", "Root", ver)
	prepare(root)

	// Without SetCommandGroup on help, help lands in Other rather than a
	// hardcoded Meta special case.
	got := commandSections(root)
	assert.Contains(t, got, "Meta:\n  version  Show version\n\nOther:\n  help     Print the help of a command")

	// When the caller assigns help to Meta via SetCommandGroup, it joins Meta.
	for _, c := range root.Commands() {
		if c.Name() == "help" {
			SetCommandGroup(c, "Meta")
		}
	}
	gotWithGroup := commandSections(root)
	assert.Contains(t, gotWithGroup, "Meta:\n  version  Show version\n  help     Print the help of a command")
	assert.NotContains(t, gotWithGroup, "Other:")
}

func TestHelpSections_EdgeCases(t *testing.T) {
	t.Run("flagSections omits hidden and help flags and omits Flags header when all grouped", func(t *testing.T) {
		fsHidden := pflag.NewFlagSet("hidden", pflag.ContinueOnError)
		fsHidden.Bool("help", false, "help")
		fsHidden.String("secret", "", "hidden flag")
		require.NoError(t, fsHidden.MarkHidden("secret"))
		assert.Empty(t, flagSections(fsHidden))

		fsGrouped := pflag.NewFlagSet("grouped", pflag.ContinueOnError)
		fsGrouped.String("addr", "", "server address")
		SetFlagGroup(fsGrouped, "addr", "Network")
		got := flagSections(fsGrouped)
		assert.NotContains(t, got, "Flags:")
		assert.Equal(t, "\n\nNetwork:\n      --addr string  server address", got)
	})

	t.Run("commandSections returns empty when only hidden subcommands exist", func(t *testing.T) {
		hidden := NewLeaf(Leaf{Use: "internal", Short: "Internal", Run: func(*Invocation) (any, error) { return nil, nil }})
		hidden.Hidden = true
		root := Group("tool", "Root", hidden)
		assert.Empty(t, commandSections(root))
	})

	t.Run("argUsages, nextHints, and inheritedFlagUsages filter absent and disallowed items", func(t *testing.T) {
		noop := func(*Invocation) (any, error) { return nil, nil }
		leaf := NewLeaf(Leaf{
			Use:   "send <target> [note]",
			Short: "Send message",
			Args:  map[string]string{"target": "Target id."},
			Next:  []string{"tool wait <target>", "tool logs <target>"},
			Run:   noop,
		})
		bare := NewLeaf(Leaf{Use: "bare <id>", Short: "Bare", Run: noop})
		DisallowInheritedFlags(leaf, "hub")
		root := Group("tool", "Root", leaf, bare)
		root.PersistentFlags().String("hub", "", "hub URL")
		root.PersistentFlags().String("secret", "", "hidden persistent")
		require.NoError(t, root.PersistentFlags().MarkHidden("secret"))
		root.PersistentFlags().Bool("json", false, "print JSON")

		assert.Equal(t, "  <target>  Target id.", argUsages(leaf))
		assert.Empty(t, argUsages(bare))
		assert.Equal(t, "  tool wait <target>\n  tool logs <target>", nextHints(leaf))
		assert.Empty(t, nextHints(bare))

		inherited := inheritedFlagUsages(leaf)
		assert.Contains(t, inherited, "--json")
		assert.NotContains(t, inherited, "--hub")
		assert.NotContains(t, inherited, "--secret")

		// Ensure standalone cobra command with no subcommands returns empty commandSections.
		assert.Empty(t, commandSections(&cobra.Command{Use: "leaf"}))
	})
}
