package grok

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// writeGrokSession writes one session directory with its summary.
func writeGrokSession(t *testing.T, group, id string, summary map[string]any) string {
	t.Helper()
	data, err := json.Marshal(summary)
	require.NoError(t, err)
	path := filepath.Join(group, id, grokSummaryFile)
	agenttest.WriteFixtureFile(t, path, string(data))
	return path
}

// grokSummaryFixture is a top-level session summary that the picker lists.
func grokSummaryFixture(id, cwd, title string, lastActive time.Time) map[string]any {
	return map[string]any{
		"info":            map[string]any{"id": id, "cwd": cwd},
		"num_messages":    4,
		"generated_title": title,
		"created_at":      lastActive.Add(-time.Hour).Format(time.RFC3339Nano),
		"updated_at":      lastActive.Add(-time.Minute).Format(time.RFC3339Nano),
		"last_active_at":  lastActive.Format(time.RFC3339Nano),
	}
}

func listGrokSessions(t *testing.T, home, dir string, env map[string]string) []agent.StoredSession {
	t.Helper()
	got, err := grokStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(env),
	})
	require.NoError(t, err)
	return got
}

func TestGrokReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
		writeGrokSession(t, group, "01a0cf79-ace2", grokSummaryFixture("01a0cf79-ace2", dir, "Fix the build", time.Now()))
		return "01a0cf79-ace2"
	})
}

func TestGrokEncodeCwdMatchesTheURLEncodingCrate(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"/Users/a/b":            "%2FUsers%2Fa%2Fb",
		"/Users/a b/c":          "%2FUsers%2Fa%20b%2Fc",
		"/w/x-y_z.q~r":          "%2Fw%2Fx-y_z.q~r",
		"/w/%41":                "%2Fw%2F%2541",
		"/w/한":                  "%2Fw%2F%ED%95%9C",
		`C:\Users\a`:            "C%3A%5CUsers%5Ca",
		"/w/plus+and&equals=?#": "%2Fw%2Fplus%2Band%26equals%3D%3F%23",
	} {
		assert.Equal(t, want, grokEncodeCwd(input), input)
	}
}

func TestGrokListsSessionsNewestFirstWithTheirTitles(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
	now := time.Now().UTC().Truncate(time.Second)
	writeGrokSession(t, group, "older", grokSummaryFixture("older", dir, "Older", now.Add(-2*time.Hour)))
	writeGrokSession(t, group, "newer", grokSummaryFixture("newer", dir, "Newer", now))
	summaryOnly := grokSummaryFixture("summary", dir, "", now.Add(-time.Hour))
	summaryOnly["session_summary"] = "From the summary"
	writeGrokSession(t, group, "summary", summaryOnly)

	got := listGrokSessions(t, home, dir, nil)

	require.Equal(t, []string{"newer", "summary", "older"}, agenttest.Handles(got))
	assert.Equal(t, "Newer", got[0].Title)
	assert.Equal(t, "From the summary", got[1].Title, "the session summary stands in for a missing title")
	assert.True(t, got[0].UpdatedAt.Equal(now), "the last activity orders the list")
}

func TestGrokSessionTimeFallsBackToTheUpdateAndTheFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
	updated := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	byUpdate := grokSummaryFixture("by-update", dir, "t", updated)
	delete(byUpdate, "last_active_at")
	byUpdate["updated_at"] = updated.Format(time.RFC3339Nano)
	writeGrokSession(t, group, "by-update", byUpdate)
	byFile := grokSummaryFixture("by-file", dir, "t", updated)
	delete(byFile, "last_active_at")
	delete(byFile, "updated_at")
	path := writeGrokSession(t, group, "by-file", byFile)
	fileTime := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	agenttest.TouchFixture(t, path, fileTime)

	got := listGrokSessions(t, home, dir, nil)

	require.Len(t, got, 2)
	times := map[string]time.Time{}
	for _, session := range got {
		times[session.Handle] = session.UpdatedAt
	}
	assert.True(t, times["by-update"].Equal(updated))
	assert.True(t, times["by-file"].Equal(fileTime), "a summary with no time takes its file's")
}

