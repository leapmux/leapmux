package agent

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeUserRecord is the shape of a real `user` line, reduced to the fields
// this reader takes. The cwd is escaped rather than pasted: it is a host path,
// and on Windows its backslashes would make the whole record undecodable.
func claudeUserRecord(cwd, sessionID, text string) string {
	return `{"type":"user","isSidechain":false,"cwd":` + fixtureJSONString(cwd) + `,"sessionId":"` + sessionID +
		`","timestamp":"2026-09-01T07:21:20.691Z","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}`
}

// writeClaudeTranscript writes one session file and sets its modification
// time, which is what Claude's own lister -- and this reader -- order by.
func writeClaudeTranscript(t *testing.T, projectDir, sessionID string, at time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(projectDir, sessionID+".jsonl")
	writeFixtureFile(t, path, strings.Join(lines, "\n")+"\n")
	touchFixture(t, path, at)
}

func TestMangleClaudePath(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "-Users-trustin-Workspaces-leapmux", mangleClaudePath("/Users/trustin/Workspaces/leapmux"))
	// Every non-alphanumeric character becomes a hyphen, which is why the
	// mangling is lossy and the cwd inside the file has to be checked.
	assert.Equal(t, "-a-b-c", mangleClaudePath("/a/b-c"))
	assert.Equal(t, "-a-b-c", mangleClaudePath("/a/b_c"))
	assert.Equal(t, "-a-b-c", mangleClaudePath("/a/b.c"))

	// Claude's rule is a JavaScript regex with no `u` flag, so it matches one
	// UTF-16 CODE UNIT at a time. A character outside the Basic Multilingual
	// Plane is TWO code units there and one rune here, so it must produce two
	// hyphens or the computed directory is not the one Claude wrote -- and every
	// session of that working directory becomes invisible with no error.
	assert.Equal(t, "-Users-me-work---app", mangleClaudePath("/Users/me/work/\U0001F680app"))
	// A character INSIDE the plane is one code unit on both sides, so it must
	// not gain a second hyphen.
	assert.Equal(t, "-Users-me-caf-", mangleClaudePath("/Users/me/café"))
}

func TestClaudeStoredSessions(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-newest", base,
		claudeUserRecord(dir, "sess-newest", "build the thing"),
		`{"type":"ai-title","aiTitle":"Build the thing","sessionId":"sess-newest"}`)
	writeClaudeTranscript(t, projectDir, "sess-older", base.Add(-time.Hour),
		claudeUserRecord(dir, "sess-older", "fix the bug"),
		`{"type":"ai-title","aiTitle":"Fix the bug","sessionId":"sess-older"}`)

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-newest", "sess-older"}, handlesOf(got))
	assert.Equal(t, "Build the thing", got[0].Title)
	assert.Equal(t, base, got[0].UpdatedAt.UTC())
}

// TestClaudeStoredSessions_ChecksTheRecordedCwd pins the check that makes the
// lossy directory name safe: two working directories can mangle to one
// directory, so a transcript is placed by the cwd it recorded, not by where it
// sits.
func TestClaudeStoredSessions_ChecksTheRecordedCwd(t *testing.T) {
	t.Parallel()
	mine := absPath("Users", "dev", "my-project")
	theirs := absPath("Users", "dev", "my_project")
	require.Equal(t, mangleClaudePath(mine), mangleClaudePath(theirs),
		"the fixture only proves anything if the two really do collide")

	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(mine))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-mine", base, claudeUserRecord(mine, "sess-mine", "mine"))
	writeClaudeTranscript(t, projectDir, "sess-theirs", base, claudeUserRecord(theirs, "sess-theirs", "theirs"))
	// A transcript with no cwd at all cannot be placed and must not be offered.
	writeClaudeTranscript(t, projectDir, "sess-nowhere", base,
		`{"type":"user","message":{"role":"user","content":"no cwd here"}}`)

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: mine, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-mine"}, handlesOf(got))
}

