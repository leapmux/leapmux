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
	// An empty side is never a match: a store that recorded no working
	// directory must not be offered under an arbitrary one. pathutil.SamePath
	// cleans "" to "." and answers TRUE for two empty paths, so the guard in
	// SameDir is what produces this answer and not the delegate.
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
}

func TestJSONLHeadAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

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

	// The boundary io.ReadFull cannot report on its own. A window of exactly
	// the file's size returns the same count and the same nil error as a window
	// that filled from a longer file, so a check against the window size alone
	// treated a complete last line as a partial one and dropped it.
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

// The scan cap must apply to the ORDERED list. os.ReadDir returns a directory
// sorted by file name, and these stores name a session with a UUID, so a cap
// taken during the walk keeps the lexicographically first entries -- which is
// uncorrelated with recency. One real Claude directory holds 3905 transcripts
// and loses six of its ten newest sessions that way.
func TestNewestEntries_CapsAfterOrdering(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// The name order is the REVERSE of the time order, so a cap taken during
	// the walk and a cap taken after the sort cannot agree.
	total := storedSessionScanCap + 5
	for i := range total {
		path := filepath.Join(dir, fmt.Sprintf("%05d.jsonl", i))
		agenttest.WriteFixtureFile(t, path, "{}\n")
		agenttest.TouchFixture(t, path, base.Add(-time.Duration(i)*time.Minute))
	}

	keepJSONL := func(entry os.DirEntry) bool {
		return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
	}
	entries, err := NewestEntries(dir, 3, EntryItself(keepJSONL))
	require.NoError(t, err)
	require.Len(t, entries, 3)
	assert.Equal(t, []string{"00000.jsonl", "00001.jsonl", "00002.jsonl"},
		[]string{entries[0].Name, entries[1].Name, entries[2].Name},
		"the three newest by modification time, not the first three by name")

	// The cap itself still holds: an uncapped walk of a directory past the scan
	// cap returns exactly the cap, and returns the NEWEST that many.
	all, err := NewestEntries(dir, 0, EntryItself(keepJSONL))
	require.NoError(t, err)
	assert.Len(t, all, storedSessionScanCap)
	assert.Equal(t, "00000.jsonl", all[0].Name)
}

// NamedFileInside times a session DIRECTORY by a file inside it. The directory's
// own time is not the session's: it moves when any program adds a file, and a
// read-only SQLite open adds the `-shm` sidecar, so a walk timed that way
// reports the moment LeapMux looked.
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
	entries := []Entry{{Name: "a"}, {Name: "b"}, {Name: "c"}, {Name: "d"}}
	accept := func(e Entry) (agent.StoredSession, bool) { return agent.StoredSession{Handle: e.Name}, true }

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

	// A candidate the reader refuses -- another directory's session, a subagent
	// transcript -- must not spend the budget, or a store whose newest entries
	// all belong elsewhere reports that this directory has nothing.
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
