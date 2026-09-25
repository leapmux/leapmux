package qwen

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// writeQwenSession writes one transcript of the store under root.
func writeQwenSession(t *testing.T, root, cwd, id string, records ...map[string]any) string {
	t.Helper()
	lines := make([]string, 0, len(records))
	for _, record := range records {
		data, err := json.Marshal(record)
		require.NoError(t, err)
		lines = append(lines, string(data))
	}
	path := filepath.Join(root, "projects", qwenSanitizeCwd(cwd), "chats", id+".jsonl")
	agenttest.WriteFixtureFile(t, path, strings.Join(lines, "\n")+"\n")
	return path
}

// userRecord is the first record of a session: the first prompt, and the cwd.
func userRecord(id, cwd, text string) map[string]any {
	return map[string]any{
		"uuid": "u1", "parentUuid": nil, "sessionId": id, "timestamp": "2026-09-23T18:09:07.140Z",
		"type": "user", "cwd": cwd, "version": "0.24.3",
		"message": map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}},
	}
}

func listQwenSessions(t *testing.T, home, dir string, env map[string]string) []agent.StoredSession {
	t.Helper()
	got, err := qwenStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(env),
	})
	require.NoError(t, err)
	return got
}

const (
	qwenSessionA = "70dab2cd-62c7-4f76-b53e-50a950df1520"
	qwenSessionB = "3f634835-36d5-4b60-9031-74ca5d99cb9e"
	qwenSessionC = "11111111-2222-4333-8444-555555555555"
)

func TestQwenReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		writeQwenSession(t, filepath.Join(home, ".qwen"), dir, qwenSessionA, userRecord(qwenSessionA, dir, "PROBE_TEXT say hello"))
		return qwenSessionA
	})
}

func TestQwenSanitizeCwdMatchesQwen(t *testing.T) {
	t.Parallel()
	for input, want := range map[string]string{
		"/Users/trustin/Workspaces/leapmux/.tmp/probe/qwen-code/ws": "-Users-trustin-Workspaces-leapmux--tmp-probe-qwen-code-ws",
		"/a b/c_d": "-a-b-c-d",
		"/w/한":     "-w--",
		"/w/😀":     "-w---",
		"Plain123": "Plain123",
	} {
		assert.Equal(t, want, qwenSanitizeCwd(input), input)
	}
}

func TestQwenListsSessionsWithTheirTitles(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "ws")
	root := filepath.Join(home, ".qwen")
	now := time.Now().Truncate(time.Second)
	pathA := writeQwenSession(t, root, dir, qwenSessionA, userRecord(qwenSessionA, dir, "First prompt\nsecond line"))
	pathB := writeQwenSession(t, root, dir, qwenSessionB,
		userRecord(qwenSessionB, dir, "Ignored prompt"),
		map[string]any{"type": "assistant", "message": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "ok"}}}},
		map[string]any{"type": "system", "subtype": "custom_title", "systemPayload": map[string]any{"customTitle": "Old title", "titleSource": "auto"}},
		map[string]any{"type": "system", "subtype": "custom_title", "systemPayload": map[string]any{"customTitle": "Renamed", "titleSource": "manual"}},
	)
	displayed := userRecord(qwenSessionC, dir, "@a.txt raw")
	displayed["systemPayload"] = map[string]any{"displayText": "Shown text"}
	pathC := writeQwenSession(t, root, dir, qwenSessionC, displayed)
	agenttest.TouchFixture(t, pathA, now.Add(-time.Hour))
	agenttest.TouchFixture(t, pathB, now)
	agenttest.TouchFixture(t, pathC, now.Add(-2*time.Hour))

	got := listQwenSessions(t, home, dir, nil)

	require.Equal(t, []string{qwenSessionB, qwenSessionA, qwenSessionC}, agenttest.Handles(got), "the last write orders the list")
	assert.Equal(t, "Renamed", got[0].Title, "the last title wins")
	assert.Equal(t, "First prompt", got[1].Title, "the first prompt stands in for a title, one line of it")
	assert.Equal(t, "Shown text", got[2].Title, "the text Qwen showed for a prompt wins over its raw parts")
	assert.True(t, got[0].UpdatedAt.Equal(now))
}