// The walk budget must not be spent BEFORE the cwd check decides which
// transcripts belong here. A colliding directory with more recent sessions
// filled the budget with rows the cwd check then rejected, and this directory's
// own sessions were reported as none at all.
func TestClaudeStoredSessions_ACollidingDirectoryCannotCrowdOutThisOne(t *testing.T) {
	t.Parallel()
	mine := absPath("Users", "dev", "my-project")
	theirs := absPath("Users", "dev", "my_project")
	require.Equal(t, mangleClaudePath(mine), mangleClaudePath(theirs),
		"the fixture only proves anything if the two really do collide")

	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(mine))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	// This directory's only session is the OLDEST file present.
	writeClaudeTranscript(t, projectDir, "sess-mine", base, claudeUserRecord(mine, "sess-mine", "mine"))
	// The colliding directory's sessions are all newer, and there are more of
	// them than the query's limit.
	for i, id := range []string{"sess-t1", "sess-t2", "sess-t3"} {
		writeClaudeTranscript(t, projectDir, id, base.Add(time.Duration(i+1)*time.Hour),
			claudeUserRecord(theirs, id, "theirs"))
	}

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: mine, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-mine"}, handlesOf(got),
		"this directory's session survives although three newer ones collide with it")
}

func TestClaudeStoredSessions_ExcludesSubagentTranscripts(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-real", base, claudeUserRecord(dir, "sess-real", "real work"))
	// A sidechain transcript is a subagent's.
	writeClaudeTranscript(t, projectDir, "sess-sidechain", base,
		`{"type":"user","isSidechain":true,"cwd":`+fixtureJSONString(dir)+`,"sessionId":"sess-sidechain","message":{"role":"user","content":"sub"}}`)
	// The per-session sidecar tree sits in a DIRECTORY, which the walk refuses.
	writeClaudeTranscript(t, filepath.Join(projectDir, "sess-real", "subagents"), "task-1", base,
		claudeUserRecord(dir, "task-1", "delegated"))

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-real"}, handlesOf(got))
}

func TestClaudeStoredSessions_TitlePrecedence(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "a title the user set wins over every other",
			lines: []string{
				claudeUserRecord(dir, "s", "first prompt"),
				`{"type":"ai-title","aiTitle":"model title","sessionId":"s"}`,
				`{"type":"summary","summary":"legacy summary","sessionId":"s"}`,
				`{"type":"last-prompt","lastPrompt":"latest prompt","sessionId":"s"}`,
				`{"customTitle":"what I called it","sessionId":"s"}`,
			},
			want: "what I called it",
		},
		{
			name: "then the model's title",
			lines: []string{
				claudeUserRecord(dir, "s", "first prompt"),
				`{"type":"summary","summary":"legacy summary","sessionId":"s"}`,
				`{"type":"ai-title","aiTitle":"model title","sessionId":"s"}`,
			},
			want: "model title",
		},
		{
			name: "then the legacy compaction summary",
			lines: []string{
				claudeUserRecord(dir, "s", "first prompt"),
				`{"type":"summary","summary":"legacy summary","sessionId":"s"}`,
			},
			want: "legacy summary",
		},
		{
			name: "then the most recent prompt",
			lines: []string{
				claudeUserRecord(dir, "s", "first prompt"),
				`{"type":"last-prompt","lastPrompt":"latest prompt","sessionId":"s"}`,
			},
			want: "latest prompt",
		},
		{
			name:  "and finally the first prompt of the session",
			lines: []string{claudeUserRecord(dir, "s", "first prompt")},
			want:  "first prompt",
		},
		{
			name: "a tool result never becomes the title",
			lines: []string{
				`{"type":"user","cwd":` + fixtureJSONString(dir) + `,"sessionId":"s","message":{"role":"user","content":[{"type":"tool_result","content":"machine output"},{"type":"text","text":"the real prompt"}]}}`,
			},
			want: "the real prompt",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
			writeClaudeTranscript(t, projectDir, "s", base, tc.lines...)

			got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
				WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
			})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Title)
		})
	}
}

