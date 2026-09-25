package cline

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestParseHistoryReadsTheListAndIgnoresWhatFollows(t *testing.T) {
	t.Parallel()
	entries, err := parseHistory([]byte(`[{"sessionId":"s1","cwd":"/w","isSubagent":false}]` + "\nA note after the list.\n"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "s1", entries[0].SessionID)
	_, err = parseHistory([]byte(`not json`))
	require.Error(t, err)
}

func TestDirectorySessionsKeepTheRootsOfTheDirectory(t *testing.T) {
	t.Parallel()
	dir := resolvePath(t.TempDir())
	other := resolvePath(t.TempDir())
	entries := []historyEntry{
		{SessionID: "old", Cwd: dir, Prompt: `<user_input mode="act">Old one</user_input>`, UpdatedAt: "2026-09-20T00:00:00Z"},
		{SessionID: "new", Cwd: dir, Metadata: historyMetadata{Title: "Fix the build"}, UpdatedAt: "2026-09-22T00:00:00Z"},
		{SessionID: "child", Cwd: dir, IsSubagent: true, UpdatedAt: "2026-09-23T00:00:00Z"},
		{SessionID: "there", Cwd: other, UpdatedAt: "2026-09-24T00:00:00Z"},
		{SessionID: "", Cwd: dir},
	}
	sessions := directorySessions(entries, dir, 10)
	require.Len(t, sessions, 2)
	assert.Equal(t, "new", sessions[0].Handle, "newest first")
	assert.Equal(t, "Fix the build", sessions[0].Title)
	assert.Equal(t, "old", sessions[1].Handle)
	assert.Equal(t, "Old one", sessions[1].Title, "the stored wrapper leaves the title")
	assert.Len(t, directorySessions(entries, dir, 1), 1, "the limit holds")
}

func TestSessionTitleIsOneCappedLine(t *testing.T) {
	t.Parallel()
	// A stored first prompt is the user's own text: several lines, and as long as
	// the user wrote it. One menu row shows the first line alone.
	multiLine := historyEntry{Prompt: "<user_input mode=\"act\">Fix the build\n\nThen run the tests.</user_input>"}
	assert.Equal(t, "Fix the build", sessionTitle(multiLine))
	titled := historyEntry{Metadata: historyMetadata{Title: "  Stored title\nsecond line  "}, Prompt: "The prompt"}
	assert.Equal(t, "Stored title", sessionTitle(titled), "the stored title wins, on one line")
	blankTitle := historyEntry{Metadata: historyMetadata{Title: "   "}, Prompt: "The prompt"}
	assert.Equal(t, "The prompt", sessionTitle(blankTitle), "a blank title falls back to the prompt")
	long := historyEntry{Prompt: strings.Repeat("é", 500)}
	title := sessionTitle(long)
	assert.Less(t, len([]rune(title)), 200, "a long prompt is capped")
	assert.True(t, strings.HasSuffix(title, "…"))
	assert.Empty(t, sessionTitle(historyEntry{}))
}

func TestStripUserInput(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Hi", stripUserInput(`<user_input mode="plan">Hi</user_input>`))
	assert.Equal(t, "Plain", stripUserInput("  Plain  "))
	assert.Equal(t, "<user_input broken", stripUserInput("<user_input broken"))
}

func TestEntryTimePrefersTheUpdate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 22, entryTime(historyEntry{UpdatedAt: "2026-09-22T00:00:00Z", EndedAt: "2026-09-21T00:00:00Z"}).Day())
	assert.Equal(t, 21, entryTime(historyEntry{EndedAt: "2026-09-21T00:00:00Z", StartedAt: "2026-09-20T00:00:00Z"}).Day())
	assert.Equal(t, 20, entryTime(historyEntry{StartedAt: "2026-09-20T00:00:00.123Z"}).Day())
	assert.True(t, entryTime(historyEntry{UpdatedAt: "yesterday"}).IsZero())
}

func TestSamePath(t *testing.T) {
	t.Parallel()
	dir := resolvePath(t.TempDir())
	assert.True(t, samePath(dir, dir))
	assert.True(t, samePath(dir+string(filepath.Separator), dir))
	assert.False(t, samePath("", dir))
	assert.False(t, samePath(filepath.Join(dir, "sub"), dir))
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		assert.True(t, samePath(strings.ToUpper(dir), dir), "the file system ignores case")
	}
}

