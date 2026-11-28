package cli

import (
	"encoding/json"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDescribeFiltered_EmptyAndMultiSpacePrefix(t *testing.T) {
	root := Group("leapmux", "Root",
		Group("agent", "Agents",
			NewLeaf(Leaf{
				Use:   "send <agent> [text]",
				Short: "Send message",
				Run:   func(*Invocation) (any, error) { return nil, nil },
			}),
			Group("queue", "Queue",
				NewLeaf(Leaf{
					Use:   "list <agent>",
					Short: "List queued inputs",
					Run:   func(*Invocation) (any, error) { return nil, nil },
				}),
			),
		),
		Group("worker", "Workers",
			NewLeaf(Leaf{
				Use:   "list",
				Short: "List workers",
				Run:   func(*Invocation) (any, error) { return nil, nil },
			}),
		),
	)

	t.Run("empty match encodes as JSON empty array", func(t *testing.T) {
		tree := DescribeFiltered(root, DescribeOptions{Prefix: "nonexistent"})
		assert.NotNil(t, tree.Commands)
		assert.Empty(t, tree.Commands)
		raw, err := json.Marshal(tree)
		require.NoError(t, err)
		assert.JSONEq(t, `{"commands":[]}`, string(raw))
	})

	t.Run("normalizes multiple spaces in prefix", func(t *testing.T) {
		tree := DescribeFiltered(root, DescribeOptions{Prefix: "  agent   send  "})
		require.Len(t, tree.Commands, 1)
		assert.Equal(t, "agent send", tree.Commands[0].Path)
	})

	t.Run("deep prefix with depth traverses ancestors and prunes deeper children", func(t *testing.T) {
		tree := DescribeFiltered(root, DescribeOptions{Prefix: "agent  queue", Depth: 1})
		require.Len(t, tree.Commands, 2)
		assert.Equal(t, "agent queue", tree.Commands[0].Path)
		assert.Equal(t, "agent queue list", tree.Commands[1].Path)
	})
}

func TestDescribe_HiddenDisallowedQuietUnsupportedAndFlagInfoFormatting(t *testing.T) {
	noop := func(*Invocation) (any, error) { return nil, nil }
	hidden := NewLeaf(Leaf{Use: "secret", Short: "Hidden", Run: noop})
	hidden.Hidden = true

	stream := NewLeaf(Leaf{
		Use:              "stream",
		Short:            "Stream events",
		QuietUnsupported: true,
		Flags: func(fs *pflag.FlagSet) {
			fs.String("hub", "", "local hub override")
			fs.StringSliceP("kind", "k", nil, "event kinds")
			SetEnum(fs, "kind", "open", "close")
			MarkRequired(fs, "kind")
			fs.String("token", "fallback", "auth token")
			SetEnvDefault(fs, "token", "LEAPMUX_TOKEN")
			fs.Float64("rate", 0, "sample rate")
			fs.Uint32("port", 0, "listen port")
		},
		Run: noop,
	})
	DisallowInheritedFlags(stream, "hub", "quiet")

	root := Group("leapmux", "Root", hidden, stream)
	root.PersistentFlags().String("hub", "", "global hub URL")
	root.PersistentFlags().BoolP("quiet", "q", false, "quiet output")
	root.PersistentFlags().Bool("json", false, "JSON output")

	tree := Describe(root)
	require.Len(t, tree.Commands, 1)
	cmd := tree.Commands[0]
	assert.Equal(t, "stream", cmd.Path)
	assert.True(t, cmd.QuietUnsupported)

	byFlag := map[string]FlagInfo{}
	for _, f := range cmd.Flags {
		byFlag[f.Name] = f
	}
	assert.Contains(t, byFlag, "json")
	assert.True(t, byFlag["json"].Global)
	assert.NotContains(t, byFlag, "quiet", "disallowed inherited flag must be omitted")
	require.Contains(t, byFlag, "hub")
	assert.False(t, byFlag["hub"].Global, "local flag overrides disallowed inherited flag")

	kind := byFlag["kind"]
	assert.Equal(t, "-k, --kind string", kind.Names())
	assert.Equal(t, "event kinds (one of: open, close; required; repeatable)", kind.Text())
	assert.Empty(t, kind.Default, "empty slice default [] is omitted")

	token := byFlag["token"]
	assert.Equal(t, "auth token (default: $LEAPMUX_TOKEN)", token.Text(), "EnvDefault takes precedence over Default")

	assert.Equal(t, "number", byFlag["rate"].Placeholder())
	assert.Empty(t, byFlag["rate"].Default, "zero default is omitted")
	assert.Equal(t, "int", byFlag["port"].Placeholder())
	assert.Equal(t, "", byFlag["json"].Placeholder())
	assert.Equal(t, "duration", FlagInfo{Type: "duration"}.Placeholder())
}