func TestGrokHidesSessionsThatItsOwnPickerHides(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
	now := time.Now()
	with := func(id string, change func(map[string]any)) {
		summary := grokSummaryFixture(id, dir, "Title "+id, now)
		change(summary)
		writeGrokSession(t, group, id, summary)
	}
	with("visible", func(map[string]any) {})
	with("subagent", func(s map[string]any) { s["session_kind"] = "subagent" })
	with("subagent-fork", func(s map[string]any) { s["session_kind"] = "subagent_fork" })
	with("headless", func(s map[string]any) { s["session_kind"] = "headless" })
	with("hidden", func(s map[string]any) { s["hidden"] = true })
	with("unhidden-subagent", func(s map[string]any) { s["session_kind"] = "subagent"; s["hidden"] = false })
	with("husk", func(s map[string]any) { s["num_messages"] = 0; delete(s, "generated_title") })
	with("empty-fork", func(s map[string]any) {
		s["num_messages"] = 0
		delete(s, "generated_title")
		s["session_kind"] = "fork"
	})
	with("empty-worktree", func(s map[string]any) {
		s["num_messages"] = 0
		delete(s, "generated_title")
		s["session_kind"] = "worktree"
	})
	with("labeled-worktree", func(s map[string]any) {
		s["num_messages"] = 0
		delete(s, "generated_title")
		s["worktree_label"] = "feature-x"
	})
	with("blank-title-husk", func(s map[string]any) {
		s["num_messages"] = 0
		s["generated_title"] = "   "
	})
	with("elsewhere", func(s map[string]any) { s["info"] = map[string]any{"id": "elsewhere", "cwd": "/somewhere/else"} })
	with("no-id", func(s map[string]any) { s["info"] = map[string]any{"cwd": dir} })
	with(".scratch", func(map[string]any) {})
	agenttest.WriteFixtureFile(t, filepath.Join(group, "broken", grokSummaryFile), "{not json")
	// A session directory with no summary is a session Grok did not finish
	// writing.
	require.NoError(t, os.MkdirAll(filepath.Join(group, "unfinished"), 0o755))

	got := listGrokSessions(t, home, dir, nil)

	assert.ElementsMatch(t, []string{"visible", "unhidden-subagent", "empty-fork", "empty-worktree", "labeled-worktree"}, agenttest.Handles(got),
		"a husk is hidden unless the user made it on purpose, and a blank title is no title")
}

func TestGrokListsTheExactWorkingDirectoryOnly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	sessions := filepath.Join(home, ".grok", "sessions")
	writeGrokSession(t, filepath.Join(sessions, grokEncodeCwd(dir)), "mine", grokSummaryFixture("mine", dir, "Mine", time.Now()))
	sibling := dir + "-other"
	writeGrokSession(t, filepath.Join(sessions, grokEncodeCwd(sibling)), "theirs", grokSummaryFixture("theirs", sibling, "Theirs", time.Now()))
	child := filepath.Join(dir, "sub")
	writeGrokSession(t, filepath.Join(sessions, grokEncodeCwd(child)), "child", grokSummaryFixture("child", child, "Child", time.Now()))

	assert.Equal(t, []string{"mine"}, agenttest.Handles(listGrokSessions(t, home, dir, nil)))
	assert.Equal(t, []string{"mine"}, agenttest.Handles(listGrokSessions(t, home, dir+string(os.PathSeparator), nil)),
		"a trailing separator leaves the directory the same")
}

func TestGrokFindsAHashedGroupByItsMarker(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	// A cwd whose encoded name passes the 255-byte limit of one component.
	dir := filepath.Join(home, strings.Repeat("long-directory-name-", 10), "project")
	require.Greater(t, len(grokEncodeCwd(dir)), grokMaxGroupNameBytes)
	sessions := filepath.Join(home, ".grok", "sessions")
	hashed := filepath.Join(sessions, "project-0123456789abcdef")
	agenttest.WriteFixtureFile(t, filepath.Join(hashed, grokCwdMarkerFile), dir+"\n")
	writeGrokSession(t, hashed, "hashed", grokSummaryFixture("hashed", dir, "Hashed", time.Now()))
	// Another hashed group states another cwd.
	other := filepath.Join(sessions, "project-fedcba9876543210")
	agenttest.WriteFixtureFile(t, filepath.Join(other, grokCwdMarkerFile), dir+"-x")
	writeGrokSession(t, other, "other", grokSummaryFixture("other", dir+"-x", "Other", time.Now()))

	assert.Equal(t, []string{"hashed"}, agenttest.Handles(listGrokSessions(t, home, dir, nil)))
}

