//go:build unix

package cline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// fakeHistoryCLI writes a fake `cline` that records its arguments and the
// variables that keep it hermetic, and then runs body. The plugin states it by
// its absolute path, so no PATH entry -- and no shell profile -- can put the
// user's real Cline in its place.
type fakeHistoryCLI struct {
	program  string
	argsFile string
	output   string
}

func newFakeHistoryCLI(t *testing.T, body string) fakeHistoryCLI {
	t.Helper()
	dir := t.TempDir()
	cli := fakeHistoryCLI{
		program:  filepath.Join(dir, "cline"),
		argsFile: filepath.Join(dir, "args"),
		output:   filepath.Join(dir, "history.json"),
	}
	script := "#!/bin/sh\n" +
		`printf '%s\n' "$@" "HOME=$HOME" "` + envSessionBackendMode + `=$` + envSessionBackendMode + `" "` +
		envNoAutoUpdate + `=$` + envNoAutoUpdate + `" "CLINE_DIR=$CLINE_DIR" > '` + cli.argsFile + "'\n" +
		strings.ReplaceAll(body, "{{output}}", "'"+cli.output+"'") + "\n"
	require.NoError(t, os.WriteFile(cli.program, []byte(script), 0o755))
	return cli
}

func (c fakeHistoryCLI) plugin() clineProvider {
	return clineProvider{cli: launch.Binaries(c.program)}
}

func (c fakeHistoryCLI) args(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(c.argsFile)
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func (c fakeHistoryCLI) writeHistory(t *testing.T, entries []map[string]any) {
	t.Helper()
	data, err := json.Marshal(entries)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(c.output, data, 0o600))
}

func TestClineReadsItsSessionHistory(t *testing.T) {
	t.Parallel()
	cli := newFakeHistoryCLI(t, `cat {{output}}`)
	var home string
	agenttest.RequireReadsSessionStore(t, cli.plugin(), func(t *testing.T, fixtureHome, workingDir string) string {
		home = fixtureHome
		require.NoError(t, os.MkdirAll(workingDir, 0o755))
		cli.writeHistory(t, []map[string]any{
			{"sessionId": "1790258346189_other", "cwd": t.TempDir(), "isSubagent": false, "updatedAt": "2026-09-21T00:00:00Z"},
			{"sessionId": "1790258346189_zqp76", "cwd": resolvePath(workingDir), "isSubagent": false, "prompt": "<user_input mode=\"act\">say hi</user_input>", "updatedAt": "2026-09-20T00:00:00Z"},
		})
		return "1790258346189_zqp76"
	})
	assert.Equal(t, []string{
		"history", "--json", "--limit", "500",
		"HOME=" + home,
		envSessionBackendMode + "=" + sessionBackendLocal,
		envNoAutoUpdate + "=1",
		"CLINE_DIR=",
	}, cli.args(t), "the reader runs hermetically under the query's home")
}

func TestAFailingHistoryListsNothing(t *testing.T) {
	t.Parallel()
	for _, body := range []string{`exit 3`, `echo 'not json'`} {
		cli := newFakeHistoryCLI(t, body)
		sessions, err := cli.plugin().ListStoredSessions(context.Background(), agent.StoredSessionQuery{
			WorkingDir: t.TempDir(), Shell: "/bin/sh", HomeDir: t.TempDir(), Getenv: agenttest.FixtureEnv(nil),
		})
		require.NoError(t, err, body)
		assert.Empty(t, sessions, body)
	}
}

// The history command runs through the user's login shell, and the profile
// runs after the worker hands the shell its environment. A profile that
// exports the backend mode `auto` would let `cline history` attach to the
// user's own hub or start a detached one, so the shell wrapper sets the backend
// mode and the update switch after the profile. The shell reads the profile of
// the query's home, never the user's.
func TestHistoryKeepsItsLocalBackendWhenTheProfileExportsAnother(t *testing.T) {
	t.Parallel()
	cli := newFakeHistoryCLI(t, `echo '[]'`)
	home := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(home, ".profile"), []byte(
		"export "+envSessionBackendMode+"=auto\nexport "+envNoAutoUpdate+"=0\nexport CLINE_DIR=/profile/.cline\n"), 0o600))
	_, err := cli.plugin().ListStoredSessions(context.Background(), agent.StoredSessionQuery{
		WorkingDir: t.TempDir(), Shell: "/bin/sh", LoginShell: true, HomeDir: home, Getenv: agenttest.FixtureEnv(nil),
	})
	require.NoError(t, err)
	args := cli.args(t)
	assert.Contains(t, args, "CLINE_DIR=/profile/.cline", "the login shell runs the profile of the query's home")
	assert.Contains(t, args, envSessionBackendMode+"="+sessionBackendLocal, "the profile cannot move the command off its local backend")
	assert.Contains(t, args, envNoAutoUpdate+"=1", "the profile cannot turn the update check on")
}

// Cline records the directory as its process saw it, which can be a link to the
// directory that the picker lists, or the directory behind the picker's link.
func TestDirectorySessionsFollowALink(t *testing.T) {
	t.Parallel()
	dir := resolvePath(t.TempDir())
	link := filepath.Join(t.TempDir(), "link")
	require.NoError(t, os.Symlink(dir, link))
	entries := []historyEntry{{SessionID: "linked", Cwd: link, UpdatedAt: "2026-09-20T00:00:00Z"}}
	sessions := directorySessions(entries, dir, 10)
	require.Len(t, sessions, 1)
	assert.Equal(t, "linked", sessions[0].Handle)
	assert.Equal(t, dir, resolvePath(link), "the picker resolves its own directory the same way")
}
