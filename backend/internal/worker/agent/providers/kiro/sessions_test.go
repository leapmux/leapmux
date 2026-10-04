package kiro

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// kiroSessionFixture is a session record as Kiro's v3 engine writes it.
func kiroSessionFixture(id, dir, title string, modified time.Time) map[string]any {
	return map[string]any{
		"schemaVersion":  "1.0.0",
		"id":             id,
		"title":          title,
		"agentMode":      "vibe",
		"workspacePaths": []any{dir},
		"createdAt":      modified.Add(-time.Hour).Format(time.RFC3339Nano),
		"lastModifiedAt": modified.Format(time.RFC3339Nano),
		"rootPaths":      []any{dir},
	}
}

// kiroGroup is the directory that holds the sessions of one workspace.
func kiroGroup(home, dir string) string {
	return filepath.Join(home, kiroStoreDir, kiroSessionsDir, kiroWorkspaceKey(dir))
}

// writeKiroSession writes one session directory: its record and, when
// messages is not empty, its message log.
func writeKiroSession(t *testing.T, group, id string, record map[string]any, messages string) string {
	t.Helper()
	data, err := json.Marshal(record)
	require.NoError(t, err)
	path := filepath.Join(group, id, kiroSessionFile)
	agenttest.WriteFixtureFile(t, path, string(data))
	if messages != "" {
		agenttest.WriteFixtureFile(t, filepath.Join(group, id, kiroMessagesFile), messages)
	}
	return path
}

// kiroMessagesFixture is a message log with one prompt.
const kiroMessagesFixture = `{"role":"user","content":[{"type":"text","text":"hello"}]}` + "\n"

func listKiroSessions(t *testing.T, home, dir string, limit int) []agent.StoredSession {
	t.Helper()
	got, err := kiroStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: limit,
	})
	require.NoError(t, err)
	return got
}

func TestKiroReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		const id = "sess_c7e2951e-620b-49f8-9928-d37958fe1aba"
		writeKiroSession(t, kiroGroup(home, dir), id, kiroSessionFixture(id, dir, "V3-HELLO say hi", time.Now()), kiroMessagesFixture)
		return id
	})
}

func TestKiroListsSessionsNewestFirstWithTheirTitles(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := kiroGroup(home, dir)
	now := time.Now().UTC().Truncate(time.Millisecond)
	writeKiroSession(t, group, "sess_older", kiroSessionFixture("sess_older", dir, "Older", now.Add(-2*time.Hour)), kiroMessagesFixture)
	writeKiroSession(t, group, "sess_newer", kiroSessionFixture("sess_newer", dir, "Newer", now), kiroMessagesFixture)
	writeKiroSession(t, group, "sess_untitled", kiroSessionFixture("sess_untitled", dir, kiroUntitledTitle, now.Add(-time.Hour)), kiroMessagesFixture)

	got := listKiroSessions(t, home, dir, 0)

	require.Equal(t, []string{"sess_newer", "sess_untitled", "sess_older"}, agenttest.Handles(got))
	assert.Equal(t, "Newer", got[0].Title)
	assert.Empty(t, got[1].Title, "Kiro's placeholder title is no title")
	assert.True(t, got[0].UpdatedAt.Equal(now), "the record's own time orders the list")
}

func TestKiroSessionTimeFallsBackToTheFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	record := kiroSessionFixture("sess_a", dir, "t", time.Now())
	delete(record, "lastModifiedAt")
	path := writeKiroSession(t, kiroGroup(home, dir), "sess_a", record, kiroMessagesFixture)
	fileTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	agenttest.TouchFixture(t, path, fileTime)

	got := listKiroSessions(t, home, dir, 0)

	require.Len(t, got, 1)
	assert.True(t, got[0].UpdatedAt.Equal(fileTime), "a record with no time takes its file's")
}

func TestKiroHidesWhatItsOwnPickerHides(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := kiroGroup(home, dir)
	now := time.Now()
	with := func(id string, messages string, change func(map[string]any)) {
		record := kiroSessionFixture(id, dir, "Title "+id, now)
		change(record)
		writeKiroSession(t, group, id, record, messages)
	}
	with("sess_visible", kiroMessagesFixture, func(map[string]any) {})
	with("sess_step", kiroMessagesFixture, func(r map[string]any) {
		r["_meta"] = map[string]any{"kiro": map[string]any{"workflow": map[string]any{"workflowId": "wf_1", "nodeId": "work"}}}
	})
	with("sess_null_workflow", kiroMessagesFixture, func(r map[string]any) {
		r["_meta"] = map[string]any{"kiro": map[string]any{"workflow": nil}}
	})
	with("sess_never_prompted", "", func(map[string]any) {})
	with("sess_wrong_id", kiroMessagesFixture, func(r map[string]any) { r["id"] = "sess_other" })
	with("sess_no_id", kiroMessagesFixture, func(r map[string]any) { delete(r, "id") })
	with("sess_two_roots", kiroMessagesFixture, func(r map[string]any) { r["workspacePaths"] = []any{dir, dir + "-b"} })
	with("sess_elsewhere", kiroMessagesFixture, func(r map[string]any) { r["workspacePaths"] = []any{"/somewhere/else"} })
	with("sess_no_roots", kiroMessagesFixture, func(r map[string]any) { delete(r, "workspacePaths") })
	// A root with a trailing separator is the same workspace, as the engine
	// normalizes it.
	with("sess_trailing_root", kiroMessagesFixture, func(r map[string]any) { r["workspacePaths"] = []any{dir + "/"} })
	agenttest.WriteFixtureFile(t, filepath.Join(group, "sess_broken", kiroSessionFile), "{not json")
	agenttest.WriteFixtureFile(t, filepath.Join(group, "sess_empty_log", kiroSessionFile), mustJSON(t, kiroSessionFixture("sess_empty_log", dir, "t", now)))
	agenttest.WriteFixtureFile(t, filepath.Join(group, "sess_empty_log", kiroMessagesFile), "")
	// A session directory with no record is one Kiro did not finish writing.
	require.NoError(t, os.MkdirAll(filepath.Join(group, "sess_unfinished"), 0o755))
	// A stray file in the group is no session.
	agenttest.WriteFixtureFile(t, filepath.Join(group, "notes.txt"), "x")

	got := listKiroSessions(t, home, dir, 0)

	assert.ElementsMatch(t, []string{"sess_visible", "sess_null_workflow", "sess_trailing_root"}, agenttest.Handles(got))
}

