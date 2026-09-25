//go:build unix

package amp

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
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// fakeThreadsCLI writes a fake `amp` that records its arguments and one
// environment variable, and then runs body. The plugin states it by its
// absolute path, so no PATH entry -- and no shell profile -- can put the user's
// real Amp CLI in its place.
type fakeThreadsCLI struct {
	program  string
	argsFile string
	output   string
}

func newFakeThreadsCLI(t *testing.T, body string) fakeThreadsCLI {
	t.Helper()
	dir := t.TempDir()
	cli := fakeThreadsCLI{
		program:  filepath.Join(dir, "amp"),
		argsFile: filepath.Join(dir, "args"),
		output:   filepath.Join(dir, "threads.json"),
	}
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" "` + envSkipUpdateCheck + `=$` + envSkipUpdateCheck + `" > '` + cli.argsFile + "'\n" +
		strings.ReplaceAll(body, "{{output}}", "'"+cli.output+"'") + "\n"
	require.NoError(t, os.WriteFile(cli.program, []byte(script), 0o755))
	return cli
}

func (c fakeThreadsCLI) plugin() ampProvider {
	return ampProvider{cli: launch.Binaries(c.program)}
}

func (c fakeThreadsCLI) args(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(c.argsFile)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (c fakeThreadsCLI) writeThreads(t *testing.T, entries []map[string]any) {
	t.Helper()
	data, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(c.output, data, 0o600))
}

func sessionQuery(dir string) agent.StoredSessionQuery {
	return agent.StoredSessionQuery{WorkingDir: dir, Shell: "/bin/sh"}
}

func TestAmpReadsItsThreadList(t *testing.T) {
	t.Parallel()
	cli := newFakeThreadsCLI(t, `cat {{output}}; echo "No more threads."`)
	agenttest.RequireReadsSessionStore(t, cli.plugin(), func(t *testing.T, _, workingDir string) string {
		require.NoError(t, os.MkdirAll(workingDir, 0o755))
		cli.writeThreads(t, []map[string]any{
			{"id": "T-other", "title": "Elsewhere", "updated": "2026-09-21T00:00:00Z", "tree": fileURI(t.TempDir()), "messageCount": 5},
			{"id": "T-019a2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b", "title": "Fix the build", "updated": "2026-09-20T00:00:00Z", "tree": fileURI(resolvePath(workingDir)), "messageCount": 5},
		})
		return "T-019a2b3c-4d5e-7f60-8a9b-0c1d2e3f4a5b"
	})
	assert.Equal(t, []string{"threads", "list", "--json", "--limit", "500", envSkipUpdateCheck + "=1"}, cli.args(t))
}

func TestAmpThreadListKeepsOnlyTheWorkspace(t *testing.T) {
	t.Parallel()
	cli := newFakeThreadsCLI(t, `cat {{output}}`)
	dir := t.TempDir()
	cli.writeThreads(t, []map[string]any{
		{"id": "T-here", "title": "Here", "updated": "2026-09-20T00:00:00Z", "tree": fileURI(resolvePath(dir)), "messageCount": 2},
		{"id": "T-there", "title": "There", "updated": "2026-09-21T00:00:00Z", "tree": fileURI(t.TempDir()), "messageCount": 2},
		{"id": "T-no-tree", "title": "No tree", "updated": "2026-09-21T00:00:00Z", "tree": nil, "messageCount": 2},
	})
	sessions, err := cli.plugin().ListStoredSessions(context.Background(), sessionQuery(dir))
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	assert.Equal(t, "T-here", sessions[0].Handle)
	assert.Equal(t, "Here", sessions[0].Title)
}

// Every failure of the call is the empty list, never an error: the picker still
// offers what the worker's own database recorded.
func TestAmpThreadListFailuresAreEmpty(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"the CLI fails":          `echo "Error: Not logged in" >&2; exit 1`,
		"the output is not JSON": `echo "No threads found."`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cli := newFakeThreadsCLI(t, body)
			sessions, err := cli.plugin().ListStoredSessions(context.Background(), sessionQuery(t.TempDir()))
			require.NoError(t, err)
			assert.Empty(t, sessions)
		})
	}
}

