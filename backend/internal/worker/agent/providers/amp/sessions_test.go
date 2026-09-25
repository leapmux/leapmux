package amp

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// fileURI is the `tree` URI Amp records for a directory.
func fileURI(path string) string {
	slashed := filepath.ToSlash(path)
	if !strings.HasPrefix(slashed, "/") {
		slashed = "/" + slashed // a Windows drive path
	}
	return (&url.URL{Scheme: "file", Path: slashed}).String()
}

func ptr(s string) *string { return &s }

func TestSameWorkspace(t *testing.T) {
	t.Parallel()
	root := resolvePath(t.TempDir())

	assert.True(t, sameWorkspace(fileURI(root), root))
	assert.False(t, sameWorkspace(fileURI(filepath.Join(root, "sub")), root), "a subdirectory is another workspace")
	assert.False(t, sameWorkspace(fileURI(filepath.Dir(root)), root))
	assert.False(t, sameWorkspace("https://github.com/org/repo", root), "a remote tree is not a local workspace")
	assert.False(t, sameWorkspace("file://", root))
	assert.False(t, sameWorkspace("::not a uri", root))

	spaced := filepath.Join(root, "my project")
	require.NoError(t, os.Mkdir(spaced, 0o755))
	assert.True(t, sameWorkspace(fileURI(spaced), resolvePath(spaced)), "a percent-encoded space identifies the same directory")

	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		assert.True(t, sameWorkspace(fileURI(strings.ToUpper(root)), root), "the file system ignores case")
	}
}

// A `tree` URI of a Windows drive puts a slash before the drive letter, which
// the comparison drops.
func TestSameWorkspaceReadsAWindowsDrivePath(t *testing.T) {
	t.Parallel()
	root := filepath.FromSlash("C:/Users/me/work")
	assert.True(t, sameWorkspace("file:///C:/Users/me/work", root))
	assert.True(t, sameWorkspace("file:///C:/Users/me/work/", root), "a trailing slash identifies the same directory")
	assert.False(t, sameWorkspace("file:///D:/Users/me/work", root))
}

// queryEnv makes the CLI's environment follow the query. The service's query
// states no environment, so the worker's own values stay; a hermetic query
// replaces every variable that locates Amp's data and login.
func TestQueryEnv(t *testing.T) {
	t.Parallel()
	home := homeEnvName()
	worker := []string{
		"PATH=/usr/bin",
		home + "=/worker-home",
		"XDG_CONFIG_HOME=/worker/config",
		"AMP_API_KEY=the-worker-key",
		"AMP_URL=https://worker.example",
	}
	values := func(env []string) map[string][]string {
		out := map[string][]string{}
		for _, entry := range env {
			key, value, _ := strings.Cut(entry, "=")
			out[key] = append(out[key], value)
		}
		return out
	}

	t.Run("a query with no home and no environment changes nothing", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, worker, queryEnv(append([]string(nil), worker...), agent.StoredSessionQuery{}))
	})
	t.Run("the query's home alone replaces the home", func(t *testing.T) {
		t.Parallel()
		env := values(queryEnv(append([]string(nil), worker...), agent.StoredSessionQuery{HomeDir: "/query-home"}))
		assert.Equal(t, []string{"/query-home"}, env[home])
		assert.Equal(t, []string{"the-worker-key"}, env["AMP_API_KEY"], "a query with no environment keeps the worker's login")
		assert.Equal(t, []string{"/worker/config"}, env["XDG_CONFIG_HOME"])
	})
	t.Run("the query's environment replaces every variable of Amp's data and login", func(t *testing.T) {
		t.Parallel()
		env := values(queryEnv(append([]string(nil), worker...), agent.StoredSessionQuery{
			HomeDir: "/query-home",
			Getenv:  envOf(map[string]string{"AMP_URL": "http://127.0.0.1:9", "XDG_DATA_HOME": "/query/data"}),
		}))
		assert.Equal(t, []string{"/usr/bin"}, env["PATH"], "a variable that locates nothing of Amp's stays")
		assert.Equal(t, []string{"/query-home"}, env[home])
		assert.Equal(t, []string{"http://127.0.0.1:9"}, env["AMP_URL"])
		assert.Equal(t, []string{"/query/data"}, env["XDG_DATA_HOME"])
		for _, key := range []string{"AMP_API_KEY", "XDG_CONFIG_HOME", envSettingsFile} {
			assert.NotContainsf(t, env, key, "the query states no %s, so the CLI gets none", key)
		}
	})
}

