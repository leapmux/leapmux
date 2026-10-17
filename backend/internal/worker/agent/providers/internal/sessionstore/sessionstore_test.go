package sessionstore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrimTitle(t *testing.T) {
	t.Parallel()

	assert.Empty(t, TrimTitle("   "))
	assert.Equal(t, "hello", TrimTitle("  hello  "))

	// Several stores put the first PROMPT in the title field, and a prompt is
	// arbitrary multi-line user text.
	assert.Equal(t, "first line", TrimTitle("first line\nsecond line\nthird"))
	assert.Equal(t, "first line", TrimTitle("first line\r\nsecond"))

	long := strings.Repeat("a", maxStoredSessionTitleRunes+50)
	got := TrimTitle(long)
	assert.Equal(t, maxStoredSessionTitleRunes+1, len([]rune(got)), "capped, plus the ellipsis")
	assert.True(t, strings.HasSuffix(got, "…"))

	// The cap counts RUNES, so a multi-byte character is never cut into
	// invalid UTF-8.
	multibyte := strings.Repeat("가", maxStoredSessionTitleRunes+10)
	assert.True(t, len(TrimTitle(multibyte)) > 0)
	assert.True(t, strings.ToValidUTF8(TrimTitle(multibyte), "?") == TrimTitle(multibyte))
}

func TestSameDir(t *testing.T) {
	t.Parallel()
	assert.True(t, SameDir("/a/b", "/a/b"))
	assert.True(t, SameDir("/a/b/", "/a/b"))
	assert.True(t, SameDir("/a/./b", "/a/b"))
	assert.False(t, SameDir("/a/b", "/a/c"))
	// An empty path never matches a directory. pathutil.SamePath cleans an empty path to a dot and
	// considers two empty paths equal. SameDir must reject them before delegation.
	assert.False(t, SameDir("", "/a/b"))
	assert.False(t, SameDir("/a/b", ""))
	assert.False(t, SameDir("", ""))
	if runtime.GOOS == "windows" {
		// Delegating picks up the case fold. These stores record the CLI's own
		// process.cwd(), which need not agree with the worker on case.
		assert.True(t, SameDir(`C:\Users\x`, `c:\users\x`))
	}
}

func TestFirstNonBlank(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "first", FirstNonBlank("first", "second"))
	assert.Equal(t, "second", FirstNonBlank("", "second"))
	// Whitespace is blank, so a store that wrote a space-only title falls
	// through to the next candidate rather than producing an empty-looking row.
	assert.Equal(t, "third", FirstNonBlank("", "   \t", "third"))
	assert.Empty(t, FirstNonBlank())
	assert.Empty(t, FirstNonBlank("", " ", "\n"))
}

func TestContentBlockText(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "plain", ContentBlockText([]byte(`"plain"`)))
	assert.Equal(t, "block", ContentBlockText([]byte(`[{"type":"text","text":"block"}]`)))
	assert.Equal(t, "second", ContentBlockText([]byte(`[{"type":"text","text":"  "},{"type":"text","text":"second"}]`)))
	assert.Empty(t, ContentBlockText([]byte(`[{"type":"tool_result","content":"out"}]`)))
	assert.Empty(t, ContentBlockText(nil))
	assert.Empty(t, ContentBlockText([]byte(`{"unexpected":"shape"}`)))
	assert.Equal(t, "kept", ContentBlockText([]byte(`["stray", {"type":"text","text":"kept"}]`)),
		"an item that is not a block does not hide the blocks after it")
}