func TestGrokReadsTheHomeThatGrokHomeStates(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	custom := filepath.Join(home, "elsewhere")
	dir := filepath.Join(home, "project")
	writeGrokSession(t, filepath.Join(custom, "sessions", grokEncodeCwd(dir)), "custom", grokSummaryFixture("custom", dir, "Custom", time.Now()))

	assert.Equal(t, []string{"custom"}, agenttest.Handles(listGrokSessions(t, home, dir, map[string]string{"GROK_HOME": custom})))
	assert.Empty(t, listGrokSessions(t, home, dir, nil), "without GROK_HOME the store is ~/.grok")
}

func TestGrokSessionStoreAbsenceIsNotAFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	assert.Empty(t, listGrokSessions(t, home, filepath.Join(home, "project"), nil))
	assert.Empty(t, listGrokSessions(t, home, "", nil), "no working directory lists nothing")
}

func TestGrokSessionStoreCapsTheList(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := filepath.Join(home, ".grok", "sessions", grokEncodeCwd(dir))
	now := time.Now()
	for i := range 5 {
		id := "s" + string(rune('a'+i))
		writeGrokSession(t, group, id, grokSummaryFixture(id, dir, id, now.Add(time.Duration(i)*time.Minute)))
	}
	got, err := grokStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"se", "sd"}, agenttest.Handles(got))
}

func TestGrokTrimCwdKeepsARoot(t *testing.T) {
	t.Parallel()
	sep := string(os.PathSeparator)
	assert.Equal(t, sep, grokTrimCwd(sep))
	assert.Equal(t, sep, grokTrimCwd(sep+sep))
	assert.Equal(t, sep+"w", grokTrimCwd(sep+"w"+sep))
	assert.Empty(t, grokTrimCwd("  "))
}

// longGrokCwd is a working directory whose encoded name passes the limit of one
// path component, so Grok stores its sessions under a hashed group.
func longGrokCwd(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, strings.Repeat("long-directory-name-", 10), "project")
	require.Greater(t, len(grokEncodeCwd(dir)), grokMaxGroupNameBytes)
	return dir
}

func TestGrokSessionGroupsFindOnlyAMarkedHashedGroup(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := longGrokCwd(t, home)
	root := filepath.Join(home, ".grok", "sessions")
	assert.Nil(t, grokSessionGroups(root, dir), "an absent store has no group")

	matching := filepath.Join(root, "project-0123456789abcdef")
	agenttest.WriteFixtureFile(t, filepath.Join(matching, grokCwdMarkerFile), " "+dir+"\n")
	require.NoError(t, os.MkdirAll(filepath.Join(root, "project-nomarker"), 0o755))
	agenttest.WriteFixtureFile(t, filepath.Join(root, "%2Fplain", grokCwdMarkerFile), dir)
	agenttest.WriteFixtureFile(t, filepath.Join(root, "project-a-file"), dir)
	agenttest.WriteFixtureFile(t, filepath.Join(root, "project-longer", grokCwdMarkerFile), dir+strings.Repeat("/deeper", 20))

	assert.Equal(t, []string{matching}, grokSessionGroups(root, dir),
		"a group with no marker, a plain name, a file and a marker of a longer cwd are not the group of dir")

	short := filepath.Join(home, "short")
	assert.Equal(t, []string{filepath.Join(root, grokEncodeCwd(short))}, grokSessionGroups(root, short), "a short cwd has its plain group alone")
}

// Two hashed groups can hold a copy of one session. Grok's own picker lists
// the session once, and so does this reader.
func TestGrokListsASessionOnceWhenTwoGroupsHoldIt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := longGrokCwd(t, home)
	sessions := filepath.Join(home, ".grok", "sessions")
	now := time.Now()
	for _, name := range []string{"project-aaaa", "project-bbbb"} {
		group := filepath.Join(sessions, name)
		agenttest.WriteFixtureFile(t, filepath.Join(group, grokCwdMarkerFile), dir)
		writeGrokSession(t, group, "copied", grokSummaryFixture("copied", dir, "Copied", now))
		writeGrokSession(t, group, "only-"+name, grokSummaryFixture("only-"+name, dir, "Only", now.Add(-time.Minute)))
	}

	assert.ElementsMatch(t, []string{"copied", "only-project-aaaa", "only-project-bbbb"}, agenttest.Handles(listGrokSessions(t, home, dir, nil)))
}
