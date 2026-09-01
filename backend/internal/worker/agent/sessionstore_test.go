package agent

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/sqlitedb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureEnv builds a Getenv seam over a map, so a reader's path resolution can
// be exercised without touching the process environment. t.Setenv is the
// alternative and it cannot be used here: these tests run in parallel, and
// t.Setenv panics under t.Parallel.
func fixtureEnv(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

// newFixtureDB creates a SQLite database at `path` and applies `ddl`. The
// parent directory is created, so a caller states the whole store layout in one
// path.
func newFixtureDB(t *testing.T, path, ddl string) *sql.DB {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	db, err := sqlitedb.Open(path, sqlitedb.Config{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(ddl)
	require.NoError(t, err)
	return db
}

// writeFixtureFile writes a file, creating its parents.
func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
}

// touchFixture sets a file's modification time, so a test can state the
// recency order the readers sort by rather than depend on write order.
func touchFixture(t *testing.T, path string, at time.Time) {
	t.Helper()
	require.NoError(t, os.Chtimes(path, at, at))
}

// handlesOf reduces a result to its handles, which is what most orderings and
// filters are asserted on.
func handlesOf(sessions []StoredSession) []string {
	out := make([]string, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s.Handle)
	}
	return out
}

func TestSortAndCapSessions(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("orders newest first", func(t *testing.T) {
		t.Parallel()
		got := sortAndCapSessions([]StoredSession{
			{Handle: "old", UpdatedAt: base.Add(-2 * time.Hour)},
			{Handle: "new", UpdatedAt: base},
			{Handle: "mid", UpdatedAt: base.Add(-time.Hour)},
		}, 0)
		assert.Equal(t, []string{"new", "mid", "old"}, handlesOf(got))
	})

	t.Run("breaks a timestamp tie by handle", func(t *testing.T) {
		t.Parallel()
		got := sortAndCapSessions([]StoredSession{
			{Handle: "b", UpdatedAt: base},
			{Handle: "a", UpdatedAt: base},
		}, 0)
		assert.Equal(t, []string{"a", "b"}, handlesOf(got),
			"one stable order, so two calls cannot disagree")
	})

	t.Run("sorts an unknown time last, not to the epoch", func(t *testing.T) {
		t.Parallel()
		got := sortAndCapSessions([]StoredSession{
			{Handle: "unknown"},
			{Handle: "ancient", UpdatedAt: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC)},
		}, 0)
		assert.Equal(t, []string{"ancient", "unknown"}, handlesOf(got))
	})

	t.Run("drops a record with no handle", func(t *testing.T) {
		t.Parallel()
		got := sortAndCapSessions([]StoredSession{
			{Handle: "", Title: "unresumable", UpdatedAt: base},
			{Handle: "  ", UpdatedAt: base},
			{Handle: "real", UpdatedAt: base.Add(-time.Hour)},
		}, 0)
		assert.Equal(t, []string{"real"}, handlesOf(got))
	})

	t.Run("caps to the limit, keeping the newest", func(t *testing.T) {
		t.Parallel()
		got := sortAndCapSessions([]StoredSession{
			{Handle: "a", UpdatedAt: base.Add(-3 * time.Hour)},
			{Handle: "b", UpdatedAt: base.Add(-time.Hour)},
			{Handle: "c", UpdatedAt: base.Add(-2 * time.Hour)},
		}, 2)
		assert.Equal(t, []string{"b", "c"}, handlesOf(got))
	})

	t.Run("handles the empty input", func(t *testing.T) {
		t.Parallel()
		assert.Empty(t, sortAndCapSessions(nil, 10))
	})
}

func TestTrimTitle(t *testing.T) {
	t.Parallel()

	assert.Empty(t, trimTitle("   "))
	assert.Equal(t, "hello", trimTitle("  hello  "))

	// Several stores put the first PROMPT in the title field, and a prompt is
	// arbitrary multi-line user text.
	assert.Equal(t, "first line", trimTitle("first line\nsecond line\nthird"))
	assert.Equal(t, "first line", trimTitle("first line\r\nsecond"))

	long := strings.Repeat("a", maxStoredSessionTitleRunes+50)
	got := trimTitle(long)
	assert.Equal(t, maxStoredSessionTitleRunes+1, len([]rune(got)), "capped, plus the ellipsis")
	assert.True(t, strings.HasSuffix(got, "…"))

	// The cap counts RUNES, so a multi-byte character is never cut into
	// invalid UTF-8.
	multibyte := strings.Repeat("가", maxStoredSessionTitleRunes+10)
	assert.True(t, len(trimTitle(multibyte)) > 0)
	assert.True(t, strings.ToValidUTF8(trimTitle(multibyte), "?") == trimTitle(multibyte))
}

func TestSameDir(t *testing.T) {
	t.Parallel()
	assert.True(t, sameDir("/a/b", "/a/b"))
	assert.True(t, sameDir("/a/b/", "/a/b"))
	assert.True(t, sameDir("/a/./b", "/a/b"))
	assert.False(t, sameDir("/a/b", "/a/c"))
	// An empty side is never a match: a store that recorded no working
	// directory must not be offered under an arbitrary one.
	assert.False(t, sameDir("", "/a/b"))
	assert.False(t, sameDir("/a/b", ""))
}

func TestExpandHome(t *testing.T) {
	t.Parallel()
	assert.Equal(t, filepath.Join("/home/u", ".codex"), expandHome("~/.codex", "/home/u"))
	assert.Equal(t, "/home/u", expandHome("~", "/home/u"))
	assert.Equal(t, "/abs/path", expandHome("/abs/path", "/home/u"))
	// `~user` is not a home reference this expands; leaving it alone is
	// correct, because the stores that write `~` write only the bare form.
	assert.Equal(t, "~other/x", expandHome("~other/x", "/home/u"))
	assert.Equal(t, "~/x", expandHome("~/x", ""))
}