func TestContentBlocks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		content string
		want    []ContentBlock
	}{
		{name: "a plain string is one text block", content: `"hello"`, want: []ContentBlock{{Type: "text", Text: "hello"}}},
		{name: "null is no content", content: `null`},
		{name: "a null array item is not a block", content: `[null,{"type":"text","text":"kept"}]`, want: []ContentBlock{{Type: "text", Text: "kept"}}},
		{name: "an empty string is one empty text block", content: `""`, want: []ContentBlock{{Type: "text"}}},
		{
			name:    "an array keeps its blocks in order",
			content: `[{"type":"image","source":{}},{"type":"text","text":"a"},{"type":"tool_result","content":"out"}]`,
			want:    []ContentBlock{{Type: "image"}, {Type: "text", Text: "a"}, {Type: "tool_result"}},
		},
		{
			name:    "an item that is not a block object is skipped",
			content: `["stray", 7, {"type":"text","text":"a"}]`,
			want:    []ContentBlock{{Type: "text", Text: "a"}},
		},
		{
			name:    "a block whose text is not a string is skipped",
			content: `[{"type":"text","text":{"nested":true}},{"type":"text","text":"a"}]`,
			want:    []ContentBlock{{Type: "text", Text: "a"}},
		},
		{name: "an empty array has no blocks", content: `[]`, want: []ContentBlock{}},
		{name: "an object is no content", content: `{"type":"text","text":"a"}`},
		{name: "a number is no content", content: `7`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, ContentBlocks([]byte(tc.content)))
		})
	}
	assert.Nil(t, ContentBlocks(nil), "absent content has no blocks")
}

func TestJSONLHeadAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("keeps a complete first record at the tail window", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "tail-boundary.jsonl")
		agenttest.WriteFixtureFile(t, path, "{\"i\":\"a\"}\n{\"i\":\"b\"}\n")
		lines, err := JSONLTail(path, 10)
		require.NoError(t, err)
		assert.Equal(t, [][]byte{[]byte(`{"i":"b"}`)}, lines)
	})

	t.Run("reads every line of a short file from both ends", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "short.jsonl")
		agenttest.WriteFixtureFile(t, path, "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n")

		head, atEOF, err := JSONLHead(path, JSONLProbeBytes)
		require.NoError(t, err)
		assert.Len(t, head, 3, "a file inside the window keeps its last line")
		assert.True(t, atEOF, "a file inside the window needs no tail read")

		tail, err := JSONLTail(path, JSONLProbeBytes)
		require.NoError(t, err)
		assert.Len(t, tail, 3, "a file inside the window keeps its first line")
	})

	t.Run("drops the partial line at the cut", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "cut.jsonl")
		// Three 10-byte lines. A 15-byte window covers the first and half the
		// second from the head, and the last plus half the second from the
		// tail.
		agenttest.WriteFixtureFile(t, path, "{\"i\":\"a\"}\n{\"i\":\"b\"}\n{\"i\":\"c\"}\n")

		head, atEOF, err := JSONLHead(path, 15)
		require.NoError(t, err)
		require.Len(t, head, 1)
		assert.JSONEq(t, `{"i":"a"}`, string(head[0]))
		assert.False(t, atEOF, "a truncated window must ask for the tail")

		tail, err := JSONLTail(path, 15)
		require.NoError(t, err)
		require.Len(t, tail, 1)
		assert.JSONEq(t, `{"i":"c"}`, string(tail[0]))
	})

	// io.ReadFull cannot distinguish an exact fit from a longer file through its count and error
	// alone. Both return the full count and nil error. A check against only the count would discard
	// a complete last line.
	t.Run("keeps the last line of a file that ends exactly at the window", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "exact.jsonl")
		// 29 bytes, no trailing newline: every line is complete and the file
		// ends precisely where the window does.
		content := "{\"i\":\"a\"}\n{\"i\":\"b\"}\n{\"i\":\"c\"}"
		require.Len(t, content, 29)
		agenttest.WriteFixtureFile(t, path, content)

		head, atEOF, err := JSONLHead(path, 29)
		require.NoError(t, err)
		assert.Len(t, head, 3, "the file ended at the window, so nothing was cut")
		assert.True(t, atEOF)
	})

	// The same boundary, at the size that loses the WHOLE file: a single
	// unterminated record. The caller drops a session whose head window is
	// empty, so this took the session's cwd and its identity with it.
	t.Run("keeps a lone record that ends exactly at the window", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "exact-single.jsonl")
		content := `{"cwd":"/repo"}`
		agenttest.WriteFixtureFile(t, path, content)

		head, atEOF, err := JSONLHead(path, int64(len(content)))
		require.NoError(t, err)
		require.Len(t, head, 1, "a session must not vanish because it fits exactly")
		assert.JSONEq(t, content, string(head[0]))
		assert.True(t, atEOF)
	})

	t.Run("drops the partial line of a file one byte past the window", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "over.jsonl")
		agenttest.WriteFixtureFile(t, path, "{\"i\":\"a\"}\n{\"i\":\"b\"}")

		head, atEOF, err := JSONLHead(path, 10)
		require.NoError(t, err)
		require.Len(t, head, 1)
		assert.JSONEq(t, `{"i":"a"}`, string(head[0]))
		assert.False(t, atEOF)
	})

	t.Run("skips blank lines", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "blank.jsonl")
		agenttest.WriteFixtureFile(t, path, "{\"a\":1}\n\n\n{\"b\":2}\n")
		head, _, err := JSONLHead(path, JSONLProbeBytes)
		require.NoError(t, err)
		assert.Len(t, head, 2)
	})

	t.Run("reports a missing file", func(t *testing.T) {
		t.Parallel()
		_, _, err := JSONLHead(filepath.Join(dir, "absent.jsonl"), JSONLProbeBytes)
		assert.Error(t, err)
		_, err = JSONLTail(filepath.Join(dir, "absent.jsonl"), JSONLProbeBytes)
		assert.Error(t, err)
	})

	t.Run("handles an empty file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "empty.jsonl")
		agenttest.WriteFixtureFile(t, path, "")
		head, atEOF, err := JSONLHead(path, JSONLProbeBytes)
		require.NoError(t, err)
		assert.Empty(t, head)
		assert.True(t, atEOF)
	})
}

func TestNewestEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for name, age := range map[string]time.Duration{
		"old.jsonl": 3 * time.Hour,
		"mid.jsonl": 2 * time.Hour,
		"new.jsonl": time.Hour,
		"skip.txt":  0,
	} {
		path := filepath.Join(dir, name)
		agenttest.WriteFixtureFile(t, path, "{}\n")
		agenttest.TouchFixture(t, path, base.Add(-age))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir"), 0o755))

	keepJSONL := func(entry os.DirEntry) bool {
		return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
	}

	entries, err := NewestEntries(dir, 0, EntryItself(keepJSONL))
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"new.jsonl", "mid.jsonl", "old.jsonl"}, names,
		"newest first, and the filter drops the .txt and the directory")

	capped, err := NewestEntries(dir, 2, EntryItself(keepJSONL))
	require.NoError(t, err)
	assert.Len(t, capped, 2)
	assert.Equal(t, "new.jsonl", capped[0].Name)

	_, err = NewestEntries(filepath.Join(dir, "absent"), 0, EntryItself(keepJSONL))
	assert.ErrorIs(t, err, ErrAbsent,
		"an absent directory is the normal state for a CLI never run, not a failure")
}

// The scan limit must apply after ordering by modification time. os.ReadDir sorts filenames, and
// session filenames can hold UUIDs. A cut during that scan can discard newer sessions. One real
// Claude directory holds 3905 transcripts and loses six of its ten newest sessions that way.
func TestNewestEntries_CapsAfterOrdering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// Filename order opposes timestamp order in this fixture. A cut before sorting therefore cannot
	// return the newest records.
	total := storedSessionScanCap + 5
	for i := range total {
		path := filepath.Join(dir, fmt.Sprintf("%05d.jsonl", i))
		agenttest.WriteFixtureFile(t, path, "{}\n")
		agenttest.TouchFixture(t, path, base.Add(time.Duration(i)*time.Minute))
	}

	keepJSONL := func(entry os.DirEntry) bool {
		return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
	}
	entries, err := NewestEntries(dir, 3, EntryItself(keepJSONL))
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, []string{fmt.Sprintf("%05d.jsonl", total-1), fmt.Sprintf("%05d.jsonl", total-2), fmt.Sprintf("%05d.jsonl", total-3)},
		[]string{entries[0].Name, entries[1].Name, entries[2].Name},
		"the three newest by modification time, not the first three by name")

	// The cap itself still holds: an uncapped walk of a directory past the scan
	// cap returns exactly the cap, and returns the NEWEST that many.
	all, err := NewestEntries(dir, 0, EntryItself(keepJSONL))
	require.NoError(t, err)
	assert.Len(t, all, storedSessionScanCap)
	assert.Equal(t, fmt.Sprintf("%05d.jsonl", total-1), all[0].Name)
}

