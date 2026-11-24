package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// B14: argUsages must keep the variadic "..." marker in the Arguments section
// of --help so it matches the Usage line and Markdown reference.
func TestArgUsages_PreservesVariadicEllipsis(t *testing.T) {
	cmd := NewLeaf(Leaf{
		Use:   "wait <agent>...",
		Short: "Wait for agents",
		Args:  map[string]string{"agent": "one or more agents"},
		Run:   func(*Invocation) (any, error) { return nil, nil },
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	require.NoError(t, cmd.Help())
	assert.Contains(t, out.String(), "<agent>...", "variadic argument must display ... in Arguments section")
}

// B15: Untrusted text from remote Workers/Hubs must not inject ANSI CSI/OSC
// escape sequences, C0/C1 controls, or Unicode BiDi overrides into the terminal.
func TestRenderText_SanitizesTerminalEscapeAndBiDiSequences(t *testing.T) {
	type row struct {
		ID    string `json:"id" table:"ID"`
		Title string `json:"title" table:"TITLE"`
	}
	malicious := "safe\x1b]52;c;YWJj\x07\x1b[31mred\x1b[0m\u202ereversed\u2066iso\u2028line"

	var tableBuf bytes.Buffer
	p := &Printer{Out: &tableBuf, Err: &bytes.Buffer{}}
	require.NoError(t, p.Result(struct {
		Rows []row `json:"rows"`
	}{Rows: []row{{ID: "id-1", Title: malicious}}}))
	assert.NotContains(t, tableBuf.String(), "\x1b", "table output must strip ANSI escape sequences")
	assert.NotContains(t, tableBuf.String(), "\x07", "table output must strip BEL")
	assert.NotContains(t, tableBuf.String(), "\u202e", "table output must strip BiDi override")
	assert.NotContains(t, tableBuf.String(), "\u2066", "table output must strip BiDi isolate")
	assert.NotContains(t, tableBuf.String(), "\u2028", "table output must strip Unicode line separator")
	assert.Contains(t, tableBuf.String(), "saferedreversedisoline")

	var objBuf bytes.Buffer
	p = &Printer{Out: &objBuf, Err: &bytes.Buffer{}}
	require.NoError(t, p.Result(row{ID: "id-1", Title: malicious}))
	assert.NotContains(t, objBuf.String(), "\x1b", "key-value output must strip ANSI escape sequences")
	assert.NotContains(t, objBuf.String(), "\u202e", "key-value output must strip BiDi override")

	var errBuf bytes.Buffer
	p = &Printer{Out: &bytes.Buffer{}, Err: &errBuf}
	p.Failure(Newf(CodeInternal, "bad %s", malicious).WithHint("try %s", malicious))
	assert.NotContains(t, errBuf.String(), "\x1b", "error output must strip ANSI escape sequences")
	assert.NotContains(t, errBuf.String(), "\u202e", "error output must strip BiDi override")
}

func TestSanitizeTerminalText_InvalidUTF8AndC1StringSequences(t *testing.T) {
	// Invalid UTF-8 byte must not eat the following 2 valid bytes or hide a following ESC.
	assert.Equal(t, "\uFFFDAB", SanitizeTerminalText("\xffAB"))
	assert.Equal(t, "\uFFFDred", SanitizeTerminalText("\xff\x1b[31mred\x1b[0m"))
	// 8-bit C1 OSC terminated by BEL and 8-bit ST (0xC2 0x9C).
	assert.Equal(t, "ok", SanitizeTerminalText("\xc2\x9d52;c;YWJj\x07ok"))
	assert.Equal(t, "ok", SanitizeTerminalText("\xc2\x9d52;c;YWJj\xc2\x9cok"))
}

func TestExecute_ErrorPathHonorsFormatColorAndQuiet(t *testing.T) {
	newTree := func() *cobra.Command {
		root := &cobra.Command{Use: "leapmux"}
		AddOutputFlags(root)
		root.AddCommand(NewLeaf(Leaf{
			Use:   "fail",
			Short: "Always fail",
			Run:   func(*Invocation) (any, error) { return nil, Newf(CodeNotFound, "no such item") },
		}))
		return root
	}

	for _, args := range [][]string{
		{"fail", "--format", "json"},
		{"fail", "--format=json"},
		{"fail", "--format", "ndjson"},
		{"fail", "--format=ndjson"},
	} {
		var stdout, stderr bytes.Buffer
		code := Execute(t.Context(), newTree(), args, strings.NewReader(""), &stdout, &stderr)
		assert.Equal(t, ExitNotFound, code, "args=%v", args)
		assert.Contains(t, stdout.String(), `"error"`, "args=%v must emit JSON error envelope", args)
		assert.Contains(t, stdout.String(), `"not_found"`, "args=%v", args)
	}

	// Global --format and --color flags must have enum completions registered.
	root := newTree()
	fn, ok := root.GetFlagCompletionFunc("format")
	require.True(t, ok, "--format must register enum completion")
	vals, _ := fn(root, nil, "")
	assert.Contains(t, vals, "json")
	assert.Contains(t, vals, "ndjson")
	assert.Contains(t, vals, "tsv")

	fn, ok = root.GetFlagCompletionFunc("color")
	require.True(t, ok, "--color must register enum completion")
	vals, _ = fn(root, nil, "")
	assert.Contains(t, vals, "auto")
	assert.Contains(t, vals, "never")
}

// B08: QuietRenderer lets multi-field or non-"id" results provide explicit -q
// output lines, and QuietUnsupported rejects -q with ExitUsage before Run.
type sampleQuietResult struct {
	WorkerID string   `json:"worker_id"`
	Keys     []string `json:"keys"`
}

func (r sampleQuietResult) QuietLines() []string { return r.Keys }

func TestPrinter_QuietUsesQuietRenderer(t *testing.T) {
	var out bytes.Buffer
	p := &Printer{Out: &out, Err: &bytes.Buffer{}, Quiet: true}
	require.NoError(t, p.Result(sampleQuietResult{WorkerID: "w-1", Keys: []string{"k1", "k2"}}))
	assert.Equal(t, "k1\nk2\n", out.String())
}

func TestNewLeaf_QuietUnsupportedRejectsQuietBeforeRun(t *testing.T) {
	ran := false
	root := &cobra.Command{Use: "test"}
	AddOutputFlags(root)
	leaf := NewLeaf(Leaf{
		Use:              "stream",
		Short:            "Stream data",
		QuietUnsupported: true,
		Run: func(*Invocation) (any, error) {
			ran = true
			return nil, nil
		},
	})
	root.AddCommand(leaf)

	var stdout, stderr bytes.Buffer
	code := Execute(t.Context(), root, []string{"stream", "-q"}, strings.NewReader(""), &stdout, &stderr)
	assert.Equal(t, ExitUsage, code)
	assert.False(t, ran, "Run must not execute when QuietUnsupported rejects -q")
	assert.Contains(t, stderr.String(), "--quiet")
}