func TestStoredSessionQueryDefaults(t *testing.T) {
	t.Parallel()

	q := StoredSessionQuery{Getenv: fixtureEnv(map[string]string{"FOO": "bar"})}
	assert.Equal(t, "bar", q.env("FOO"))
	assert.Empty(t, q.env("MISSING"))
	assert.Equal(t, DefaultStoredSessionLimit, q.limit())
	assert.Equal(t, 7, StoredSessionQuery{Limit: 7}.limit())

	assert.Equal(t, "/home/u", StoredSessionQuery{HomeDir: "/home/u"}.home())

	// XDG_DATA_HOME wins; otherwise `~/.local/share` on EVERY platform,
	// because the CLIs this serves use the xdg-basedir package rather than the
	// platform-native layout.
	xdg := StoredSessionQuery{HomeDir: "/home/u", Getenv: fixtureEnv(map[string]string{"XDG_DATA_HOME": "/xdg"})}
	assert.Equal(t, "/xdg", xdg.xdgDataHome())
	plain := StoredSessionQuery{HomeDir: "/home/u", Getenv: fixtureEnv(nil)}
	assert.Equal(t, filepath.Join("/home/u", ".local", "share"), plain.xdgDataHome())
}

func TestJSONLHeadAndTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	t.Run("reads every line of a short file from both ends", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "short.jsonl")
		writeFixtureFile(t, path, "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n")

		head, err := jsonlHead(path, jsonlProbeBytes)
		require.NoError(t, err)
		assert.Len(t, head, 3, "a file inside the window keeps its last line")

		tail, err := jsonlTail(path, jsonlProbeBytes)
		require.NoError(t, err)
		assert.Len(t, tail, 3, "a file inside the window keeps its first line")
	})

	t.Run("drops the partial line at the cut", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "cut.jsonl")
		// Three 10-byte lines. A 15-byte window covers the first and half the
		// second from the head, and the last plus half the second from the
		// tail.
		writeFixtureFile(t, path, "{\"i\":\"a\"}\n{\"i\":\"b\"}\n{\"i\":\"c\"}\n")

		head, err := jsonlHead(path, 15)
		require.NoError(t, err)
		require.Len(t, head, 1)
		assert.JSONEq(t, `{"i":"a"}`, string(head[0]))

		tail, err := jsonlTail(path, 15)
		require.NoError(t, err)
		require.Len(t, tail, 1)
		assert.JSONEq(t, `{"i":"c"}`, string(tail[0]))
	})

	t.Run("skips blank lines", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "blank.jsonl")
		writeFixtureFile(t, path, "{\"a\":1}\n\n\n{\"b\":2}\n")
		head, err := jsonlHead(path, jsonlProbeBytes)
		require.NoError(t, err)
		assert.Len(t, head, 2)
	})

	t.Run("reports a missing file", func(t *testing.T) {
		t.Parallel()
		_, err := jsonlHead(filepath.Join(dir, "absent.jsonl"), jsonlProbeBytes)
		assert.Error(t, err)
		_, err = jsonlTail(filepath.Join(dir, "absent.jsonl"), jsonlProbeBytes)
		assert.Error(t, err)
	})

	t.Run("handles an empty file", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(dir, "empty.jsonl")
		writeFixtureFile(t, path, "")
		head, err := jsonlHead(path, jsonlProbeBytes)
		require.NoError(t, err)
		assert.Empty(t, head)
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
		writeFixtureFile(t, path, "{}\n")
		touchFixture(t, path, base.Add(-age))
	}
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "subdir"), 0o755))

	keepJSONL := func(entry os.DirEntry) bool {
		return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
	}

	entries, err := newestEntries(dir, 0, keepJSONL)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	assert.Equal(t, []string{"new.jsonl", "mid.jsonl", "old.jsonl"}, names,
		"newest first, and the filter drops the .txt and the directory")

	capped, err := newestEntries(dir, 2, keepJSONL)
	require.NoError(t, err)
	assert.Len(t, capped, 2)
	assert.Equal(t, "new.jsonl", capped[0].Name)

	_, err = newestEntries(filepath.Join(dir, "absent"), 0, keepJSONL)
	assert.ErrorIs(t, err, errSessionStoreAbsent,
		"an absent directory is the normal state for a CLI never run, not a failure")
}

func TestOpenSessionStoreDB_AbsentIsNotAFailure(t *testing.T) {
	t.Parallel()
	_, err := openSessionStoreDB(filepath.Join(t.TempDir(), "nope.db"))
	assert.ErrorIs(t, err, errSessionStoreAbsent)

	_, err = openSessionStoreDB("")
	assert.ErrorIs(t, err, errSessionStoreAbsent)
}

func TestParseRFC3339(t *testing.T) {
	t.Parallel()
	assert.Equal(t,
		time.Date(2026, 8, 27, 20, 11, 38, 728000000, time.UTC),
		parseRFC3339("2026-08-27T20:11:38.728Z"))
	assert.True(t, parseRFC3339("").IsZero())
	assert.True(t, parseRFC3339("not a time").IsZero())
}

func TestEpochMillis(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.UnixMilli(1788105082914).UTC(), epochMillis(1788105082914))
	// Zero and negative mean "the store said nothing", which must not become
	// the epoch and sort ahead of a real timestamp.
	assert.True(t, epochMillis(0).IsZero())
	assert.True(t, epochMillis(-1).IsZero())
}
