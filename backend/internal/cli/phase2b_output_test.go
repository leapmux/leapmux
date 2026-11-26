package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/pflag"
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

		t.Setenv("NO_COLOR", "")
		t.Setenv("TERM", "dumb")
		t.Setenv("CLICOLOR_FORCE", "1")
		var dumbColor bytes.Buffer
		pDumb := &Printer{Out: &dumbColor, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAuto}
		require.NoError(t, pDumb.Result(items))
		assert.NotContains(t, dumbColor.String(), "\x1b[", "TERM=dumb must suppress auto color")
		assert.False(t, ColorEnabled(&dumbColor, ColorAuto))
		assert.True(t, ColorEnabled(&dumbColor, ColorAlways))
	})

	t.Run("IsTerminalFile and IsTerminalFD return false on pipe and nil file", func(t *testing.T) {
		assert.False(t, IsTerminalFile(nil))
		r, w, err := os.Pipe()
		require.NoError(t, err)
		t.Cleanup(func() {
			_ = r.Close()
			_ = w.Close()
		})
		assert.False(t, IsTerminalFile(r))
		assert.False(t, IsTerminalFD(w.Fd()))
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
				Long:     "Detailed description that compact mode drops.",
				Examples: []Example{{Command: "leapmux agent send a1 hi"}},
				Flags: func(fs *pflag.FlagSet) {
					fs.String("channel", "", "target channel")
					MarkRequired(fs, "channel")
					fs.Bool("wait", false, "wait for reply")
				},
				Run: func(*Invocation) (any, error) { return nil, nil },
			}),
		),
		Group("worker", "Workers",
			NewLeaf(Leaf{Use: "list", Short: "List workers", Run: func(*Invocation) (any, error) { return nil, nil }}),
		),
	)
	root.PersistentFlags().String("hub", "", "hub URL")

	filtered := DescribeFiltered(root, DescribeOptions{Prefix: "agent", Compact: true})
	require.Len(t, filtered.Commands, 2)
	assert.Equal(t, "agent", filtered.Commands[0].Path)
	assert.Equal(t, "agent send", filtered.Commands[1].Path)
	assert.Empty(t, filtered.Commands[1].Description, "compact mode drops long description")
	assert.Empty(t, filtered.Commands[1].Examples, "compact mode omits examples")
	require.Len(t, filtered.Commands[1].Flags, 1, "compact mode keeps required flags and drops optional and global flags")
	assert.Equal(t, "channel", filtered.Commands[1].Flags[0].Name)
	assert.True(t, filtered.Commands[1].Flags[0].Required)

	depth1 := DescribeFiltered(root, DescribeOptions{Depth: 1})
	require.Len(t, depth1.Commands, 2)
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

func TestWriteNDJSON_MapUntaggedEmbeddedAndFallback(t *testing.T) {
	rows := []sampleRow{
		{ID: "1111111111111111", Title: "One", State: "idle"},
		{ID: "2222222222222222", Title: "Two", State: "working"},
	}

	t.Run("map wrapper emits one line per item", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON, Fields: []string{"id"}}
		require.NoError(t, p.Result(map[string]any{"pins": rows}))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.JSONEq(t, `{"id":"1111111111111111"}`, lines[0])
		assert.JSONEq(t, `{"id":"2222222222222222"}`, lines[1])
	})

	t.Run("map wrapper prefers object slice over scalar slice", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON}
		require.NoError(t, p.Result(map[string]any{
			"items":    rows,
			"warnings": []string{"stale"},
		}))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Contains(t, lines[0], `"One"`)
		assert.Contains(t, lines[1], `"Two"`)
	})

	t.Run("untagged struct slice field", func(t *testing.T) {
		type untagged struct {
			Rows []sampleRow
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON}
		require.NoError(t, p.Result(untagged{Rows: rows}))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Contains(t, lines[0], `"One"`)
	})

	t.Run("embedded struct slice field and array field", func(t *testing.T) {
		type innerList struct {
			Rows []sampleRow `json:"rows"`
		}
		type embeddedWrapper struct {
			innerList
			Cursor string `json:"cursor"`
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON}
		require.NoError(t, p.Result(embeddedWrapper{innerList: innerList{Rows: rows}, Cursor: "c1"}))
		lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Contains(t, lines[0], `"One"`)

		out.Reset()
		type arrayWrapper struct {
			Rows [2]sampleRow `json:"rows"`
		}
		require.NoError(t, p.Result(arrayWrapper{Rows: [2]sampleRow{rows[0], rows[1]}}))
		lines = strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Contains(t, lines[1], `"Two"`)
	})

	t.Run("non-slice fallback in writeNDJSONSlice applies fields", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, writeNDJSONSlice(&out, sampleRow{ID: "abc", Title: "secret", State: "idle"}, []string{"id"}))
		assert.JSONEq(t, `{"id":"abc"}`, strings.TrimSpace(out.String()))
	})
}

