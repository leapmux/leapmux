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

func TestEscapeCell(t *testing.T) {
	assert.Equal(t, `one of: keep\|push, &lt;tab&gt; &amp; more`, escapeCell("one of: keep|push, <tab> & more"))
	assert.Equal(t, "two lines", escapeCell("two\nlines"))
}
