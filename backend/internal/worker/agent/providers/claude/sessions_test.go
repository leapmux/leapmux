package claude

import (
	"context"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeUserRecord is the shape of a real `user` line, reduced to the fields
// this reader takes. The cwd is escaped rather than pasted: it is a host path,
// and on Windows its backslashes would make the whole record undecodable.
func claudeUserRecord(cwd, sessionID, text string) string {
	return `{"type":"user","isSidechain":false,"cwd":` + agenttest.JSONString(cwd) + `,"sessionId":"` + sessionID +
		`","timestamp":"2026-09-01T07:21:20.691Z","message":{"role":"user","content":[{"type":"text","text":"` + text + `"}]}}`
}

// writeClaudeTranscript writes one session file and sets its modification
// time, which is what Claude's own lister -- and this reader -- order by.
func writeClaudeTranscript(t *testing.T, projectDir, sessionID string, at time.Time, lines ...string) {
	t.Helper()
	path := filepath.Join(projectDir, sessionID+".jsonl")
	agenttest.WriteFixtureFile(t, path, strings.Join(lines, "\n")+"\n")
	agenttest.TouchFixture(t, path, at)
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
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-newest", base,
		claudeUserRecord(dir, "sess-newest", "build the thing"),
		`{"type":"ai-title","aiTitle":"Build the thing","sessionId":"sess-newest"}`)
	writeClaudeTranscript(t, projectDir, "sess-older", base.Add(-time.Hour),
		claudeUserRecord(dir, "sess-older", "fix the bug"),
		`{"type":"ai-title","aiTitle":"Fix the bug","sessionId":"sess-older"}`)

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)

	assert.Equal(t, []string{"sess-newest", "sess-older"}, agenttest.Handles(got))
	assert.Equal(t, "Build the thing", got[0].Title)
	assert.Equal(t, base, got[0].UpdatedAt.UTC())
}

// TestClaudeStoredSessions_ChecksTheRecordedCwd pins the check that makes the
// lossy directory name safe: two working directories can mangle to one
// directory, so a transcript is placed by the cwd it recorded, not by where it
// sits.
func TestClaudeStoredSessions_ChecksTheRecordedCwd(t *testing.T) {
	t.Parallel()
	mine := testutil.NativeAbsPath("/Users/dev/my-project")
	theirs := testutil.NativeAbsPath("/Users/dev/my_project")
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

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: mine, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-mine"}, agenttest.Handles(got))
}

// The walk budget must not be spent BEFORE the cwd check decides which
// transcripts belong here. A colliding directory with more recent sessions
// filled the budget with rows the cwd check then rejected, and this directory's
// own sessions were reported as none at all.
func TestClaudeStoredSessions_ACollidingDirectoryCannotCrowdOutThisOne(t *testing.T) {
	t.Parallel()
	mine := testutil.NativeAbsPath("/Users/dev/my-project")
	theirs := testutil.NativeAbsPath("/Users/dev/my_project")
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

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: mine, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-mine"}, agenttest.Handles(got),
		"this directory's session survives although three newer ones collide with it")
}

func TestClaudeStoredSessions_ExcludesSubagentTranscripts(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-real", base, claudeUserRecord(dir, "sess-real", "real work"))
	// A sidechain transcript is a subagent's.
	writeClaudeTranscript(t, projectDir, "sess-sidechain", base,
		`{"type":"user","isSidechain":true,"cwd":`+agenttest.JSONString(dir)+`,"sessionId":"sess-sidechain","message":{"role":"user","content":"sub"}}`)
	// The per-session sidecar tree sits in a DIRECTORY, which the walk refuses.
	writeClaudeTranscript(t, filepath.Join(projectDir, "sess-real", "subagents"), "task-1", base,
		claudeUserRecord(dir, "task-1", "delegated"))

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-real"}, agenttest.Handles(got))
}

func TestClaudeStoredSessions_TitlePrecedence(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
			writeClaudeTranscript(t, projectDir, "s", base, tc.lines...)

			got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
				WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
			})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Title)
		})
	}
}

// claudeUserContentRecord is a `user` line whose message content is content.
// extra adds top-level fields, such as isMeta. The JSON encoder writes the
// record, so a text block can hold any characters.
func claudeUserContentRecord(t *testing.T, cwd string, content any, extra map[string]any) string {
	t.Helper()
	record := map[string]any{
		"type":        "user",
		"isSidechain": false,
		"cwd":         cwd,
		"sessionId":   "s",
		"message":     map[string]any{"role": "user", "content": content},
	}
	maps.Copy(record, extra)
	raw, err := json.Marshal(record)
	require.NoError(t, err)
	return string(raw)
}

