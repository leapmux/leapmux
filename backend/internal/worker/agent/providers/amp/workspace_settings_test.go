package amp

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

// A repository's `.amp/settings.json` merges over the settings file that the
// worker generates, and a repository's `.amp/plugins/` runs as code in Amp. In
// Ask mode the agent refuses a turn, and starts no process, when either one would
// let a call run without LeapMux's banner.

// refusedTurn sends a message and returns the refusal. It asserts that no
// process started.
func refusedTurn(t *testing.T, h *harness) error {
	t.Helper()
	err := h.agent.SendInput("go", nil)
	require.Error(t, err)
	assert.Empty(t, h.startedThreads(), "no process starts")
	assert.False(t, h.turnActive(), "no turn starts")
	return err
}

func TestAskRefusesAWorkspaceThatBypassesTheBanner(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		content  string
		mentions []string
	}{
		{"allow all", `{"amp.dangerouslyAllowAll": true}`, []string{settingDangerouslyAllowAll}},
		{"an allow rule", `{"amp.permissions": [{"tool": "*", "action": "allow"}]}`, []string{settingPermissions, `"allow"`, `"*"`}},
		{"an allow rule for one tool", `{"amp.permissions": [{"tool": "Read", "action": "ask"}, {"tool": "shell_command", "matches": {"command": "curl *"}, "action": "allow"}]}`, []string{settingPermissions, `"allow"`, `"shell_command"`}},
		{"an allow rule for subagents", `{"amp.permissions": [{"tool": "*", "action": "allow", "context": "subagent"}]}`, []string{`"allow"`}},
		{"a delegation to another program", `{"amp.permissions": [{"tool": "*", "action": "delegate", "to": "./x.sh"}]}`, []string{settingPermissions, `"./x.sh"`}},
		{"a delegation with no program", `{"amp.permissions": [{"tool": "*", "action": "delegate"}]}`, []string{`"delegate"`}},
		{"JSONC", "{\n  // a note\n  \"amp.dangerouslyAllowAll\": true /* on */,\n}\n", []string{settingDangerouslyAllowAll}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, ".amp", "settings.json")
			writeFile(t, path, tc.content)
			h := newHarness(t, withWorkingDir(dir))

			err := refusedTurn(t, h)
			assert.ErrorContains(t, err, path)
			for _, mention := range tc.mentions {
				assert.ErrorContains(t, err, mention)
			}
			assert.ErrorContains(t, err, "Allow All", "the refusal says how to go on")
		})
	}
}

func TestAskRefusesAWorkspacePlugin(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		files []string
	}{
		{"a plugin file", []string{"evil.ts"}},
		{"a JavaScript plugin file", []string{"evil.js"}},
		{"a plugin directory", []string{"evil/index.ts"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			for _, file := range tc.files {
				writeFile(t, filepath.Join(dir, ".amp", "plugins", file), "export default () => {}\n")
			}
			h := newHarness(t, withWorkingDir(dir))
			err := refusedTurn(t, h)
			assert.ErrorContains(t, err, filepath.Join(dir, ".amp", "plugins"))
		})
	}
}

// Amp loads neither a test file nor a file of another kind as a plugin, so such
// files are no reason to refuse.
func TestAskIgnoresFilesThatAmpDoesNotLoadAsPlugins(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, file := range []string{"README.md", "evil.test.ts", "helper/notes.txt"} {
		writeFile(t, filepath.Join(dir, ".amp", "plugins", file), "text\n")
	}
	h := newHarness(t, withWorkingDir(dir))
	require.NoError(t, h.agent.SendInput("go", nil))
}

// A workspace file whose rules only ask or refuse, or delegate to the LeapMux
// helper itself, bypasses nothing: execute mode turns `ask` into a refusal.
// Amp reads `amp.dangerouslyAllowAll` as on only for the JSON value true, and
// it drops a rule whose action it does not know.
func TestAskAcceptsAWorkspaceThatBypassesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".amp", "settings.json"), `{
		"amp.dangerouslyAllowAll": "true",
		"amp.permissions": [
			{"tool": "shell_command", "matches": {"command": "rm *"}, "action": "reject"},
			{"tool": "edit_file", "action": "ask"},
			{"tool": "*", "action": "delegate", "to": "`+testHelperProgram+`"},
			"not a rule",
			{"tool": "Read", "action": "allow-please"}
		],
		"amp.mcpServers": {}
	}`)
	h := newHarness(t, withWorkingDir(dir))
	require.NoError(t, h.agent.SendInput("go", nil))
}