func TestProjectValueFields_PrecisionSlicesMultiSliceAndOverlap(t *testing.T) {
	t.Run("preserves int64 precision above 2^53", func(t *testing.T) {
		type bigID struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatJSON, Fields: []string{"id"}}
		require.NoError(t, p.Result(bigID{ID: 9007199254740993, Name: "exact"}))
		assert.Contains(t, out.String(), "9007199254740993", "int64 > 2^53 must not lose precision via float64")
		assert.NotContains(t, out.String(), `"name"`)
	})

	t.Run("filters top-level slice", func(t *testing.T) {
		rows := []sampleRow{
			{ID: "1", Title: "Alpha", State: "idle"},
			{ID: "2", Title: "Beta", State: "working"},
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatJSON, Fields: []string{"id"}}
		require.NoError(t, p.Result(rows))
		assert.JSONEq(t, `{"data":[{"id":"1"},{"id":"2"}]}`, out.String())
	})

	t.Run("multi-slice wrapper projects object list and preserves scalar slice", func(t *testing.T) {
		wrapper := struct {
			Items    []sampleRow `json:"items"`
			Warnings []string    `json:"warnings"`
		}{
			Items:    []sampleRow{{ID: "1", Title: "Alpha", State: "idle"}},
			Warnings: []string{"partial"},
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatJSON, Fields: []string{"id"}}
		require.NoError(t, p.Result(wrapper))
		assert.JSONEq(t, `{"data":{"items":[{"id":"1"}],"warnings":["partial"]}}`, out.String())
	})

	t.Run("wrapper whose metadata shares a key with list items still projects list", func(t *testing.T) {
		type taskRow struct {
			ID       string `json:"id" table:"ID"`
			WorkerID string `json:"worker_id" table:"WORKER"`
			Title    string `json:"title" table:"TITLE"`
		}
		wrapper := struct {
			WorkerID string    `json:"worker_id"`
			Items    []taskRow `json:"items"`
		}{
			WorkerID: "w1",
			Items: []taskRow{
				{ID: "t1", WorkerID: "w1", Title: "hidden"},
			},
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatJSON, Fields: []string{"id", "worker_id"}}
		require.NoError(t, p.Result(wrapper))
		assert.JSONEq(t, `{"data":{"worker_id":"w1","items":[{"id":"t1","worker_id":"w1"}]}}`, out.String())

		out.Reset()
		pText := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Fields: []string{"id", "worker_id"}}
		require.NoError(t, pText.Result(wrapper))
		assert.Contains(t, out.String(), "t1")
		assert.NotContains(t, out.String(), "hidden")
	})
}