func TestQwenFiltersTheLossyGroupByCwd(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".qwen")
	dir := filepath.Join(home, "a-b")
	twin := filepath.Join(home, "a_b")
	require.Equal(t, qwenSanitizeCwd(dir), qwenSanitizeCwd(twin), "the two directories share a group")
	writeQwenSession(t, root, dir, qwenSessionA, userRecord(qwenSessionA, dir, "mine"))
	writeQwenSession(t, root, twin, qwenSessionB, userRecord(qwenSessionB, twin, "theirs"))

	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, nil)))
}

func TestQwenHidesWhatItsOwnListerHides(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".qwen")
	dir := filepath.Join(home, "ws")
	writeQwenSession(t, root, dir, qwenSessionA, userRecord(qwenSessionA, dir, "visible"))
	writeQwenSession(t, root, dir, qwenSessionB,
		userRecord(qwenSessionB, dir, "a branch"),
		map[string]any{"type": "system", "subtype": "parent_session", "systemPayload": map[string]any{"parentSessionId": qwenSessionA}},
	)
	agenttest.WriteFixtureFile(t, filepath.Join(root, "projects", qwenSanitizeCwd(dir), "chats", qwenSessionC+".jsonl"), "{not json\n")
	agenttest.WriteFixtureFile(t, filepath.Join(root, "projects", qwenSanitizeCwd(dir), "chats", qwenSessionA+".runtime.json"), "{}")
	agenttest.WriteFixtureFile(t, filepath.Join(root, "projects", qwenSanitizeCwd(dir), "chats", "notes.jsonl"), "{}\n")
	agenttest.WriteFixtureFile(t, filepath.Join(root, "projects", qwenSanitizeCwd(dir), "chats", "archive", qwenSessionC+".jsonl"), "{}\n")

	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, nil)))
}

func TestQwenResolvesItsRuntimeDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "ws")

	runtimeDir := filepath.Join(home, "runtime")
	writeQwenSession(t, runtimeDir, dir, qwenSessionA, userRecord(qwenSessionA, dir, "runtime"))
	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{"QWEN_RUNTIME_DIR": runtimeDir})))

	qwenHomeDir := filepath.Join(home, "qwen-home")
	writeQwenSession(t, qwenHomeDir, dir, qwenSessionB, userRecord(qwenSessionB, dir, "home"))
	assert.Equal(t, []string{qwenSessionB}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{"QWEN_HOME": qwenHomeDir})))

	settingsHome := filepath.Join(home, "settings-home")
	output := filepath.Join(home, "output")
	agenttest.WriteFixtureFile(t, filepath.Join(settingsHome, "settings.json"), `{"advanced":{"runtimeOutputDir":`+agenttest.JSONString(output)+`}}`)
	writeQwenSession(t, output, dir, qwenSessionC, userRecord(qwenSessionC, dir, "setting"))
	assert.Equal(t, []string{qwenSessionC}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{"QWEN_HOME": settingsHome})),
		"the user setting moves the transcripts")

	relative := filepath.Join(home, "relative-home")
	agenttest.WriteFixtureFile(t, filepath.Join(relative, "settings.json"), `{"advanced":{"runtimeOutputDir":"out"}}`)
	writeQwenSession(t, relative, dir, qwenSessionA, userRecord(qwenSessionA, dir, "default"))
	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{"QWEN_HOME": relative})),
		"a relative setting states no place that this reader can resolve, so the global directory stands")
}

func TestQwenSessionStoreAbsenceIsNotAFailure(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	assert.Empty(t, listQwenSessions(t, home, filepath.Join(home, "ws"), nil))
	assert.Empty(t, listQwenSessions(t, home, "", nil))
}

func TestQwenSessionStoreCapsTheList(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".qwen")
	dir := filepath.Join(home, "ws")
	now := time.Now()
	for i, id := range []string{qwenSessionA, qwenSessionB, qwenSessionC} {
		path := writeQwenSession(t, root, dir, id, userRecord(id, dir, id))
		agenttest.TouchFixture(t, path, now.Add(time.Duration(i)*time.Minute))
	}
	got, err := qwenStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{qwenSessionC, qwenSessionB}, agenttest.Handles(got))
}