// A group that is a file, not a directory, is a store that LeapMux cannot
// read. The caller reports that failure, rather than a list that looks empty.
func TestKiroSessionStoreThatIsNotADirectoryFails(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	agenttest.WriteFixtureFile(t, kiroGroup(home, dir), "not a directory")

	_, err := kiroStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})

	assert.Error(t, err)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return string(data)
}

func TestKiroListsTheExactWorkingDirectoryOnly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	writeKiroSession(t, kiroGroup(home, dir), "sess_mine", kiroSessionFixture("sess_mine", dir, "Mine", time.Now()), kiroMessagesFixture)
	sibling := dir + "-other"
	writeKiroSession(t, kiroGroup(home, sibling), "sess_theirs", kiroSessionFixture("sess_theirs", sibling, "Theirs", time.Now()), kiroMessagesFixture)
	child := filepath.Join(dir, "sub")
	writeKiroSession(t, kiroGroup(home, child), "sess_child", kiroSessionFixture("sess_child", child, "Child", time.Now()), kiroMessagesFixture)

	assert.Equal(t, []string{"sess_mine"}, agenttest.Handles(listKiroSessions(t, home, dir, 0)))
	assert.Equal(t, []string{"sess_mine"}, agenttest.Handles(listKiroSessions(t, home, dir+string(os.PathSeparator), 0)),
		"a trailing separator gives the same directory")
}

func TestKiroSessionStoreAbsenceIsNotAFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	assert.Empty(t, listKiroSessions(t, home, filepath.Join(home, "project"), 0))
	assert.Empty(t, listKiroSessions(t, home, "", 0), "no working directory lists nothing")
	assert.Empty(t, listKiroSessions(t, home, "   ", 0))
}

func TestKiroSessionStoreCapsTheList(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := kiroGroup(home, dir)
	now := time.Now()
	for i := range 5 {
		id := "sess_" + string(rune('a'+i))
		writeKiroSession(t, group, id, kiroSessionFixture(id, dir, id, now.Add(time.Duration(i)*time.Minute)), kiroMessagesFixture)
	}
	// Workflow steps are newer than every session and take no place.
	for i := range 3 {
		id := "sess_step_" + string(rune('a'+i))
		record := kiroSessionFixture(id, dir, id, now.Add(time.Hour))
		record["_meta"] = map[string]any{"kiro": map[string]any{"workflow": map[string]any{"workflowId": "wf"}}}
		writeKiroSession(t, group, id, record, kiroMessagesFixture)
	}

	assert.Equal(t, []string{"sess_e", "sess_d"}, agenttest.Handles(listKiroSessions(t, home, dir, 2)))
}

func TestKiroNormalizePath(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		assert.Equal(t, "c:/users/a/project", kiroNormalizePath(`C:\Users\A\Project\`))
		assert.Equal(t, "c:/", kiroNormalizePath(`C:\`))
		assert.Equal(t, "//server/share/dir", kiroNormalizePath(`\\server\share\dir`))
		return
	}
	for input, want := range map[string]string{
		"/Users/a/project":    "/Users/a/project",
		"/Users/a/project/":   "/Users/a/project",
		"/Users/a/project//":  "/Users/a/project",
		"/Users/a/./b/../c":   "/Users/a/c",
		"/":                   "/",
		"/Users/A/Mixed Case": "/Users/A/Mixed Case",
	} {
		assert.Equal(t, want, kiroNormalizePath(input), input)
	}
	wd, err := os.Getwd()
	require.NoError(t, err)
	assert.Equal(t, filepath.ToSlash(filepath.Join(wd, "rel")), kiroNormalizePath("rel"), "a relative path resolves against the working directory")
}

func TestKiroIsWindowsDrive(t *testing.T) {
	t.Parallel()
	assert.True(t, isWindowsDrive("C:"))
	assert.True(t, isWindowsDrive("z:"))
	assert.False(t, isWindowsDrive("C:/"))
	assert.False(t, isWindowsDrive("1:"))
	assert.False(t, isWindowsDrive("/"))
	assert.False(t, isWindowsDrive(""))
}

func TestKiroWorkspaceKeyIsSixteenHexDigits(t *testing.T) {
	t.Parallel()
	key := kiroWorkspaceKey(filepath.Join(t.TempDir(), "project"))
	assert.Len(t, key, kiroWorkspaceKeyLen)
	assert.Equal(t, strings.ToLower(key), key)
	assert.Equal(t, kiroWorkspaceKey("/w/p"), kiroWorkspaceKey("/w/p/"), "the key reads the normalized path")
}
