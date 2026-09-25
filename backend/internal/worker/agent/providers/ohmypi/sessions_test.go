package ohmypi

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sessionFileName is omp's `<file-safe UTC timestamp>_<session id>.jsonl`.
func sessionFileName(id string) string {
	return "2026-09-23T18-11-57-284Z_" + id + ".jsonl"
}

// writeSession writes one omp session file with the records given, one per line,
// and sets its modification time.
func writeSession(t *testing.T, dir, id string, at time.Time, records ...string) string {
	t.Helper()
	path := filepath.Join(dir, sessionFileName(id))
	content := ""
	for _, record := range records {
		content += record + "\n"
	}
	agenttest.WriteFixtureFile(t, path, content)
	agenttest.TouchFixture(t, path, at)
	return path
}

// sessionHeader is a header record as omp 18 writes it.
func sessionHeader(id, cwd string) string {
	return `{"type":"session","version":3,"id":"` + id + `","timestamp":"2026-09-23T18:11:57.284Z","cwd":` + agenttest.JSONString(cwd) + `}`
}

// userMessage and assistantMessage are message records as omp 18 writes them.
func userMessage(text string) string {
	return `{"type":"message","id":"m1","parentId":null,"timestamp":"2026-09-23T18:12:00.000Z","message":{"role":"user","content":[{"type":"text","text":` + agenttest.JSONString(text) + `}],"attribution":"user","timestamp":1790187120000}}`
}

const assistantMessage = `{"type":"message","id":"m2","parentId":"m1","timestamp":"2026-09-23T18:12:01.000Z","message":{"role":"assistant","content":[{"type":"text","text":"Done."}],"stopReason":"stop"}}`

// existingDir creates a directory and returns its path with symbolic links
// resolved, the form omp's process.cwd() reports.
func existingDir(t *testing.T, path string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(path, 0o755))
	resolved, err := filepath.EvalSymlinks(path)
	require.NoError(t, err)
	return resolved
}

// sessionQuery builds a query with its environment fixed to vars.
func sessionQuery(workingDir, home string, vars map[string]string) agent.StoredSessionQuery {
	return agent.StoredSessionQuery{WorkingDir: workingDir, HomeDir: home, Getenv: agenttest.FixtureEnv(vars)}
}

func TestReadsItsSessionStore(t *testing.T) {
	t.Parallel()
	agenttest.RequireReadsSessionStore(t, Registration().Plugin, func(t *testing.T, home, dir string) string {
		// The literal directory name, not one this package computes: the test
		// then also pins the encoding omp uses.
		existingDir(t, dir)
		return writeSession(t, filepath.Join(home, ".omp", "agent", "sessions", "-workspace-project"),
			"01a0cf77", time.Now(), sessionHeader("01a0cf77", existingDir(t, dir)), userMessage("Fix the build."))
	})
}