// TestClaudeStoredSessions_TakesTheNewestTitleFromTheTail pins why the tail is
// read at all: `ai-title` is appended again whenever the CLI regenerates it, so
// a transcript longer than the head window holds a stale title at the front.
func TestClaudeStoredSessions_TakesTheNewestTitleFromTheTail(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))

	lines := []string{
		claudeUserRecord(dir, "s", "first prompt"),
		`{"type":"ai-title","aiTitle":"stale title","sessionId":"s"}`,
	}
	// Padding wider than the head window, so the last title is only reachable
	// from the tail.
	padding := strings.Repeat("x", 4096)
	for range 40 {
		lines = append(lines, `{"type":"assistant","message":{"role":"assistant","content":"`+padding+`"}}`)
	}
	lines = append(lines, `{"type":"ai-title","aiTitle":"fresh title","sessionId":"s"}`)
	writeClaudeTranscript(t, projectDir, "s", time.Now(), lines...)

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "fresh title", got[0].Title)
}

func TestClaudeStoredSessions_SurvivesACorruptTranscript(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-good", base, claudeUserRecord(dir, "sess-good", "works"))
	writeClaudeTranscript(t, projectDir, "sess-broken", base.Add(time.Hour), "{ not json at all", "still not json")
	writeClaudeTranscript(t, projectDir, "sess-empty", base.Add(2*time.Hour))

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-good"}, handlesOf(got),
		"one unreadable transcript must not lose the readable ones")
}

func TestClaudeStoredSessions_HonoursTheConfigDirOverride(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	alt := filepath.Join(home, "alt-claude")
	writeClaudeTranscript(t, filepath.Join(alt, "projects", mangleClaudePath(dir)), "s", time.Now(),
		claudeUserRecord(dir, "s", "over here"))

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home,
		Getenv: fixtureEnv(map[string]string{"CLAUDE_CONFIG_DIR": "~/alt-claude"}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, handlesOf(got), "the leading ~ expands against HomeDir")
}

// TestClaudeStoredSessions_LongPathMatchesByPrefix covers the case the mangling
// cannot reproduce: past 200 characters Claude appends a hash computed by Bun's
// own hash function, so the directory is found by its truncated prefix and the
// recorded cwd decides which sessions belong.
func TestClaudeStoredSessions_LongPathMatchesByPrefix(t *testing.T) {
	t.Parallel()
	// 45 nested components, which is what pushes the mangled name past the cap.
	// Each call builds its own slice, so the two leaves cannot share a backing
	// array and overwrite one another.
	deepPath := func(leaf string) string {
		segments := append([]string{"Users", "dev"}, slices.Repeat([]string{"deep"}, 45)...)
		return absPath(append(segments, leaf)...)
	}
	dir := deepPath("project")
	mangled := mangleClaudePath(dir)
	require.Greater(t, len(mangled), claudeMangleMaxLength, "the fixture must exercise the long case")

	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	hashed := filepath.Join(projects, mangled[:claudeMangleMaxLength]+"-1a2b3c")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	writeClaudeTranscript(t, hashed, "s", base, claudeUserRecord(dir, "s", "deep work"))
	// A different deep path sharing the prefix: the cwd check separates them.
	other := deepPath("other")
	writeClaudeTranscript(t, filepath.Join(projects, mangleClaudePath(other)[:claudeMangleMaxLength]+"-9z8y7x"),
		"other", base, claudeUserRecord(other, "other", "not mine"))

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, handlesOf(got))
}

func TestClaudeStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: absPath("Users", "dev", "project"), HomeDir: t.TempDir(), Getenv: fixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestClaudeStoredSessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := absPath("Users", "dev", "project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range 6 {
		id := "sess-" + string(rune('a'+i))
		writeClaudeTranscript(t, projectDir, id, base.Add(time.Duration(i)*time.Hour),
			claudeUserRecord(dir, id, "work"))
	}

	got, err := claudeStoredSessions(context.Background(), StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: fixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-f", "sess-e"}, handlesOf(got), "the newest two")
}