// claudeTextBlocks is a content array that holds one text block for each text.
func claudeTextBlocks(texts ...string) []map[string]any {
	blocks := make([]map[string]any, 0, len(texts))
	for _, text := range texts {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	return blocks
}

// TestClaudeStoredSessions_FirstPromptFollowsTheCLI pins the first-prompt title
// to Claude Code's own reader (uGe in 2.1.289). The reader takes the first text
// block that is not context markup, and it skips the records that carry no
// prompt of the user.
func TestClaudeStoredSessions_FirstPromptFollowsTheCLI(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	notes := providerkit.BuildInlineTextAttachmentBlock(agent.ClassifiedAttachment{
		Filename: "notes.txt", MIMEType: "text/plain", Data: []byte("line one\nline two\n"), Kind: agent.AttachmentKindText,
	})
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": "iVBORw0KGgo="}}

	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name: "an attached text file before the prompt does not become the title",
			lines: []string{
				claudeUserContentRecord(t, dir, claudeTextBlocks(notes, "Summarize the notes."), nil),
			},
			want: "Summarize the notes.",
		},
		{
			name: "two attached files and an image before the prompt do not become the title",
			lines: []string{
				claudeUserContentRecord(t, dir, []any{image, claudeTextBlocks(notes)[0], claudeTextBlocks(notes)[0], claudeTextBlocks("Compare them.")[0]}, nil),
			},
			want: "Compare them.",
		},
		{
			name: "a system reminder before the prompt is skipped",
			lines: []string{
				claudeUserContentRecord(t, dir, claudeTextBlocks("<system-reminder>\nThe date changed.\n</system-reminder>", "fix the bug"), nil),
			},
			want: "fix the bug",
		},
		{
			name: "the marker of an interrupted request is skipped",
			lines: []string{
				claudeUserContentRecord(t, dir, claudeTextBlocks("[Request interrupted by user for tool use]"), nil),
				claudeUserRecord(dir, "s", "try again with tests"),
			},
			want: "try again with tests",
		},
		{
			name: "a meta record is skipped",
			lines: []string{
				claudeUserContentRecord(t, dir, "Caveat: the messages below came from local commands.", map[string]any{"isMeta": true}),
				claudeUserRecord(dir, "s", "the real prompt"),
			},
			want: "the real prompt",
		},
		{
			name: "a compaction summary is skipped",
			lines: []string{
				claudeUserContentRecord(t, dir, "This session continues an earlier conversation.", map[string]any{"isCompactSummary": true}),
				claudeUserRecord(dir, "s", "the real prompt"),
			},
			want: "the real prompt",
		},
		{
			name: "a record that carries a tool result never supplies the title",
			lines: []string{
				claudeUserContentRecord(t, dir, []any{
					map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "machine output"},
					claudeTextBlocks("hook feedback")[0],
				}, nil),
				claudeUserRecord(dir, "s", "the real prompt"),
			},
			want: "the real prompt",
		},
		{
			name: "an image-only record supplies no title",
			lines: []string{
				claudeUserContentRecord(t, dir, []any{image}, nil),
				claudeUserRecord(dir, "s", "describe the screenshot"),
			},
			want: "describe the screenshot",
		},
		{
			name: "a slash command supplies the title only when no prompt follows it",
			lines: []string{
				claudeUserContentRecord(t, dir, "<command-message>review</command-message>\n<command-name>/review</command-name>", nil),
			},
			want: "/review",
		},
		{
			name: "a prompt after a slash command wins over the command",
			lines: []string{
				claudeUserContentRecord(t, dir, "<command-message>review</command-message>\n<command-name>/review</command-name>", nil),
				claudeUserRecord(dir, "s", "explain the diff"),
			},
			want: "explain the diff",
		},
		{
			name: "a shell command supplies the title in the CLI's form",
			lines: []string{
				claudeUserContentRecord(t, dir, "<bash-input>ls -la</bash-input>", nil),
			},
			want: "! ls -la",
		},
		{
			name: "pasted content counts as the prompt",
			lines: []string{
				claudeUserContentRecord(t, dir, "<pasted_content id=\"1a2b\">\nhello world\n</pasted_content id=\"1a2b\">", nil),
			},
			want: "hello world",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
			writeClaudeTranscript(t, projectDir, "s", base, tc.lines...)

			got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
				WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
			})
			require.NoError(t, err)
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Title)
		})
	}
}