func TestStoredSessions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	t.Run("lists the sessions of the working directory, newest first", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		bucket := filepath.Join(home, ".omp", "agent", "sessions", "-project")
		older := writeSession(t, bucket, "older", now.Add(-time.Hour), sessionHeader("older", cwd), userMessage("First task"), assistantMessage)
		newer := writeSession(t, bucket, "newer", now, sessionHeader("newer", cwd), userMessage("Second task"), assistantMessage)
		// A session's subagent transcripts sit in a directory beside its file.
		agenttest.WriteFixtureFile(t, filepath.Join(bucket, "2026-09-23T18-11-57-284Z_newer", "0-Scout.jsonl"),
			sessionHeader("child", cwd)+"\n"+userMessage("Child task")+"\n")

		got, err := storedSessions(context.Background(), sessionQuery(cwd, home, nil))
		require.NoError(t, err)
		assert.Equal(t, []string{newer, older}, agenttest.Handles(got))
		assert.Equal(t, "Second task", got[0].Title)
		assert.Equal(t, now, got[0].UpdatedAt.UTC())
	})

	t.Run("honors the limit", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		bucket := filepath.Join(home, ".omp", "agent", "sessions", "-project")
		for i, id := range []string{"a", "b", "c"} {
			writeSession(t, bucket, id, now.Add(time.Duration(i)*time.Minute), sessionHeader(id, cwd), userMessage(id))
		}
		q := sessionQuery(cwd, home, nil)
		q.Limit = 2
		got, err := storedSessions(context.Background(), q)
		require.NoError(t, err)
		assert.Equal(t, []string{"c", "b"}, titles(got))
	})

	t.Run("an empty working directory lists nothing", func(t *testing.T) {
		got, err := storedSessions(context.Background(), sessionQuery("  ", t.TempDir(), nil))
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("an absent store lists nothing and fails nothing", func(t *testing.T) {
		home := t.TempDir()
		got, err := storedSessions(context.Background(), sessionQuery(existingDir(t, filepath.Join(home, "p")), home, nil))
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a flat session directory is filtered by each header's cwd", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		flat := filepath.Join(home, "flat")
		mine := writeSession(t, flat, "mine", now, sessionHeader("mine", cwd), userMessage("Mine"))
		writeSession(t, flat, "theirs", now, sessionHeader("theirs", filepath.Join(home, "other")), userMessage("Theirs"))

		got, err := storedSessions(context.Background(), sessionQuery(cwd, home, map[string]string{envSessionDir: flat}))
		require.NoError(t, err)
		assert.Equal(t, []string{mine}, agenttest.Handles(got))
	})

	t.Run("a relative flat directory is relative to the working directory", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		mine := writeSession(t, filepath.Join(cwd, "sessions"), "mine", now, sessionHeader("mine", cwd), userMessage("Mine"))

		got, err := storedSessions(context.Background(), sessionQuery(cwd, home, map[string]string{envSessionDir: "sessions"}))
		require.NoError(t, err)
		assert.Equal(t, []string{mine}, agenttest.Handles(got))
	})

	// omp refuses to start with a profile name it rejects, so it wrote no
	// session under any directory for that environment.
	t.Run("a profile name omp rejects lists nothing", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		writeSession(t, filepath.Join(home, ".omp", "agent", "sessions", "-project"), "s", now, sessionHeader("s", cwd), userMessage("Prompt"))

		got, err := storedSessions(context.Background(), sessionQuery(cwd, home, map[string]string{envProfile: "Not Valid"}))
		require.NoError(t, err)
		assert.Empty(t, got)
	})

	t.Run("a named profile reads its own store", func(t *testing.T) {
		home := t.TempDir()
		cwd := existingDir(t, filepath.Join(home, "project"))
		writeSession(t, filepath.Join(home, ".omp", "agent", "sessions", "-project"), "default", now, sessionHeader("default", cwd), userMessage("Default"))
		mine := writeSession(t, filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions", "-project"), "work", now,
			sessionHeader("work", cwd), userMessage("Work"))

		got, err := storedSessions(context.Background(), sessionQuery(cwd, home, map[string]string{envProfile: "work"}))
		require.NoError(t, err)
		assert.Equal(t, []string{mine}, agenttest.Handles(got))
	})

	t.Run("a header that states the canonical cwd matches a symlinked working directory", func(t *testing.T) {
		home := t.TempDir()
		real := existingDir(t, filepath.Join(home, "real"))
		link := filepath.Join(home, "link")
		require.NoError(t, os.Symlink(real, link))
		// omp classifies the canonical path, so the bucket is the real one.
		mine := writeSession(t, filepath.Join(home, ".omp", "agent", "sessions", "-real"), "mine", now,
			sessionHeader("mine", real), userMessage("Mine"))

		got, err := storedSessions(context.Background(), sessionQuery(link, home, nil))
		require.NoError(t, err)
		assert.Equal(t, []string{mine}, agenttest.Handles(got))
	})
}

// entryFor builds the directory entry the walk hands readSession for one file.
func entryFor(t *testing.T, path string) sessionstore.Entry {
	t.Helper()
	info, err := os.Stat(path)
	require.NoError(t, err)
	return sessionstore.Entry{Path: path, Name: filepath.Base(path), ModTime: info.ModTime()}
}