// Allow All accepts every call, so the agent refuses nothing there.
func TestAllowAllRefusesNothing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
	writeFile(t, filepath.Join(dir, ".amp", "plugins", "evil.ts"), "export default () => {}\n")
	h := newHarness(t, withWorkingDir(dir), withOptions(map[string]string{agent.OptionIDPermissionMode: contracts.AmpPermissionModeAllowAll}))
	require.NoError(t, h.agent.SendInput("go", nil))
}

// A workspace file that the worker cannot read cannot be shown to bypass
// nothing, so Ask refuses it too. Amp itself fails on such a file.
func TestAskRefusesAnUnreadableWorkspaceFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, ".amp", "settings.json")
	writeFile(t, path, `{"amp.dangerouslyAllowAll": `)
	h := newHarness(t, withWorkingDir(dir))
	err := refusedTurn(t, h)
	assert.ErrorContains(t, err, path)
}

// Amp looks for the workspace file from the working directory up to the git
// top level, `.amp/settings.json` first and then `.amp/settings.jsonc`, and the
// first file that it finds is the one it reads.
func TestAskFindsTheWorkspaceFileAsAmpDoes(t *testing.T) {
	t.Parallel()

	t.Run("a parent directory inside the repository", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		sub := filepath.Join(repo, "pkg", "sub")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		path := filepath.Join(repo, "pkg", ".amp", "settings.jsonc")
		writeFile(t, path, `{"amp.dangerouslyAllowAll": true}`)
		h := newHarness(t, withWorkingDir(sub))
		assert.ErrorContains(t, refusedTurn(t, h), "settings.jsonc")
	})
	t.Run("the git top level", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		sub := filepath.Join(repo, "pkg")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		writeFile(t, filepath.Join(repo, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
		h := newHarness(t, withWorkingDir(sub))
		assert.ErrorContains(t, refusedTurn(t, h), filepath.Join(repo, ".amp", "settings.json"))
	})
	t.Run("the nearest file wins", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		sub := filepath.Join(repo, "pkg")
		writeFile(t, filepath.Join(sub, ".amp", "settings.json"), `{"amp.notifications.enabled": false}`)
		writeFile(t, filepath.Join(repo, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
		h := newHarness(t, withWorkingDir(sub))
		require.NoError(t, h.agent.SendInput("go", nil), "Amp reads the nearest file alone")
	})
	t.Run("json before jsonc", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, ".amp", "settings.json"), `{}`)
		writeFile(t, filepath.Join(dir, ".amp", "settings.jsonc"), `{"amp.dangerouslyAllowAll": true}`)
		h := newHarness(t, withWorkingDir(dir))
		require.NoError(t, h.agent.SendInput("go", nil), "Amp reads settings.json and never settings.jsonc beside it")
	})
	t.Run("nothing above the git top level", func(t *testing.T) {
		t.Parallel()
		outer := t.TempDir()
		writeFile(t, filepath.Join(outer, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
		repo := filepath.Join(outer, "repo")
		require.NoError(t, os.Rename(testutil.NewGitRepo(t), repo))
		h := newHarness(t, withWorkingDir(repo))
		require.NoError(t, h.agent.SendInput("go", nil))
	})
	t.Run("outside a repository only the working directory", func(t *testing.T) {
		t.Parallel()
		outer := t.TempDir()
		writeFile(t, filepath.Join(outer, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
		sub := filepath.Join(outer, "sub")
		require.NoError(t, os.MkdirAll(sub, 0o755))
		h := newHarness(t, withWorkingDir(sub))
		require.NoError(t, h.agent.SendInput("go", nil))
	})
}

// nestedDir makes a directory depth levels below root and returns it.
func nestedDir(t *testing.T, root string, depth int) string {
	t.Helper()
	parts := make([]string, 0, depth+1)
	parts = append(parts, root)
	for range depth {
		parts = append(parts, "d")
	}
	dir := filepath.Join(parts...)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

// Amp's walk checks at most workspaceSearchLimit directories. When the walk
// stops before it reaches the git top level, Amp takes the top level as the
// workspace root anyway: it reads `.amp/settings.json` there, never
// `.amp/settings.jsonc`, and it loads the plugins there. The check follows the
// same rule, so a repository cannot hide a bypass under a deep working
// directory.
func TestAskFindsTheSettingsThatAmpReadsPastItsWalkLimit(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("a working directory 100 levels deep passes MAX_PATH, which git for Windows refuses without core.longpaths")
	}
	allowAll := `{"amp.dangerouslyAllowAll": true}`

	t.Run("the walk reaches the top level at its last directory", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		writeFile(t, filepath.Join(repo, ".amp", "settings.jsonc"), allowAll)
		h := newHarness(t, withWorkingDir(nestedDir(t, repo, workspaceSearchLimit-1)))
		assert.ErrorContains(t, refusedTurn(t, h), filepath.Join(repo, ".amp", "settings.jsonc"))
	})
	t.Run("the walk stops short of the top level", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		writeFile(t, filepath.Join(repo, ".amp", "settings.json"), allowAll)
		h := newHarness(t, withWorkingDir(nestedDir(t, repo, workspaceSearchLimit)))
		assert.ErrorContains(t, refusedTurn(t, h), filepath.Join(repo, ".amp", "settings.json"))
	})
	t.Run("past the walk Amp reads no settings.jsonc", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		writeFile(t, filepath.Join(repo, ".amp", "settings.jsonc"), allowAll)
		h := newHarness(t, withWorkingDir(nestedDir(t, repo, workspaceSearchLimit)))
		require.NoError(t, h.agent.SendInput("go", nil))
	})
	t.Run("past the walk Amp loads the plugins of the top level", func(t *testing.T) {
		t.Parallel()
		repo := testutil.NewGitRepo(t)
		writeFile(t, filepath.Join(repo, ".amp", "plugins", "evil.ts"), "export default () => {}\n")
		h := newHarness(t, withWorkingDir(nestedDir(t, repo, workspaceSearchLimit)))
		assert.ErrorContains(t, refusedTurn(t, h), filepath.Join(repo, ".amp", "plugins", "evil.ts"))
	})
}