func TestTableFields_CaseInsensitiveMatch(t *testing.T) {
	t.Run("untagged struct field matched case-insensitively when struct has other table tags", func(t *testing.T) {
		type taggedRow struct {
			ID          string `json:"id" table:"ID"`
			DisplayName string
		}
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Fields: []string{"displayname"}}
		require.NoError(t, p.Result([]taggedRow{{ID: "1", DisplayName: "Alice"}}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, "Alice", strings.TrimSpace(lines[1]))
	})

	t.Run("camelCase map key matched case-insensitively via rawCellValue", func(t *testing.T) {
		// Map entry whose camelCase key holds a list (not a scalar, so derivedColumns omits it)
		// falls through to selectColumns line 911 with lowercased key "foobar".
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Fields: []string{"foobar"}}
		require.NoError(t, p.Result(map[string]any{
			"items": []map[string]any{{"id": "1", "fooBar": []string{"hello", "world"}}},
		}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 2)
		assert.Equal(t, "hello, world", strings.TrimSpace(lines[1]))
	})
}

func TestTable_NarrowTerminalLongHeaderAlignment(t *testing.T) {
	type wideHeaderRow struct {
		ID                string `json:"id" table:"ID,short"`
		ConversationTitle string `json:"title" table:"CONVERSATION_TITLE"`
		State             string `json:"state" table:"STATE"`
	}
	rows := []wideHeaderRow{
		{ID: "1234567890", ConversationTitle: strings.Repeat("a", 50), State: "idle"},
		{ID: "abcdef1234", ConversationTitle: strings.Repeat("b", 50), State: "working"},
	}

	// termWidth=30 is narrower than ID(8)+2+CONVERSATION_TITLE(18)+2+STATE(7)=37,
	// so fitColumnsToWidth tries to shrink column 1 below its 18-rune header width.
	for _, styled := range []bool{false, true} {
		var out bytes.Buffer
		opts := tableOptions{
			termWidth: 30,
			style:     styler{enabled: styled},
		}
		require.NoError(t, writeTableOpts(&out, toNode(reflect.ValueOf(rows)), opts))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 3)

		stripANSI := func(s string) string { return SanitizeTerminalText(s) }
		headerLine := stripANSI(lines[0])
		row1Line := stripANSI(lines[1])
		row2Line := stripANSI(lines[2])

		headerPos := len([]rune(headerLine[:strings.Index(headerLine, "STATE")]))
		row1Pos := len([]rune(row1Line[:strings.Index(row1Line, "idle")]))
		row2Pos := len([]rune(row2Line[:strings.Index(row2Line, "working")]))
		assert.Equal(t, headerPos, row1Pos, "styled=%v: STATE column in row 1 must align with header", styled)
		assert.Equal(t, headerPos, row2Pos, "styled=%v: STATE column in row 2 must align with header", styled)
	}
}