// titles reduces a result to its titles.
func titles(sessions []agent.StoredSession) []string {
	out := make([]string, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, session.Title)
	}
	return out
}

func TestReadSession(t *testing.T) {
	t.Parallel()
	cwd := "/work/project"
	read := func(t *testing.T, records ...string) (agent.StoredSession, bool) {
		t.Helper()
		path := writeSession(t, t.TempDir(), "s", time.Now(), records...)
		return readSession(entryFor(t, path), cwd, cwd)
	}

	cases := []struct {
		name    string
		records []string
		title   string
		skipped bool
	}{
		{
			name:    "the title slot wins",
			records: []string{`{"type":"title","v":1,"title":"Slot title","pad":"    "}`, `{"type":"session","id":"s","cwd":"/work/project","title":"Header title"}`, userMessage("Prompt")},
			title:   "Slot title",
		},
		{
			name:    "a blank slot clears the header's title",
			records: []string{`{"type":"title","v":1,"title":"   ","pad":"    "}`, `{"type":"session","id":"s","cwd":"/work/project","title":"Header title"}`, userMessage("Prompt")},
			title:   "Prompt",
		},
		{
			name:    "a slot with no title keeps the header's title",
			records: []string{`{"type":"title","v":1,"pad":"    "}`, `{"type":"session","id":"s","cwd":"/work/project","title":"Header title"}`, userMessage("Prompt")},
			title:   "Header title",
		},
		{
			name:    "the header's title with no slot",
			records: []string{`{"type":"session","id":"s","cwd":"/work/project","title":"Header title"}`, userMessage("Prompt")},
			title:   "Header title",
		},
		{
			name:    "a compaction's short summary before the first prompt",
			records: []string{sessionHeader("s", cwd), userMessage("Prompt"), assistantMessage, `{"type":"compaction","id":"c","summary":"long","shortSummary":"Compacted work"}`},
			title:   "Compacted work",
		},
		{
			name:    "the first user message, on its first line",
			records: []string{sessionHeader("s", cwd), `{"type":"message","message":{"role":"assistant","content":[{"type":"text","text":"Hi"}]}}`, userMessage("Fix the build\nand the tests"), userMessage("Second prompt")},
			title:   "Fix the build",
		},
		{
			name:    "an answered session with no title is kept untitled",
			records: []string{sessionHeader("s", cwd), `{"type":"message","message":{"role":"user","content":[{"type":"image","data":"x"}]}}`, assistantMessage},
			title:   "",
		},
		{
			name:    "an empty session is skipped",
			records: []string{sessionHeader("s", cwd)},
			skipped: true,
		},
		{
			name:    "another working directory is skipped",
			records: []string{sessionHeader("s", "/work/other"), userMessage("Prompt")},
			skipped: true,
		},
		{
			name:    "a header with no id is skipped",
			records: []string{`{"type":"session","id":" ","cwd":"/work/project"}`, userMessage("Prompt")},
			skipped: true,
		},
		{
			name:    "a file whose first two records hold no header is skipped",
			records: []string{`{"type":"title","v":1,"title":"x"}`, userMessage("Prompt"), sessionHeader("s", cwd)},
			skipped: true,
		},
		{
			name:    "a file that does not open with JSON is skipped",
			records: []string{`not json`, sessionHeader("s", cwd), userMessage("Prompt")},
			skipped: true,
		},
		{
			name:    "a broken record after the header is skipped",
			records: []string{sessionHeader("s", cwd), `{"type":"message","message":`, userMessage("Prompt")},
			title:   "Prompt",
		},
		{
			name:    "a message record with no message is skipped",
			records: []string{sessionHeader("s", cwd), `{"type":"message","id":"m0"}`, userMessage("Prompt")},
			title:   "Prompt",
		},
		{
			name:    "a compaction with a blank short summary gives way to the prompt",
			records: []string{sessionHeader("s", cwd), userMessage("Prompt"), `{"type":"compaction","id":"c","shortSummary":"  "}`},
			title:   "Prompt",
		},
		{
			name:    "a session with no reply keeps its prompt",
			records: []string{sessionHeader("s", cwd), userMessage("Unanswered")},
			title:   "Unanswered",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := read(t, tc.records...)
			if tc.skipped {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, tc.title, got.Title)
		})
	}

	t.Run("the canonical spelling of the working directory matches", func(t *testing.T) {
		path := writeSession(t, t.TempDir(), "s", time.Now(), sessionHeader("s", "/private/work/project"), userMessage("Prompt"))
		_, ok := readSession(entryFor(t, path), "/work/project", "/private/work/project")
		assert.True(t, ok)
	})
}

