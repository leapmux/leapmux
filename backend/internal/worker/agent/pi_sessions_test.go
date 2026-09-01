package agent

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writePiTranscript writes one Pi session file under the directory Pi's own
// naming produces, and sets the modification time the reader orders by.
func writePiTranscript(t *testing.T, sessionsRoot, cwd, fileName string, at time.Time, lines ...string) {
	t.Helper()
	requireHostAbsDir(t, cwd)
	path := filepath.Join(sessionsRoot, manglePiPath(cwd), fileName)
	writeFixtureFile(t, path, strings.Join(lines, "\n")+"\n")
	touchFixture(t, path, at)
}

func TestManglePiPath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "--Users-trustin-Workspaces-leapmux--", manglePiPath("/Users/trustin/Workspaces/leapmux"))
	assert.Equal(t, "--private-tmp--", manglePiPath("/private/tmp"))
	// `:` and `\` both become hyphens, so a drive letter keeps its own
	// separator: `C:\Users` -> `C--Users`.
	assert.Equal(t, "--C--Users-dev--", manglePiPath(`C:\Users\dev`))
	// Exactly ONE leading separator is dropped, so the second one of a UNC
	// path becomes a hyphen rather than disappearing.
	assert.Equal(t, "---server-share--", manglePiPath("//server/share"))
	assert.Equal(t, "--relative-path--", manglePiPath("relative/path"))
}

func TestPiStoredSessions(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_sess-new.jsonl", base,
		`{"type":"session","version":3,"id":"sess-new","timestamp":"2026-09-01T12:00:00.000Z","cwd":`+fixtureJSONString(dir)+`}`,
		`{"type":"model_change","provider":"zai","modelId":"glm-5.3"}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"run the tests"}]}}`)
	writePiTranscript(t, root, dir, "2026-09-01T11-00-00-000Z_sess-old.jsonl", base.Add(-time.Hour),
		`{"type":"session","version":3,"id":"sess-old","cwd":`+fixtureJSONString(dir)+`}`,
		`{"type":"message","message":{"role":"user","content":"earlier work"}}`)

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-new", "sess-old"}, handlesOf(got),
		"the handle is the session ID, which is what Pi reports at runtime -- not the file path")
	assert.Equal(t, "run the tests", got[0].Title, "Pi stores no title, so the first user message is it")
	assert.Equal(t, "earlier work", got[1].Title)
	assert.Equal(t, base, got[0].UpdatedAt.UTC())
}

func TestPiStoredSessions_ReadsAVersion4Header(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")

	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_v4.jsonl", time.Now(),
		`{"kind":"header","version":4,"id":"v4","cwd":`+fixtureJSONString(dir)+`}`,
		`{"type":"message","message":{"role":"user","content":[{"type":"text","text":"v4 prompt"}]}}`)

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"v4"}, handlesOf(got))
	assert.Equal(t, "v4 prompt", got[0].Title)
}

func TestPiStoredSessions_ExcludesSubagentTranscripts(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_sess-real.jsonl", base,
		`{"type":"session","version":3,"id":"sess-real","cwd":`+fixtureJSONString(dir)+`}`,
		`{"type":"message","message":{"role":"user","content":"real"}}`)
	// Subagent transcripts sit in a `tasks/` directory beside the session file,
	// which the file-only walk refuses.
	writeFixtureFile(t,
		filepath.Join(root, manglePiPath(dir), "2026-09-01T12-00-00-000Z_sess-real", "tasks", "task-1.jsonl"),
		`{"type":"session","version":3,"id":"task-1","cwd":`+fixtureJSONString(dir)+`}`+"\n")

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-real"}, handlesOf(got))
}

func TestPiStoredSessions_RefusesAForeignHeader(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")

	// A header naming a different working directory, in this directory's
	// folder. The folder placed it, so the disagreement is the file's; refuse.
	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_wrong.jsonl", time.Now(),
		`{"type":"session","version":3,"id":"wrong","cwd":`+fixtureJSONString(absPath("somewhere", "else"))+`}`)
	// A file whose first record is not a header at all.
	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_nothdr.jsonl", time.Now(),
		`{"type":"message","message":{"role":"user","content":"no header"}}`)
	// And a real one, so the assertion is not vacuous.
	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_ok.jsonl", time.Now(),
		`{"type":"session","version":3,"id":"ok","cwd":`+fixtureJSONString(dir)+`}`,
		`{"type":"message","message":{"role":"user","content":"fine"}}`)

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"ok"}, handlesOf(got))
}

// TestPiStoredSessions_HeaderWithoutCwdIsPlacedByItsDirectory covers the
// difference from Claude: Pi's directory name encodes the working directory
// exactly, so a header that omits the cwd is still placed.
func TestPiStoredSessions_HeaderWithoutCwdIsPlacedByItsDirectory(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")

	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_nocwd.jsonl", time.Now(),
		`{"type":"session","version":3,"id":"nocwd"}`,
		`{"type":"message","message":{"role":"user","content":"still mine"}}`)

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"nocwd"}, handlesOf(got))
}

func TestPiStoredSessions_EnvOverrides(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")

	t.Run("PI_CODING_AGENT_DIR moves the agent directory", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, "alt-agent", "sessions")
		writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_s.jsonl", time.Now(),
			`{"type":"session","version":3,"id":"s","cwd":`+fixtureJSONString(dir)+`}`)

		got, err := piStoredSessions(context.Background(), StoredSessionQuery{
			WorkingDir: dir, HomeDir: home,
			Getenv: fixtureEnv(map[string]string{piAgentDirEnv: "~/alt-agent"}),
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"s"}, handlesOf(got))
	})

	t.Run("PI_CODING_AGENT_SESSION_DIR wins over it", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		root := filepath.Join(home, "explicit-sessions")
		writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_s.jsonl", time.Now(),
			`{"type":"session","version":3,"id":"s","cwd":`+fixtureJSONString(dir)+`}`)

		got, err := piStoredSessions(context.Background(), StoredSessionQuery{
			WorkingDir: dir, HomeDir: home,
			Getenv: fixtureEnv(map[string]string{
				piAgentDirEnv:   "~/ignored",
				piSessionDirEnv: root,
			}),
		})
		require.NoError(t, err)
		assert.Equal(t, []string{"s"}, handlesOf(got))
	})
}

func TestPiStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: absPath("Users", "dev", "project"), HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestPiSessionIDFromFileName(t *testing.T) {
	t.Parallel()
	// The timestamp component contains hyphens but no underscore, so the LAST
	// underscore separates it from the id.
	assert.Equal(t, "018f4a2b-0c1d-7e3f", piSessionIDFromFileName("2026-08-28T03-13-15-703Z_018f4a2b-0c1d-7e3f.jsonl"))
	assert.Equal(t, "bare", piSessionIDFromFileName("bare.jsonl"))
}

// TestPiStoredSessions_FallsBackToTheFileNameID covers a header this reader
// could parse as a header but that carries no id.
func TestPiStoredSessions_FallsBackToTheFileNameID(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	root := filepath.Join(home, ".pi", "agent", "sessions")

	writePiTranscript(t, root, dir, "2026-09-01T12-00-00-000Z_from-name.jsonl", time.Now(),
		`{"type":"session","version":3,"cwd":`+fixtureJSONString(dir)+`}`)

	got, err := piStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"from-name"}, handlesOf(got))
}