func TestHistoryEnvFollowsTheQuery(t *testing.T) {
	t.Parallel()
	env := historyEnv([]string{"HOME=/real", "CLINE_DIR=/real/.cline", "PATH=/bin"}, agent.StoredSessionQuery{
		HomeDir: "/fixture",
		Getenv:  func(key string) string { return map[string]string{"CLINE_DATA_DIR": "/fixture/data"}[key] },
	})
	assert.Contains(t, env, homeEnvName()+"=/fixture")
	assert.Contains(t, env, "CLINE_DATA_DIR=/fixture/data")
	assert.NotContains(t, env, "CLINE_DIR=/real/.cline", "a hermetic query never reaches the real data")
	assert.Contains(t, env, "PATH=/bin")
}

// Without a query environment, the worker's own data variables stay, because
// they locate the user's Cline data, which the picker lists.
func TestHistoryEnvKeepsTheWorkersDataWithoutAQueryEnvironment(t *testing.T) {
	t.Parallel()
	env := historyEnv([]string{"HOME=/real", "CLINE_DIR=/real/.cline", "PATH=/bin"}, agent.StoredSessionQuery{})
	assert.Equal(t, []string{"HOME=/real", "CLINE_DIR=/real/.cline", "PATH=/bin"}, env)
}

func TestStoredSessionsIgnoresAnEmptyDirectory(t *testing.T) {
	t.Parallel()
	sessions, err := storedSessions(t.Context(), clineLocator, agent.StoredSessionQuery{})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

func TestEntryTimeIsUTC(t *testing.T) {
	t.Parallel()
	assert.Equal(t, time.UTC, entryTime(historyEntry{UpdatedAt: "2026-09-22T00:00:00Z"}).Location())
}

func TestStripUserInputKeepsWhatItCannotUnwrap(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "Hi", stripUserInput(`<user_input>Hi</user_input>`), "a wrapper with no mode")
	assert.Equal(t, "Hi", stripUserInput(`<user_input mode="act">Hi`), "a wrapper with no close")
	assert.Equal(t, "", stripUserInput(`<user_input mode="act"></user_input>`))
	assert.Equal(t, "Say <user_input>", stripUserInput("Say <user_input>"), "text that only holds the tag stays")
}

// With a home and no environment of its own, the query moves the home alone:
// the worker's data variables stay, because they locate the data that the
// picker lists.
func TestHistoryEnvMovesTheHomeAlone(t *testing.T) {
	t.Parallel()
	env := historyEnv([]string{"HOME=/real", "USERPROFILE=/real", "CLINE_DIR=/real/.cline"}, agent.StoredSessionQuery{HomeDir: "/fixture"})
	assert.Contains(t, env, homeEnvName()+"=/fixture")
	assert.NotContains(t, env, homeEnvName()+"=/real")
	assert.Contains(t, env, "CLINE_DIR=/real/.cline")
}

// A query environment that states nothing clears every data variable, so the
// reader falls back to the query's home and never to the worker's data.
func TestHistoryEnvClearsTheDataThatTheQueryDoesNotState(t *testing.T) {
	t.Parallel()
	inherited := []string{"PATH=/bin"}
	for _, key := range dataEnvKeys {
		inherited = append(inherited, key+"=/real")
	}
	env := historyEnv(inherited, agent.StoredSessionQuery{Getenv: func(string) string { return "" }})
	assert.Equal(t, []string{"PATH=/bin"}, env)
}

func TestDirectorySessionsTakeTheDefaultLimit(t *testing.T) {
	t.Parallel()
	dir := resolvePath(t.TempDir())
	entries := make([]historyEntry, 0, 3)
	for _, id := range []string{"a", "b", "c"} {
		entries = append(entries, historyEntry{SessionID: id, Cwd: dir})
	}
	assert.Len(t, directorySessions(entries, dir, 0), 3, "a limit of zero caps nothing")
	assert.Empty(t, directorySessions(nil, dir, 10))
}