// The cut preserves each complete timestamp group. Filename order supplies no recency inside a
// tie. A partial group could omit the newest content timestamp.
func TestSortAndCapEntries_KeepsATieWhole(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	found := []Entry{
		{Name: "a", ModTime: base},
		{Name: "b", ModTime: base},
		{Name: "c", ModTime: base},
		{Name: "d", ModTime: base.Add(-time.Minute)},
	}
	kept := SortAndCapEntries(found, 2)
	names := make([]string, 0, len(kept))
	for _, entry := range kept {
		names = append(names, entry.Name)
	}
	assert.Equal(t, []string{"a", "b", "c"}, names)
}

// NamedFileInside derives each directory candidate timestamp from a file inside it. A read-only
// SQLite open creates the -shm sidecar and changes the directory time. The directory time would
// then report when LeapMux inspected the store.
func TestNamedFileInside_TimesByTheInnerFile(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	for name, inner := range map[string]time.Duration{
		"sess-new": time.Hour,
		"sess-old": 48 * time.Hour,
	} {
		sessionDir := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(sessionDir, 0o755))
		path := filepath.Join(sessionDir, "store.db")
		agenttest.WriteFixtureFile(t, path, "x")
		agenttest.TouchFixture(t, path, base.Add(-inner))
	}
	// The OLDER session's directory is stamped newest, which is what a store
	// migration and a read-only open both do.
	agenttest.TouchFixture(t, filepath.Join(root, "sess-old"), base.Add(time.Hour))

	// A directory holding no `store.db` is not a session.
	require.NoError(t, os.MkdirAll(filepath.Join(root, "not-a-session"), 0o755))

	entries, err := NewestEntries(root, 0, NamedFileInside("store.db"))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.Equal(t, []string{"sess-new", "sess-old"},
		[]string{entries[0].Name, entries[1].Name},
		"ordered by the inner file, although the directory times say the opposite")
	assert.Equal(t, filepath.Join(root, "sess-new"), entries[0].Path,
		"the Path is the session directory, which the caller reads more out of")
}

func TestCollectStoredSessions(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	entries := []Entry{
		{Name: "a", ModTime: base},
		{Name: "b", ModTime: base.Add(-time.Minute)},
		{Name: "c", ModTime: base.Add(-2 * time.Minute)},
		{Name: "d", ModTime: base.Add(-3 * time.Minute)},
	}
	accept := func(e Entry) (agent.StoredSession, bool) { return agent.StoredSession{Handle: e.Name}, true }

	for _, limit := range []int{0, -1, -int(^uint(0)>>1) - 1} {
		t.Run(fmt.Sprintf("reads nothing for limit %d", limit), func(t *testing.T) {
			t.Parallel()
			for _, input := range [][]Entry{nil, entries} {
				reads := 0
				var got []agent.StoredSession
				require.NotPanics(t, func() {
					got = Collect(t.Context(), input, limit, func(e Entry) (agent.StoredSession, bool) {
						reads++
						return accept(e)
					})
				})
				assert.NotNil(t, got)
				assert.Empty(t, got)
				assert.Zero(t, reads)
			}
		})
	}

	t.Run("stops reading at the limit rather than filtering afterwards", func(t *testing.T) {
		t.Parallel()
		reads := 0
		counted := func(e Entry) (agent.StoredSession, bool) {
			reads++
			return accept(e)
		}
		got := Collect(t.Context(), entries, 2, counted)
		assert.Equal(t, []string{"a", "b"}, agenttest.Handles(got))
		assert.Equal(t, 2, reads,
			"an uncapped walk is only affordable because the READ stops, not the result")
	})

	// Filename order supplies no recency inside a timestamp tie. Read the complete group before
	// selecting the newest content timestamps.
	t.Run("reads a timestamp tie whole", func(t *testing.T) {
		t.Parallel()
		tied := []Entry{
			{Name: "a", ModTime: base},
			{Name: "b", ModTime: base},
			{Name: "c", ModTime: base},
		}
		reads := 0
		counted := func(e Entry) (agent.StoredSession, bool) {
			reads++
			session, ok := accept(e)
			if e.Name == "c" {
				session.UpdatedAt = base
			}
			return session, ok
		}
		got := Collect(t.Context(), tied, 2, counted)
		assert.Equal(t, []string{"c", "a"}, agenttest.Handles(got))
		assert.Equal(t, 3, reads, "the tie group carries no recency order to cut against")
	})

	// Rejected candidates must not consume the accepted-session budget. Another directory or a
	// subagent can own those records. Otherwise, a store could report no sessions for a directory
	// that does have sessions.
	t.Run("a refused candidate does not consume the budget", func(t *testing.T) {
		t.Parallel()
		skipB := func(e Entry) (agent.StoredSession, bool) {
			if e.Name == "b" {
				return agent.StoredSession{}, false
			}
			return accept(e)
		}
		got := Collect(t.Context(), entries, 2, skipB)
		assert.Equal(t, []string{"a", "c"}, agenttest.Handles(got))
	})

	// Each read opens a file or a database, so a dismissed dialog has to be
	// observed BETWEEN candidates and not only inside one read.
	t.Run("opens nothing once the context is cancelled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		reads := 0
		counted := func(e Entry) (agent.StoredSession, bool) {
			reads++
			return accept(e)
		}
		assert.Empty(t, Collect(ctx, entries, 4, counted))
		assert.Zero(t, reads)
	})
}