func TestQwenRuntimeDirectoryExpandsTheHome(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "ws")

	writeQwenSession(t, filepath.Join(home, "rt"), dir, qwenSessionA, userRecord(qwenSessionA, dir, "runtime"))
	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{"QWEN_RUNTIME_DIR": "~/rt"})),
		"QWEN_RUNTIME_DIR may start with ~")

	settingsHome := filepath.Join(home, "settings-home")
	agenttest.WriteFixtureFile(t, filepath.Join(settingsHome, "settings.json"), `{"advanced":{"runtimeOutputDir":"~/out"}}`)
	writeQwenSession(t, filepath.Join(home, "out"), dir, qwenSessionB, userRecord(qwenSessionB, dir, "setting"))
	assert.Equal(t, []string{qwenSessionB}, agenttest.Handles(listQwenSessions(t, home, dir, map[string]string{
		"QWEN_HOME": settingsHome, "QWEN_RUNTIME_DIR": "  ",
	})), "the setting may start with ~, and a blank QWEN_RUNTIME_DIR states no directory")
}

func TestQwenUnreadableSettingsLeaveTheGlobalDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "ws")
	qwenHomeDir := filepath.Join(home, ".qwen")
	agenttest.WriteFixtureFile(t, filepath.Join(qwenHomeDir, "settings.json"), `{"advanced":`)
	writeQwenSession(t, qwenHomeDir, dir, qwenSessionA, userRecord(qwenSessionA, dir, "default"))

	assert.Equal(t, []string{qwenSessionA}, agenttest.Handles(listQwenSessions(t, home, dir, nil)))
}

// Qwen's own lister reads the first ten records of a transcript, so a branch
// mark there hides the session and a branch mark after them does not.
func TestQwenReadsTheBranchMarkInTheHeadRecordsOnly(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	root := filepath.Join(home, ".qwen")
	dir := filepath.Join(home, "ws")
	branch := map[string]any{"type": "system", "subtype": "parent_session", "systemPayload": map[string]any{"parentSessionId": qwenSessionC}}
	reply := map[string]any{"type": "assistant", "message": map[string]any{"role": "model", "parts": []any{map[string]any{"text": "ok"}}}}
	withBranchAt := func(id string, position int) {
		records := []map[string]any{userRecord(id, dir, "prompt "+id)}
		for len(records) < position-1 {
			records = append(records, reply)
		}
		writeQwenSession(t, root, dir, id, append(records, branch)...)
	}
	withBranchAt(qwenSessionA, qwenHeadRecords)
	withBranchAt(qwenSessionB, qwenHeadRecords+1)

	assert.Equal(t, []string{qwenSessionB}, agenttest.Handles(listQwenSessions(t, home, dir, nil)))
}

func TestQwenPromptText(t *testing.T) {
	t.Parallel()
	text := func(value string) chatPart { return chatPart{Text: &value} }
	for _, tc := range []struct {
		name   string
		record qwenRecord
		want   string
	}{
		{name: "no message", record: qwenRecord{}, want: ""},
		{name: "blank shown text", record: qwenRecord{
			SystemPayload: json.RawMessage(`{"displayText":"  "}`),
			Message:       &chatContent{Parts: []chatPart{text(" "), text("the raw prompt")}},
		}, want: "the raw prompt"},
		{name: "unreadable payload", record: qwenRecord{
			SystemPayload: json.RawMessage(`"text"`),
			Message:       &chatContent{Parts: []chatPart{text("the raw prompt")}},
		}, want: "the raw prompt"},
		{name: "only blank parts", record: qwenRecord{Message: &chatContent{Parts: []chatPart{text(""), {}}}}, want: ""},
	} {
		assert.Equal(t, tc.want, qwenPromptText(tc.record), tc.name)
	}
}

func TestQwenBlankCustomTitleKeepsTheTitleBeforeIt(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "ws")
	writeQwenSession(t, filepath.Join(home, ".qwen"), dir, qwenSessionA,
		userRecord(qwenSessionA, dir, "The first prompt"),
		map[string]any{"type": "system", "subtype": "custom_title", "systemPayload": map[string]any{"customTitle": "Named"}},
		map[string]any{"type": "system", "subtype": "custom_title", "systemPayload": map[string]any{"customTitle": "  "}},
	)

	got := listQwenSessions(t, home, dir, nil)
	require.Len(t, got, 1)
	assert.Equal(t, "Named", got[0].Title)
}
