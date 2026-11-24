package cli

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWriteReference(t *testing.T) {
	root := testTree(&[]testInvocation{})
	prepare(root)
	var out bytes.Buffer
	require.NoError(t, WriteReference(&out, Describe(root), "workspace"))
	page := out.String()

	assert.Contains(t, page, "## Flags of every command\n\nEvery command under `tool workspace` takes these flags.\n\n")
	assert.Contains(t, page, "| `--hub string` | hub URL |\n")
	assert.Contains(t, page, "| `-q, --quiet` | print only the ids of the result, one per line |\n")

	assert.Contains(t, page, "### `tool workspace get`\n\nA leaf.\n\n```text\ntool workspace get [workspace] [flags]\n```\n\n")
	assert.Contains(t, page, "| `[workspace]` | A workspace id. |\n")
	assert.Contains(t, page, "### `tool workspace rename`\n\nA leaf.\n\nThis command changes state.\n\n")
	assert.NotContains(t, page, "tool worker", "only the commands under the path print")
	assert.NotContains(t, page, "| `--hub string` | hub URL |\n| `--hub", "a global flag prints once")

	var none bytes.Buffer
	assert.Error(t, WriteReference(&none, Describe(root), "nothing"))
}

func TestSharedGlobalFlagsAndWriteReferenceForGroups(t *testing.T) {
	root := testTree(&[]testInvocation{})
	prepare(root)
	tree := Describe(root)

	global, hasPartial := SharedGlobalFlags(tree, map[string]bool{"fail": true})
	assert.False(t, hasPartial)
	require.NotEmpty(t, global)

	var flagsOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&flagsOut, global, hasPartial))
	assert.Contains(t, flagsOut.String(), "## Flags of every command\n\nEvery control command takes these flags.\n\n")
	assert.Contains(t, flagsOut.String(), "| `--hub string` | hub URL |\n")
	assert.Contains(t, flagsOut.String(), "| `-q, --quiet` | print only the ids of the result, one per line |\n")

	var partialOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&partialOut, global, true))
	assert.Contains(t, partialOut.String(), "every online command takes `--hub`")

	var emptyFlagsOut bytes.Buffer
	require.NoError(t, WriteSharedFlags(&emptyFlagsOut, nil, false))
	assert.Empty(t, emptyFlagsOut.String())

	// Ordering follows topLevelNames (send before workspace), omits global flags,
	// preserves local flags, and excludes unlisted top-level commands.
	var groupOut bytes.Buffer
	require.NoError(t, WriteReferenceForGroups(&groupOut, tree, []string{"send", "workspace"}, global))
	page := groupOut.String()

	sendPos := bytes.Index(groupOut.Bytes(), []byte("## `tool send`\n"))
	wsPos := bytes.Index(groupOut.Bytes(), []byte("## `tool workspace`\n"))
	require.GreaterOrEqual(t, sendPos, 0)
	require.Greater(t, wsPos, sendPos, "output order must follow topLevelNames")
	assert.Contains(t, page, "| `-n, --count int` | how many times (default: 1) |\n")
	assert.Contains(t, page, "### `tool workspace get`\n")
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

	// SharedGlobalFlags on empty filtered tree returns nil, false.
	none, partial := SharedGlobalFlags(CommandTree{}, nil)
	assert.Empty(t, none)
	assert.False(t, partial)
}

func TestEscapeCell(t *testing.T) {
	assert.Equal(t, `one of: keep\|push, &lt;tab&gt; &amp; more`, escapeCell("one of: keep|push, <tab> & more"))
	assert.Equal(t, "two lines", escapeCell("two\nlines"))
}