func TestSameWorkspaceFollowsLinks(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a symlink needs a privilege that a Windows test runner lacks")
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	require.NoError(t, os.Mkdir(real, 0o755))
	link := filepath.Join(dir, "link")
	require.NoError(t, os.Symlink(real, link))
	assert.True(t, sameWorkspace(fileURI(link), resolvePath(real)))
}

func TestParseThreadList(t *testing.T) {
	t.Parallel()
	entries, err := parseThreadList([]byte(`[{"id":"T-1","title":"One","updated":"2026-09-20T10:00:00.000Z","tree":"file:///work","messageCount":2}]` + "\nNo more threads.\n"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Equal(t, "T-1", entries[0].ID)
	assert.Equal(t, "file:///work", *entries[0].Tree)

	entries, err = parseThreadList([]byte(`[]`))
	require.NoError(t, err)
	assert.Empty(t, entries)

	_, err = parseThreadList([]byte("No threads found.\n"))
	assert.Error(t, err)
	_, err = parseThreadList(nil)
	assert.Error(t, err)
}

func TestWorkspaceSessions(t *testing.T) {
	t.Parallel()
	root := resolvePath(t.TempDir())
	here, elsewhere := fileURI(root), fileURI(filepath.Join(root, "other"))
	entries := []threadEntry{
		{ID: "T-old", Title: "Old", Updated: "2026-09-01T00:00:00Z", Tree: &here, MessageCount: 4},
		{ID: "T-new", Title: "New", Updated: "2026-09-20T12:30:00.123Z", Tree: &here, MessageCount: 1},
		{ID: "T-empty", Title: "Nothing sent", Updated: "2026-09-21T00:00:00Z", Tree: &here, MessageCount: 0},
		{ID: "T-other", Title: "Other", Updated: "2026-09-21T00:00:00Z", Tree: &elsewhere, MessageCount: 3},
		{ID: "T-no-tree", Title: "No tree", Updated: "2026-09-21T00:00:00Z", MessageCount: 3},
		{ID: "", Title: "No id", Updated: "2026-09-21T00:00:00Z", Tree: &here, MessageCount: 3},
		{ID: "T-bad-time", Title: "Bad time", Updated: "yesterday", Tree: ptr(here), MessageCount: 3},
	}

	sessions := workspaceSessions(entries, root, agent.DefaultStoredSessionLimit)
	handles := make([]string, 0, len(sessions))
	for _, session := range sessions {
		handles = append(handles, session.Handle)
	}
	assert.Equal(t, []string{"T-new", "T-old", "T-bad-time"}, handles, "newest first, and an unreadable time sorts last")
	assert.Equal(t, "New", sessions[0].Title)
	assert.Equal(t, time.Date(2026, 9, 20, 12, 30, 0, 123_000_000, time.UTC), sessions[0].UpdatedAt.UTC())

	assert.Len(t, workspaceSessions(entries, root, 1), 1, "the limit caps the list")
	assert.Empty(t, workspaceSessions(nil, root, 10))
}

func TestWorkspaceRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	assert.Equal(t, resolvePath(dir), workspaceRoot(context.Background(), dir), "outside a repository the directory is its own workspace")
	assert.Empty(t, workspaceRoot(context.Background(), ""))
	assert.Empty(t, workspaceRoot(context.Background(), filepath.Join(dir, "absent")))
	file := filepath.Join(dir, "file")
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	assert.Empty(t, workspaceRoot(context.Background(), file))
}

func TestStoredSessionsForAMissingDirectoryRunsNothing(t *testing.T) {
	t.Parallel()
	// A locator that specifies a program that must never run: the reader
	// returns before it resolves one.
	sessions, err := ampProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: filepath.Join(t.TempDir(), "absent"),
	})
	require.NoError(t, err)
	assert.Empty(t, sessions)
}