func TestEncodeSessionDirName(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the cases state POSIX paths; omp's Windows encoding differs only in its separators")
	}
	root := t.TempDir()
	home := existingDir(t, filepath.Join(root, "home"))
	temp := existingDir(t, filepath.Join(root, "tmp"))
	outside := existingDir(t, filepath.Join(root, "opt", "work:tree"))

	cases := []struct {
		name string
		cwd  string
		want string
	}{
		{name: "the home directory", cwd: home, want: "-"},
		{name: "under the home directory", cwd: existingDir(t, filepath.Join(home, "src", "app")), want: "-src-app"},
		{name: "the temp directory", cwd: temp, want: "-tmp"},
		{name: "under the temp directory", cwd: existingDir(t, filepath.Join(temp, "scratch")), want: "-tmp-scratch"},
		{name: "anywhere else", cwd: outside, want: "--" + replaceSeparators(outside[1:]) + "--"},
		// omp's own containment test refuses a first component that opens with "..".
		{name: "a first component that opens with two dots", cwd: existingDir(t, filepath.Join(home, "..cache")), want: "--" + replaceSeparators(filepath.Join(home, "..cache")[1:]) + "--"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, encodeSessionDirName(tc.cwd, home, temp))
		})
	}

	t.Run("a symlinked working directory takes its target's name", func(t *testing.T) {
		target := existingDir(t, filepath.Join(home, "real"))
		link := filepath.Join(outside, "link")
		require.NoError(t, os.Symlink(target, link))
		assert.Equal(t, "-real", encodeSessionDirName(link, home, temp))
	})

	t.Run("an absent home directory skips the home case", func(t *testing.T) {
		assert.Equal(t, "-tmp-scratch", encodeSessionDirName(filepath.Join(temp, "scratch"), "", temp))
	})

	t.Run("the colon is a separator", func(t *testing.T) {
		assert.Equal(t, "C--work-tree", replaceSeparators(`C:\work/tree`))
	})
}

