package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sampleRow struct {
	ID    string `json:"id" table:"ID,short"`
	Title string `json:"title" table:"TITLE"`
	State string `json:"state" table:"STATE"`
}

func TestPrinter_FormatsAndFlags(t *testing.T) {
	items := struct {
		Items []sampleRow `json:"items"`
	}{
		Items: []sampleRow{
			{ID: "1234567890abcdef", Title: "Alpha task", State: "working"},
			{ID: "fedcba0987654321", Title: "Beta task", State: "idle"},
		},
	}

	t.Run("format tsv with no-header and fields", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatTSV, NoHeader: true, Fields: []string{"id", "state"}}
		require.NoError(t, p.Result(items))
		assert.Equal(t, "12345678\tworking\nfedcba09\tidle\n", out.String())
	})

	t.Run("format tsv with header", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatTSV}
		require.NoError(t, p.Result(items))
		assert.Equal(t, "ID\tTITLE\tSTATE\n12345678\tAlpha task\tworking\nfedcba09\tBeta task\tidle\n", out.String())
	})

	t.Run("format ndjson emits unwrapped lines with field projection", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON, Fields: []string{"id", "state"}}
		require.NoError(t, p.Result(items))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		var first map[string]any
		require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
		assert.Equal(t, map[string]any{"id": "1234567890abcdef", "state": "working"}, first)
	})

	t.Run("color always emits ANSI codes; color never and NO_COLOR emit zero ANSI", func(t *testing.T) {
		var colored bytes.Buffer
		p := &Printer{Out: &colored, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAlways}
		require.NoError(t, p.Result(items))
		assert.Contains(t, colored.String(), "\x1b[")

		var plain bytes.Buffer
		pPlain := &Printer{Out: &plain, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorNever}
		require.NoError(t, pPlain.Result(items))
		assert.NotContains(t, plain.String(), "\x1b[")

		t.Setenv("NO_COLOR", "1")
		t.Setenv("CLICOLOR_FORCE", "1")
		var noColor bytes.Buffer
		pAuto := &Printer{Out: &noColor, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAuto}
		require.NoError(t, pAuto.Result(items))
		assert.NotContains(t, noColor.String(), "\x1b[", "NO_COLOR must suppress auto color even with CLICOLOR_FORCE")
	})

	t.Run("spinner is a silent no-op on non-TTY buffers", func(t *testing.T) {
		var errBuf bytes.Buffer
		p := &Printer{Out: &bytes.Buffer{}, Err: &errBuf, Format: FormatText}
		spin := StartSpinner(p, "waiting for agent")
		spin.Update("still working")
		spin.Stop()
		spin.Stop() // idempotent
		assert.Empty(t, errBuf.String(), "non-TTY stderr must receive zero spinner bytes")
	})
}

func TestDestructiveTypoGuard(t *testing.T) {
	root := Group("leapmux", "Root",
		Group("workspace", "Workspaces",
			NewLeaf(Leaf{Use: "list", Short: "List workspaces", Run: func(*Invocation) (any, error) { return nil, nil }}),
			NewLeaf(Leaf{Use: "delete <workspace>", Short: "Delete workspace", Mutates: true, Destructive: true, Run: func(*Invocation) (any, error) { return nil, nil }}),
		),
	)

	// Edit distance 2 ("delat" vs "delete") must not suggest destructive command.
	var out, errBuf bytes.Buffer
	code := Execute(context.Background(), root, []string{"workspace", "delat"}, strings.NewReader(""), &out, &errBuf)
	assert.Equal(t, ExitUsage, code)
	assert.NotContains(t, errBuf.String(), `did you mean delete`, "destructive command must not be suggested at edit distance 2")

	// Edit distance 1 ("deleta" -> "delete", not a prefix) is allowed.
	out.Reset()
	errBuf.Reset()
	code = Execute(context.Background(), root, []string{"workspace", "deleta"}, strings.NewReader(""), &out, &errBuf)
	assert.Equal(t, ExitUsage, code)
	assert.Contains(t, errBuf.String(), `did you mean delete?`)
}

func TestDescribeFiltered(t *testing.T) {
	root := Group("leapmux", "Root",
		Group("agent", "Agents",
			NewLeaf(Leaf{
				Use:      "send <agent> [text]",
				Short:    "Send message",
				Examples: []Example{{Command: "leapmux agent send a1 hi"}},
				Run:      func(*Invocation) (any, error) { return nil, nil },
			}),
		),
		Group("worker", "Workers",
			NewLeaf(Leaf{Use: "list", Short: "List workers", Run: func(*Invocation) (any, error) { return nil, nil }}),
		),
	)

	filtered := DescribeFiltered(root, DescribeOptions{Prefix: "agent", Compact: true})
	require.Len(t, filtered.Commands, 2)
	assert.Equal(t, "agent", filtered.Commands[0].Path)
	assert.Equal(t, "agent send", filtered.Commands[1].Path)
	assert.Empty(t, filtered.Commands[1].Examples, "compact mode omits examples")
	assert.Empty(t, filtered.Commands[1].Flags, "compact mode omits flags")

	depth1 := DescribeFiltered(root, DescribeOptions{Depth: 1})
	for _, c := range depth1.Commands {
		assert.NotContains(t, c.Path, " ", "depth 1 returns only top-level commands")
	}
}

func TestCommandGroupsAndNextHintsInHelp(t *testing.T) {
	ag := NewLeaf(Leaf{
		Use:   "open",
		Short: "Open agent",
		Next:  []string{"leapmux agent send <id> hi"},
		Run:   func(*Invocation) (any, error) { return nil, nil },
	})
	SetCommandGroup(ag, "Daily Work")
	root := Group("leapmux", "Root", ag)

	var out, errBuf bytes.Buffer
	code := Execute(context.Background(), root, []string{"--help"}, strings.NewReader(""), &out, &errBuf)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, out.String(), "Daily Work:\n  open  ")

	out.Reset()
	code = Execute(context.Background(), root, []string{"open", "--help"}, strings.NewReader(""), &out, &errBuf)
	assert.Equal(t, ExitOK, code)
	assert.Contains(t, out.String(), "Next:\n  leapmux agent send <id> hi")
}