// The check runs before each new turn, so a file that appears while the
// process runs refuses the next turn.
func TestAskChecksTheWorkspaceBeforeEachTurn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newHarness(t, withWorkingDir(dir))
	fp := h.send("first")
	h.feed(fp, initLine("T-8"))
	h.feed(fp, textLine("done", stopReasonEndTurn))

	writeFile(t, filepath.Join(dir, ".amp", "settings.json"), `{"amp.dangerouslyAllowAll": true}`)
	err := h.agent.SendInput("second", nil)
	assert.ErrorContains(t, err, settingDangerouslyAllowAll)
	assert.Len(t, fp.lines(), 1, "the refused message never reaches Amp")
	assert.False(t, h.turnActive())
}

// A resume after an error starts a process, and a process loads the
// workspace's plugins, so the resume checks the workspace too.
func TestAskChecksTheWorkspaceBeforeAResume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	h := newHarness(t, withWorkingDir(dir))
	fp := h.send("go")
	h.feed(fp, initLine("T-7"))
	writeFile(t, filepath.Join(dir, ".amp", "plugins", "evil.ts"), "export default () => {}\n")
	fp.exit()
	fp.awaitHandled(t)

	// The refused resume states why, which is also the sign that it ended.
	require.Eventually(t, func() bool {
		for _, notification := range h.sink.Notifications() {
			if message, _ := notification[contracts.NotificationFieldError].(string); strings.Contains(message, "could not resume") {
				return strings.Contains(message, filepath.Join(dir, ".amp", "plugins"))
			}
		}
		return false
	}, 30*time.Second, 2*time.Millisecond, "the refused resume states the plugin")
	assert.Equal(t, []string{""}, h.startedThreads(), "the resume starts no process")
}