func TestTable_StateAndDimTagsWithStateClassifier(t *testing.T) {
	type styledRow struct {
		ID      string `json:"id" table:"ID,short"`
		RefID   string `json:"ref_id" table:"REF,short"`
		State   string `json:"state" table:"STATE,state"`
		Plain   string `json:"plain" table:"STATUS"`
		Created string `json:"created" table:"CREATED,dim"`
		Date    string `json:"date" table:"DATE"`
	}
	classifier := func(value string) StateTone {
		switch value {
		case "ready":
			return StateToneGreen
		case "working":
			return StateToneYellow
		case "failed":
			return StateToneRed
		case "waiting":
			return StateToneMagenta
		case "info":
			return StateToneCyan
		case "muted":
			return StateToneDim
		default:
			return StateToneNone
		}
	}
	rows := []styledRow{
		{ID: "1234567890", RefID: "abcdef1234", State: "ready", Plain: "ready", Created: "2026-11-26", Date: "2026-11-26"},
		{ID: "2234567890", RefID: "bbcdef1234", State: "working", Plain: "working", Created: "2026-11-26", Date: "2026-11-26"},
		{ID: "3234567890", RefID: "cbcdef1234", State: "failed", Plain: "failed", Created: "2026-11-26", Date: "2026-11-26"},
		{ID: "4234567890", RefID: "dbcdef1234", State: "waiting", Plain: "waiting", Created: "2026-11-26", Date: "2026-11-26"},
		{ID: "5234567890", RefID: "ebcdef1234", State: "info", Plain: "info", Created: "2026-11-26", Date: "2026-11-26"},
		{ID: "6234567890", RefID: "fbcdef1234", State: "muted", Plain: "muted", Created: "2026-11-26", Date: "2026-11-26"},
	}

	t.Run("printer StateClassifier styles ,state and ,dim columns explicitly", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{
			Out:             &out,
			Err:             &bytes.Buffer{},
			Format:          FormatText,
			Color:           ColorAlways,
			StateClassifier: classifier,
		}
		require.NoError(t, p.Result(rows))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 7)

		// Column 0 (,short at index 0) stays plain; column 1 (,short at index 1) dims.
		row1 := strings.Fields(lines[1])
		require.Len(t, row1, 6)
		assert.Equal(t, "12345678", row1[0])
		assert.Equal(t, ansiDim+"abcdef12"+ansiReset, row1[1])
		// ,state column gets StateClassifier ANSI tones; plain STATUS column stays unstyled.
		assert.Equal(t, ansiGreen+"ready"+ansiReset, row1[2])
		assert.Equal(t, "ready", row1[3])
		// ,dim column dims; untagged DATE column stays plain.
		assert.Equal(t, ansiDim+"2026-11-26"+ansiReset, row1[4])
		assert.Equal(t, "2026-11-26", row1[5])

		assert.Equal(t, ansiYellow+"working"+ansiReset, strings.Fields(lines[2])[2])
		assert.Equal(t, "working", strings.Fields(lines[2])[3])
		assert.Equal(t, ansiRed+"failed"+ansiReset, strings.Fields(lines[3])[2])
		assert.Equal(t, "failed", strings.Fields(lines[3])[3])
		assert.Equal(t, ansiBoldMagenta+"waiting"+ansiReset, strings.Fields(lines[4])[2])
		assert.Equal(t, "waiting", strings.Fields(lines[4])[3])
		assert.Equal(t, ansiCyan+"info"+ansiReset, strings.Fields(lines[5])[2])
		assert.Equal(t, "info", strings.Fields(lines[5])[3])
		assert.Equal(t, ansiDim+"muted"+ansiReset, strings.Fields(lines[6])[2])
		assert.Equal(t, "muted", strings.Fields(lines[6])[3])
	})

	t.Run("falls back to SetDefaultStateClassifier when Printer.StateClassifier is nil", func(t *testing.T) {
		prev := getDefaultStateClassifier()
		t.Cleanup(func() { SetDefaultStateClassifier(prev) })
		SetDefaultStateClassifier(classifier)

		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAlways}
		require.NoError(t, p.Result(rows[:1]))
		assert.Contains(t, out.String(), ansiGreen+"ready"+ansiReset)

		// Printer.StateClassifier overrides SetDefaultStateClassifier.
		out.Reset()
		pOverride := &Printer{
			Out:    &out,
			Err:    &bytes.Buffer{},
			Format: FormatText,
			Color:  ColorAlways,
			StateClassifier: func(string) StateTone {
				return StateToneRed
			},
		}
		require.NoError(t, pOverride.Result(rows[:1]))
		assert.Contains(t, out.String(), ansiRed+"ready"+ansiReset)
		assert.NotContains(t, out.String(), ansiGreen)

		// Nil default classifier and nil Printer.StateClassifier leave ,state plain.
		SetDefaultStateClassifier(nil)
		out.Reset()
		pNone := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorAlways}
		require.NoError(t, pNone.Result(rows[:1]))
		assert.Equal(t, "ready", strings.Fields(strings.Split(strings.TrimRight(out.String(), "\n"), "\n")[1])[2])

		// ColorNever suppresses all styling even when classifier is set.
		out.Reset()
		pNever := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatText, Color: ColorNever, StateClassifier: classifier}
		require.NoError(t, pNever.Result(rows[:1]))
		assert.NotContains(t, out.String(), "\x1b[")
	})
}