func TestCollectManyPreservesTheBoundaryGroup(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	entries := []Entry{{Name: "empty", ModTime: stamp}, {Name: "batch", ModTime: stamp}, {Name: "new", ModTime: stamp}, {Name: "older", ModTime: stamp.Add(-time.Hour)}}
	var reads []string
	got := CollectMany(t.Context(), entries, 2, func(entry Entry) []agent.StoredSession {
		reads = append(reads, entry.Name)
		switch entry.Name {
		case "empty":
			return nil
		case "batch":
			return []agent.StoredSession{{Handle: "old", UpdatedAt: stamp}, {Handle: "oldest", UpdatedAt: stamp.Add(-time.Hour)}}
		default:
			return []agent.StoredSession{{Handle: entry.Name, UpdatedAt: stamp.Add(time.Hour)}}
		}
	})
	assert.Equal(t, []string{"empty", "batch", "new"}, reads)
	assert.Equal(t, []string{"new", "old"}, agenttest.Handles(got))

	ctx, cancel := context.WithCancel(t.Context())
	reads = nil
	got = CollectMany(ctx, entries, 2, func(entry Entry) []agent.StoredSession {
		reads = append(reads, entry.Name)
		cancel()
		return []agent.StoredSession{{Handle: "first"}}
	})
	assert.Equal(t, []string{"empty"}, reads)
	assert.Equal(t, []string{"first"}, agenttest.Handles(got))
}

func TestCollectLimitsRetainedSessionsWithinTimestampTie(t *testing.T) {
	t.Parallel()
	stamp := time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC)
	entries := make([]Entry, 10000)
	for i := range entries {
		entries[i] = Entry{Name: fmt.Sprintf("%05d", i), ModTime: stamp}
	}
	reads := 0
	got := Collect(t.Context(), entries, 2, func(entry Entry) (agent.StoredSession, bool) {
		reads++
		return agent.StoredSession{Handle: entry.Name, UpdatedAt: stamp.Add(time.Duration(reads))}, true
	})
	assert.Equal(t, len(entries), reads)
	assert.Equal(t, []string{"09999", "09998"}, agenttest.Handles(got))
	assert.LessOrEqual(t, cap(got), 2)
}

func TestOpenSessionStoreDB_AbsentIsNotAFailure(t *testing.T) {
	t.Parallel()
	_, err := OpenDB(t.Context(), filepath.Join(t.TempDir(), "nope.db"))
	assert.ErrorIs(t, err, ErrAbsent)

	_, err = OpenDB(t.Context(), "")
	assert.ErrorIs(t, err, ErrAbsent)
}

func TestParseRFC3339(t *testing.T) {
	t.Parallel()
	assert.Equal(t,
		time.Date(2026, 8, 27, 20, 11, 38, 728000000, time.UTC),
		ParseRFC3339("2026-08-27T20:11:38.728Z"))
	assert.True(t, ParseRFC3339("").IsZero())
	assert.True(t, ParseRFC3339("not a time").IsZero())
}

func TestEpochMillis(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.UnixMilli(1788105082914).UTC(), EpochMillis(1788105082914))
	// Zero and negative mean "the store said nothing", which must not become
	// the epoch and sort ahead of a real timestamp.
	assert.True(t, EpochMillis(0).IsZero())
	assert.True(t, EpochMillis(-1).IsZero())
}