// TestExpandClaudePastedContent pins the port of Claude Code's expansion of its
// pasted-content markers (Ovt and Xme in 2.1.289), edge cases included.
func TestExpandClaudePastedContent(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		text string
		want string
	}{
		{name: "text without a marker stays", text: "plain text", want: "plain text"},
		{name: "empty text stays empty", text: "", want: ""},
		{name: "a marker gives its body", text: "<pasted_content id=\"1a2b\">\nhello\nworld\n</pasted_content id=\"1a2b\">", want: "hello\nworld"},
		{
			name: "up to two line breaks around a marker go, and the pieces join",
			text: "fix this\n\n\n<pasted_content id=\"00ff\">\nBODY\n</pasted_content id=\"00ff\">\n\n\nthanks",
			want: "fix this\nBODY\nthanks",
		},
		{name: "an empty body gives nothing", text: "a <pasted_content id=\"abcd\">\n</pasted_content id=\"abcd\"> b", want: "a  b"},
		{
			name: "two markers both expand",
			text: "<pasted_content id=\"0001\">\none\n</pasted_content id=\"0001\"><pasted_content id=\"0002\">\ntwo\n</pasted_content id=\"0002\">",
			want: "onetwo",
		},
		{name: "an id that is not four lowercase hexadecimal digits stays", text: "<pasted_content id=\"ABCD\">\nx\n</pasted_content id=\"ABCD\">", want: "<pasted_content id=\"ABCD\">\nx\n</pasted_content id=\"ABCD\">"},
		{name: "an opening tag without its line break stays", text: "<pasted_content id=\"abcd\">x\n</pasted_content id=\"abcd\">", want: "<pasted_content id=\"abcd\">x\n</pasted_content id=\"abcd\">"},
		{name: "a closing tag for another id does not close the marker", text: "<pasted_content id=\"abcd\">\nx\n</pasted_content id=\"dcba\">", want: "<pasted_content id=\"abcd\">\nx\n</pasted_content id=\"dcba\">"},
		{
			name: "a marker without its closing tag ends the expansion and stays",
			text: "<pasted_content id=\"0001\">\none\n</pasted_content id=\"0001\"> then <pasted_content id=\"0002\">\nopen",
			want: "one then <pasted_content id=\"0002\">\nopen",
		},
		{name: "a marker cut short at the end of the text stays", text: "see <pasted_content id=\"ab", want: "see <pasted_content id=\"ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, expandClaudePastedContent(tc.text))
		})
	}
}

// TestClaudeStoredSessions_TakesTheNewestTitleFromTheTail pins why the tail is
// read at all: `ai-title` is appended again whenever the CLI regenerates it, so
// a transcript longer than the head window holds a stale title at the front.
func TestClaudeStoredSessions_TakesTheNewestTitleFromTheTail(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
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

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "fresh title", got[0].Title)
}

func TestClaudeStoredSessions_SurvivesACorruptTranscript(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	writeClaudeTranscript(t, projectDir, "sess-good", base, claudeUserRecord(dir, "sess-good", "works"))
	writeClaudeTranscript(t, projectDir, "sess-broken", base.Add(time.Hour), "{ not json at all", "still not json")
	writeClaudeTranscript(t, projectDir, "sess-empty", base.Add(2*time.Hour))

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-good"}, agenttest.Handles(got),
		"one unreadable transcript must not lose the readable ones")
}

func TestClaudeStoredSessions_HonoursTheConfigDirOverride(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	alt := filepath.Join(home, "alt-claude")
	writeClaudeTranscript(t, filepath.Join(alt, "projects", mangleClaudePath(dir)), "s", time.Now(),
		claudeUserRecord(dir, "s", "over here"))

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home,
		Getenv: agenttest.FixtureEnv(map[string]string{"CLAUDE_CONFIG_DIR": "~/alt-claude"}),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, agenttest.Handles(got), "the leading ~ expands against HomeDir")
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
		return testutil.NativeAbsPath("/" + strings.Join(append(segments, leaf), "/"))
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

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"s"}, agenttest.Handles(got))
}

func TestClaudeStoredSessions_AbsentStoreIsEmpty(t *testing.T) {
	t.Parallel()
	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: testutil.NativeAbsPath("/Users/dev/project"), HomeDir: t.TempDir(), Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestClaudeStoredSessions_RespectsTheLimit(t *testing.T) {
	t.Parallel()
	dir := testutil.NativeAbsPath("/Users/dev/project")
	home := t.TempDir()
	projectDir := filepath.Join(home, ".claude", "projects", mangleClaudePath(dir))
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	for i := range 6 {
		id := "sess-" + string(rune('a'+i))
		writeClaudeTranscript(t, projectDir, id, base.Add(time.Duration(i)*time.Hour),
			claudeUserRecord(dir, id, "work"))
	}

	got, err := claudeStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: dir, HomeDir: home, Getenv: agenttest.FixtureEnv(nil), Limit: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"sess-f", "sess-e"}, agenttest.Handles(got), "the newest two")
}

func TestClaudeReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		writeClaudeTranscript(t, filepath.Join(home, ".claude", "projects", mangleClaudePath(dir)),
			"claude-session", time.Now(), claudeUserRecord(dir, "claude-session", "work"))
		return "claude-session"
	})
}