func TestRenderJSON_NilSlicesEmptyWrappersAndNestedMaps(t *testing.T) {
	t.Run("writeNDJSON handles nil slices, empty wrappers, omitempty, marshalers, and multi-list wrappers", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatNDJSON}

		// Nil slice inside wrapper emits zero lines.
		require.NoError(t, p.Result(struct {
			Items []sampleRow `json:"items"`
		}{Items: nil}))
		assert.Empty(t, out.String())

		// Empty slice inside wrapper emits zero lines.
		require.NoError(t, p.Result(struct {
			Items []sampleRow `json:"items"`
		}{Items: []sampleRow{}}))
		assert.Empty(t, out.String())

		// Empty omitempty slice beside a sibling field falls back to emitting the wrapper object.
		out.Reset()
		require.NoError(t, p.Result(struct {
			Items  []sampleRow `json:"items,omitempty"`
			Cursor string      `json:"cursor"`
		}{Items: nil, Cursor: "c1"}))
		assert.JSONEq(t, `{"cursor":"c1"}`, strings.TrimSpace(out.String()))

		// Two object slices do not have a single primary list, so the whole wrapper prints on one line.
		out.Reset()
		require.NoError(t, p.Result(struct {
			A []sampleRow `json:"a"`
			B []sampleRow `json:"b"`
		}{
			A: []sampleRow{{ID: "1"}},
			B: []sampleRow{{ID: "2"}},
		}))
		assert.Equal(t, 1, strings.Count(out.String(), "\n"))
		assert.Contains(t, out.String(), `"a":[`)
		assert.Contains(t, out.String(), `"b":[`)

		// json.Marshaler bypasses list extraction and prints one line.
		out.Reset()
		require.NoError(t, p.Result(selfEncoding{}))
		assert.JSONEq(t, `{"kind":"custom"}`, strings.TrimSpace(out.String()))

		// Map with nil entry and []any holding maps extracts the object slice.
		out.Reset()
		require.NoError(t, p.Result(map[string]any{
			"empty": nil,
			"tags":  []any{"x", "y"},
			"rows":  []any{map[string]any{"id": "m1", "meta": map[string]any{"k": "v"}}},
		}))
		assert.JSONEq(t, `{"id":"m1","meta":{"k":"v"}}`, strings.TrimSpace(out.String()))
	})

	t.Run("projectValueFields handles empty wrappers, two lists, nested maps, scalars, and blank fields", func(t *testing.T) {
		emptyWrapper := map[string]any{
			"items":  []sampleRow{},
			"cursor": "c1",
		}
		// Non-matching field on empty wrapper projects the empty list and keeps sibling metadata.
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatJSON, Fields: []string{"id"}}
		require.NoError(t, p.Result(emptyWrapper))
		assert.JSONEq(t, `{"data":{"cursor":"c1","items":[]}}`, out.String())

		// Matching top-level field on empty wrapper filters the top-level map.
		out.Reset()
		p.Fields = []string{"cursor"}
		require.NoError(t, p.Result(emptyWrapper))
		assert.JSONEq(t, `{"data":{"cursor":"c1"}}`, out.String())

		// Two non-empty object slices fall back to top-level filtering.
		out.Reset()
		p.Fields = []string{"a"}
		require.NoError(t, p.Result(map[string]any{
			"a": []map[string]any{{"id": "1"}},
			"b": []map[string]any{{"id": "2"}},
		}))
		assert.JSONEq(t, `{"data":{"a":[{"id":"1"}]}}`, out.String())

		// Nested maps inside projected list items remain intact.
		out.Reset()
		p.Fields = []string{"id", "meta"}
		require.NoError(t, p.Result(map[string]any{
			"items": []map[string]any{
				{"id": "1", "secret": "drop", "meta": map[string]any{"env": "prod", "n": 2}},
			},
		}))
		assert.JSONEq(t, `{"data":{"items":[{"id":"1","meta":{"env":"prod","n":2}}]}}`, out.String())

		// Top-level scalar and whitespace-only fields leave value unchanged.
		assert.Equal(t, "hello", projectValueFields("hello", []string{"id"}))
		assert.Equal(t, emptyWrapper, projectValueFields(emptyWrapper, []string{" ", ""}))
	})

	t.Run("nodeToJSONValue converts scalar, object, list, and null nodes", func(t *testing.T) {
		n := node{
			kind: nodeObject,
			fields: []nodeField{
				{key: "name", value: node{kind: nodeScalar, scalar: "alpha"}},
				{key: "items", value: node{kind: nodeList, items: []node{{kind: nodeScalar, scalar: "1"}, {kind: nodeNull}}}},
			},
		}
		assert.Equal(t, map[string]any{
			"name":  "alpha",
			"items": []any{"1", nil},
		}, nodeToJSONValue(n))
	})
}