func TestSessionsRoot(t *testing.T) {
	t.Parallel()
	const cwd = "/work/project"

	t.Run("without XDG", func(t *testing.T) {
		home := t.TempDir()
		cases := []struct {
			name string
			vars map[string]string
			want string
		}{
			{name: "the default profile", want: filepath.Join(home, ".omp", "agent", "sessions")},
			{name: "a renamed config root", vars: map[string]string{envConfigDir: ".omp-dev"}, want: filepath.Join(home, ".omp-dev", "agent", "sessions")},
			{name: "a named profile", vars: map[string]string{envProfile: "work"}, want: filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions")},
			{name: "the legacy profile variable", vars: map[string]string{envLegacyProfile: "work"}, want: filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions")},
			{name: "OMP_PROFILE wins over PI_PROFILE", vars: map[string]string{envProfile: "a", envLegacyProfile: "b"}, want: filepath.Join(home, ".omp", "profiles", "a", "agent", "sessions")},
			{name: "the profile named default", vars: map[string]string{envProfile: "default"}, want: filepath.Join(home, ".omp", "agent", "sessions")},
			{name: "a profile name with edge spaces", vars: map[string]string{envProfile: " work "}, want: filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions")},
			{name: "a blank profile name is the default profile", vars: map[string]string{envProfile: "   "}, want: filepath.Join(home, ".omp", "agent", "sessions")},
			{name: "a profile ignores the agent directory", vars: map[string]string{envProfile: "work", envAgentDir: "/elsewhere"}, want: filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions")},
			{name: "an absolute agent directory", vars: map[string]string{envAgentDir: "/srv/omp"}, want: filepath.Join("/srv/omp", "sessions")},
			{name: "a relative agent directory", vars: map[string]string{envAgentDir: "state"}, want: filepath.Join(cwd, "state", "sessions")},
			{name: "a profile name omp rejects", vars: map[string]string{envProfile: "Work"}, want: ""},
			{name: "a Windows device name", vars: map[string]string{envProfile: "nul.x"}, want: ""},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, sessionsRoot(sessionQuery(cwd, home, tc.vars), cwd))
			})
		}
	})

	t.Run("with XDG", func(t *testing.T) {
		if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
			t.Skip("omp moves its data under XDG only on Linux and macOS")
		}
		home := t.TempDir()
		xdg := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(xdg, "omp"), 0o755))

		assert.Equal(t, filepath.Join(xdg, "omp", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: xdg}), cwd),
			"the default profile moves once omp's directory exists")
		assert.Equal(t, filepath.Join(xdg, "omp", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: xdg, envAgentDir: filepath.Join(home, ".omp", "agent")}), cwd),
			"an agent directory that is the default one still moves")
		assert.Equal(t, filepath.Join("/srv/omp", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: xdg, envAgentDir: "/srv/omp"}), cwd),
			"another agent directory does not move")
		assert.Equal(t, filepath.Join(home, ".omp", "profiles", "work", "agent", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: xdg, envProfile: "work"}), cwd),
			"a profile does not move with the default profile")

		require.NoError(t, os.MkdirAll(filepath.Join(xdg, "omp", "profiles", "work"), 0o755))
		assert.Equal(t, filepath.Join(xdg, "omp", "profiles", "work", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: xdg, envProfile: "work"}), cwd),
			"a profile moves once its own directory exists")

		assert.Equal(t, filepath.Join(home, ".omp", "agent", "sessions"),
			sessionsRoot(sessionQuery(cwd, home, map[string]string{envXDGDataHome: t.TempDir()}), cwd),
			"an XDG directory with no omp directory changes nothing")
	})
}

func TestValidProfileName(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"work", "a", "team.dev", "x_1-2", "0"} {
		assert.Truef(t, validProfileName(name), "%q", name)
	}
	long := "a"
	for len(long) < 65 {
		long += "b"
	}
	for _, name := range []string{".", "..", "work.", "Work", "-work", "_x", "con", "COM1", "lpt9.txt", "a/b", long} {
		assert.Falsef(t, validProfileName(name), "%q", name)
	}
	assert.True(t, validProfileName(long[:64]), "64 characters is the maximum")
}

func TestTempDir(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		assert.Equal(t, `C:\Users\u\AppData\Local\Temp`, tempDir(sessionQuery("", "", map[string]string{"TEMP": `C:\Users\u\AppData\Local\Temp\`})))
		assert.Equal(t, `C:\`, tempDir(sessionQuery("", "", map[string]string{"TEMP": `C:\`})))
		assert.Equal(t, `C:\Windows\temp`, tempDir(sessionQuery("", "", map[string]string{"SystemRoot": `C:\Windows`})))
		return
	}
	assert.Equal(t, "/var/tmp", tempDir(sessionQuery("", "", map[string]string{"TMPDIR": "/var/tmp/"})))
	assert.Equal(t, "/scratch", tempDir(sessionQuery("", "", map[string]string{"TMP": "/scratch"})))
	assert.Equal(t, "/t", tempDir(sessionQuery("", "", map[string]string{"TEMP": "/t"})))
	assert.Equal(t, "/", tempDir(sessionQuery("", "", map[string]string{"TMPDIR": "/"})), "the root keeps its slash")
	assert.Equal(t, "/tmp", tempDir(sessionQuery("", "", nil)))
}