// The reader refuses output past the cap whole, even when its end would parse:
// it never holds more than the cap in memory.
func TestAmpThreadListRefusesOutputPastTheCap(t *testing.T) {
	t.Parallel()
	cli := newFakeThreadsCLI(t, `head -c 9000000 /dev/zero | tr '\0' ' '; cat {{output}}`)
	dir := t.TempDir()
	cli.writeThreads(t, []map[string]any{
		{"id": "T-here", "title": "Here", "updated": "2026-09-20T00:00:00Z", "tree": fileURI(resolvePath(dir)), "messageCount": 2},
	})
	sessions, err := cli.plugin().ListStoredSessions(context.Background(), sessionQuery(dir))
	require.NoError(t, err)
	assert.Empty(t, sessions)
}

// A call that does not answer ends with its deadline, and the list is empty.
func TestAmpThreadListEndsAtTheDeadline(t *testing.T) {
	t.Parallel()
	cli := newFakeThreadsCLI(t, `exec sleep 60`)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	sessions, err := cli.plugin().ListStoredSessions(ctx, sessionQuery(t.TempDir()))
	require.NoError(t, err)
	assert.Empty(t, sessions)
	assert.Less(t, time.Since(started), 30*time.Second, "the call does not wait for the CLI to finish")
}

// A query that states its own home and environment gets a CLI that reads them
// alone. The lister must never reach the developer's real Amp login from a
// hermetic query: HOME, the XDG directories and Amp's own variables come from
// the query, and the CLI gets no variable that the query does not state. Not
// parallel: it changes PATH and the process environment.
func TestAmpThreadListUsesTheQuerysHomeAndEnvironment(t *testing.T) {
	bin := t.TempDir()
	envFile := filepath.Join(t.TempDir(), "env")
	script := "#!/bin/sh\n" +
		`printf '%s\n' "HOME=$HOME" "XDG_CONFIG_HOME=$XDG_CONFIG_HOME" "XDG_DATA_HOME=$XDG_DATA_HOME" "AMP_API_KEY=$AMP_API_KEY" "AMP_URL=$AMP_URL" > '` + envFile + "'\n" +
		"printf '[]\\n'\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "amp"), []byte(script), 0o755))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_CONFIG_HOME", "/real/config")
	t.Setenv("XDG_DATA_HOME", "/real/data")
	t.Setenv("AMP_API_KEY", "the-real-key")
	t.Setenv("AMP_URL", "https://real.example")

	home := t.TempDir()
	got, err := ampProvider{}.ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: t.TempDir(),
		HomeDir:    home,
		Getenv:     envOf(map[string]string{"XDG_DATA_HOME": "/query/data"}),
		Shell:      "/bin/sh",
	})
	require.NoError(t, err)
	assert.Empty(t, got)
	data, err := os.ReadFile(envFile)
	require.NoError(t, err, "the CLI ran")
	assert.Equal(t, []string{
		"HOME=" + home,
		"XDG_CONFIG_HOME=",
		"XDG_DATA_HOME=/query/data",
		"AMP_API_KEY=",
		"AMP_URL=",
	}, strings.Split(strings.TrimSpace(string(data)), "\n"))
}

// The thread list runs through the user's login shell, and the profile runs
// after the worker hands the shell its environment. The shell wrapper sets the
// update switch after the profile, so a profile export cannot turn the update
// check back on. The shell reads the profile of the query's home, never the
// user's.
func TestAmpThreadListSkipsTheUpdateCheckWhenTheProfileExportsIt(t *testing.T) {
	t.Parallel()
	cli := newFakeThreadsCLI(t, `printf '[]\n'`)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"),
		[]byte("export "+envSkipUpdateCheck+"=0\n"), 0o600))
	_, err := cli.plugin().ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: t.TempDir(), Shell: "/bin/sh", LoginShell: true, HomeDir: home, Getenv: envOf(nil),
	})
	require.NoError(t, err)
	assert.Contains(t, cli.args(t), envSkipUpdateCheck+"=1", "the profile cannot turn the update check on")
}