func TestRenderTable_WidthLimitsHeadersAndTSVEdgeCases(t *testing.T) {
	type edgeRow struct {
		EmptyHeader string `json:"empty_header" table:""`
		Title       string `json:"title" table:"TITLE"`
		State       string `json:"state" table:"STATE"`
	}
	longTitle := strings.Repeat("x", maxCellRunes+15)
	rows := []edgeRow{{EmptyHeader: "v1", Title: longTitle, State: "ready"}}

	t.Run("zero and negative termWidth cap cells at maxCellRunes without shrinking to terminal", func(t *testing.T) {
		for _, width := range []int{0, -10} {
			var out bytes.Buffer
			require.NoError(t, writeTableOpts(&out, toNode(reflect.ValueOf(rows)), tableOptions{termWidth: width}))
			lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
			require.Len(t, lines, 2)
			assert.Contains(t, lines[1], strings.Repeat("x", maxCellRunes-1)+"…", "termWidth=%d", width)
			assert.NotContains(t, lines[1], longTitle, "termWidth=%d", width)
		}
	})

	t.Run("noHeader with narrow termWidth shrinks wide column cleanly", func(t *testing.T) {
		var out bytes.Buffer
		require.NoError(t, writeTableOpts(&out, toNode(reflect.ValueOf(rows)), tableOptions{
			noHeader:  true,
			termWidth: 25,
		}))
		lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
		require.Len(t, lines, 1, "noHeader must omit the header line")
		assert.Contains(t, lines[0], "…")
		assert.LessOrEqual(t, len([]rune(lines[0])), 25)
	})

	t.Run("wide ignores narrow termWidth and prints full value", func(t *testing.T) {
		var out bytes.Buffer
		opts := TableOptions{opts: tableOptions{wide: true, termWidth: 20}}
		require.NoError(t, WriteTableWithOptions(&out, rows, opts))
		assert.Contains(t, out.String(), longTitle)
	})

	t.Run("truncateTo handles non-positive, 1-rune, multi-byte, and exact-length limits", func(t *testing.T) {
		assert.Equal(t, "hello", truncateTo("hello", 0))
		assert.Equal(t, "hello", truncateTo("hello", -3))
		assert.Equal(t, "hello", truncateTo("hello", 5))
		assert.Equal(t, "…", truncateTo("hello", 1))
		assert.Equal(t, "h…", truncateTo("hello", 2))
		assert.Equal(t, "日本…", truncateTo("日本語テスト", 3))
	})

	t.Run("TSV renders scalar lists, single objects with fields, and missing cells", func(t *testing.T) {
		var out bytes.Buffer
		p := &Printer{Out: &out, Err: &bytes.Buffer{}, Format: FormatTSV}
		require.NoError(t, p.Result([]string{"alpha", "beta"}))
		assert.Equal(t, "alpha\nbeta\n", out.String())

		out.Reset()
		p.Fields = []string{"id", "title"}
		require.NoError(t, p.Result(sampleRow{ID: "id1", Title: "hello", State: "idle"}))
		assert.Equal(t, "id\tid1\ntitle\thello\n", out.String())

		out.Reset()
		p.Fields = []string{"id", "missing"}
		require.NoError(t, p.Result([]sampleRow{{ID: "id1"}}))
		assert.Equal(t, "ID\tMISSING\nid1\t-\n", out.String())
	})
}
