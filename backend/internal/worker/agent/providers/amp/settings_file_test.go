package amp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

const testHelperProgram = "/opt/leapmux/leapmux"

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
}

func TestUserSettingsPath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()

	t.Run("AMP_SETTINGS_FILE wins", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, "/etc/amp.json", userSettingsPath(envOf(map[string]string{
			envSettingsFile:  " /etc/amp.json ",
			envXDGConfigHome: "/xdg",
		}), home))
	})
	t.Run("XDG_CONFIG_HOME moves the directory", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, filepath.Join("/xdg", "amp", "settings.json"),
			userSettingsPath(envOf(map[string]string{envXDGConfigHome: "/xdg"}), home))
	})
	t.Run("the home's .config by default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, filepath.Join(home, ".config", "amp", "settings.json"), userSettingsPath(envOf(nil), home))
	})
	t.Run("settings.jsonc when only it exists", func(t *testing.T) {
		t.Parallel()
		config := t.TempDir()
		writeFile(t, filepath.Join(config, "amp", "settings.jsonc"), `{}`)
		assert.Equal(t, filepath.Join(config, "amp", "settings.jsonc"),
			userSettingsPath(envOf(map[string]string{envXDGConfigHome: config}), home))
	})
	t.Run("settings.json before settings.jsonc", func(t *testing.T) {
		t.Parallel()
		config := t.TempDir()
		writeFile(t, filepath.Join(config, "amp", "settings.json"), `{}`)
		writeFile(t, filepath.Join(config, "amp", "settings.jsonc"), `{}`)
		assert.Equal(t, filepath.Join(config, "amp", "settings.json"),
			userSettingsPath(envOf(map[string]string{envXDGConfigHome: config}), home))
	})
	t.Run("a blank AMP_SETTINGS_FILE states no file", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, filepath.Join("/xdg", "amp", "settings.json"), userSettingsPath(envOf(map[string]string{
			envSettingsFile:  " \t",
			envXDGConfigHome: "/xdg",
		}), home))
	})
	t.Run("a blank XDG_CONFIG_HOME states no directory", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, filepath.Join(home, ".config", "amp", "settings.json"),
			userSettingsPath(envOf(map[string]string{envXDGConfigHome: "  "}), home))
	})
	t.Run("a directory is not a settings file", func(t *testing.T) {
		t.Parallel()
		config := t.TempDir()
		require.NoError(t, os.MkdirAll(filepath.Join(config, "amp", "settings.json"), 0o755))
		writeFile(t, filepath.Join(config, "amp", "settings.jsonc"), `{}`)
		assert.Equal(t, filepath.Join(config, "amp", "settings.jsonc"),
			userSettingsPath(envOf(map[string]string{envXDGConfigHome: config}), home))
	})
}

func TestReadUserSettings(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	settings, err := readSettingsFile(filepath.Join(dir, "absent.json"))
	require.NoError(t, err)
	assert.Empty(t, settings, "an absent file is an empty set of settings")

	writeFile(t, filepath.Join(dir, "blank.json"), " \n\t")
	settings, err = readSettingsFile(filepath.Join(dir, "blank.json"))
	require.NoError(t, err)
	assert.Empty(t, settings)

	writeFile(t, filepath.Join(dir, "jsonc.json"), "{\n  // the user's note\n  \"amp.notifications.enabled\": false,\n}\n")
	settings, err = readSettingsFile(filepath.Join(dir, "jsonc.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `false`, string(settings["amp.notifications.enabled"]))

	writeFile(t, filepath.Join(dir, "array.json"), `[1]`)
	_, err = readSettingsFile(filepath.Join(dir, "array.json"))
	assert.ErrorContains(t, err, "not a JSON object")

	writeFile(t, filepath.Join(dir, "broken.json"), `{"a": /* open`)
	_, err = readSettingsFile(filepath.Join(dir, "broken.json"))
	assert.ErrorContains(t, err, "does not close")

	_, err = readSettingsFile(dir)
	assert.ErrorContains(t, err, "not a regular file", "a directory cannot be read as settings")

	// Amp's JSONC parser refuses a leading byte-order mark, so the worker refuses
	// it too, and says why.
	writeFile(t, filepath.Join(dir, "bom.json"), "\ufeff{\"amp.notifications.enabled\": false}")
	_, err = readSettingsFile(filepath.Join(dir, "bom.json"))
	assert.ErrorContains(t, err, "byte-order mark")
}

// A settings file larger than the cap is not one a user writes, and the worker
// refuses it before it reads the whole file into memory.
func TestReadUserSettingsRefusesAFileLargerThanTheCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, path, `{"amp.notifications.enabled": false}`)
	require.NoError(t, os.Truncate(path, maxUserSettingsBytes+1))
	_, err := readSettingsFile(path)
	assert.ErrorContains(t, err, "larger than")
}

// A settings file of exactly the cap is one the worker reads.
func TestReadUserSettingsAcceptsAFileAtTheCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	content := `{"amp.notifications.enabled": false}`
	writeFile(t, path, content+strings.Repeat(" ", maxUserSettingsBytes-len(content)))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.EqualValues(t, maxUserSettingsBytes, info.Size())

	settings, err := readSettingsFile(path)
	require.NoError(t, err)
	assert.JSONEq(t, `false`, string(settings["amp.notifications.enabled"]))
}

// The limit holds for the read itself, so a file that grows after the check of
// its size cannot pass it.
func TestReadAtMost(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "settings.json")
	writeFile(t, path, "12345")

	text, err := readAtMost(path, 5)
	require.NoError(t, err)
	assert.Equal(t, "12345", string(text), "the reader reads a file of exactly the limit whole")

	_, err = readAtMost(path, 4)
	assert.ErrorContains(t, err, "larger than 4 bytes")

	_, err = readAtMost(filepath.Join(t.TempDir(), "absent.json"), 5)
	assert.ErrorIs(t, err, os.ErrNotExist)
}

// generated decodes a generated settings file.
func generated(t *testing.T, user map[string]json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	content, err := generateSettings(user, testHelperProgram)
	require.NoError(t, err)
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content, &out))
	return out
}

func TestGenerateSettingsFromNothing(t *testing.T) {
	t.Parallel()
	out := generated(t, map[string]json.RawMessage{})
	assert.JSONEq(t, `["ask_user_choice"]`, string(out[settingToolsDisable]))
	assert.JSONEq(t, `"disabled"`, string(out[settingUpdatesMode]))
	assert.JSONEq(t, `false`, string(out[settingDangerouslyAllowAll]))
	assert.JSONEq(t, `[{"tool":"*","action":"delegate","to":"/opt/leapmux/leapmux"}]`, string(out[settingPermissions]))
	assert.JSONEq(t, `["/**"]`, string(out[settingGuardedFilesAllowlist]))
	assert.Len(t, out, 5)
}

// Amp asks about a guarded file before it reads any permission rule, through a
// dialog that stream-JSON mode cannot answer, and the session ends. A catch-all
// allowlist entry sends every edit to the permission rules, so LeapMux's
// delegation decides it. The user's own entries stay.
func TestGenerateSettingsAllowlistsEveryGuardedFile(t *testing.T) {
	t.Parallel()
	out := generated(t, map[string]json.RawMessage{
		settingGuardedFilesAllowlist: json.RawMessage(`["~/.config/my-tool/**"]`),
	})
	assert.JSONEq(t, `["~/.config/my-tool/**","/**"]`, string(out[settingGuardedFilesAllowlist]))

	out = generated(t, map[string]json.RawMessage{settingGuardedFilesAllowlist: json.RawMessage(`["/**"]`)})
	assert.JSONEq(t, `["/**"]`, string(out[settingGuardedFilesAllowlist]), "the catch-all is not listed twice")

	out = generated(t, map[string]json.RawMessage{settingGuardedFilesAllowlist: json.RawMessage(`"/**"`)})
	assert.JSONEq(t, `["/**"]`, string(out[settingGuardedFilesAllowlist]), "a value that is not a list of strings is one Amp ignores")
}

// The catch-all pattern matches every absolute path under Amp's own matcher:
// Amp prefixes a pattern path with file://, turns ** into .* and matches the
// whole file URI. This test replays that translation.
func TestGuardedFilesCatchAllMatchesEveryFileURI(t *testing.T) {
	t.Parallel()
	pattern := regexp.MustCompile(`(?i)^` + strings.NewReplacer(".", `\.`, "**", ".*").Replace("file://"+guardedFilesCatchAll) + `$`)
	for _, uri := range []string{
		"file:///Users/me/work/.env",
		"file:///var/folders/ab/T/work/a.db",
		"file:///home/me/.claude/settings.json",
		"file:///c%3A/Users/me/.git/config",
		"file:///",
	} {
		assert.True(t, pattern.MatchString(uri), uri)
	}
}

func TestGenerateSettingsKeepsTheUsersSettings(t *testing.T) {
	t.Parallel()
	out := generated(t, map[string]json.RawMessage{
		"amp.mcpServers":            json.RawMessage(`{"db":{"command":"db-mcp","env":{"TOKEN":"x"}}}`),
		settingToolsDisable:         json.RawMessage(`["browser_navigate","ask_user_choice"]`),
		settingUpdatesMode:          json.RawMessage(`"auto"`),
		settingDangerouslyAllowAll:  json.RawMessage(`true`),
		"amp.git.commit.coauthor":   json.RawMessage(`false`),
		"amp.terminal.theme":        json.RawMessage(`"dark"`),
		"amp.internal.unknownValue": json.RawMessage(`{"nested":[1,2]}`),
	})
	assert.JSONEq(t, `{"db":{"command":"db-mcp","env":{"TOKEN":"x"}}}`, string(out["amp.mcpServers"]))
	assert.JSONEq(t, `false`, string(out["amp.git.commit.coauthor"]))
	assert.JSONEq(t, `{"nested":[1,2]}`, string(out["amp.internal.unknownValue"]))
	assert.JSONEq(t, `["browser_navigate","ask_user_choice"]`, string(out[settingToolsDisable]),
		"the question tool is not listed twice")
	assert.JSONEq(t, `"disabled"`, string(out[settingUpdatesMode]), "no update replaces the CLI under a running agent")
	assert.JSONEq(t, `false`, string(out[settingDangerouslyAllowAll]), "LeapMux's permission mode governs")
}

func TestGenerateSettingsDropsAToolListAmpIgnores(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"a string":                   `"browser_navigate"`,
		"a list that holds a number": `["browser_navigate", 1]`,
		"an object":                  `{"browser_navigate": true}`,
	} {
		out := generated(t, map[string]json.RawMessage{settingToolsDisable: json.RawMessage(value)})
		assert.JSONEqf(t, `["ask_user_choice"]`, string(out[settingToolsDisable]), "%s is not a list of strings", name)
	}

	out := generated(t, map[string]json.RawMessage{settingToolsDisable: json.RawMessage(`null`)})
	assert.JSONEq(t, `["ask_user_choice"]`, string(out[settingToolsDisable]), "a null list is an empty list")
}

func TestGenerateSettingsRewritesThePermissionRules(t *testing.T) {
	t.Parallel()
	out := generated(t, map[string]json.RawMessage{
		settingPermissions: json.RawMessage(`[
			{"tool":"shell_command","matches":{"command":"git status"},"action":"allow"},
			{"tool":"shell_command","matches":{"command":"rm *"},"action":"reject"},
			{"tool":"edit_file","action":"ask","context":"thread"},
			"not a rule",
			{"tool":"mcp__*","action":"delegate","to":"/usr/local/bin/my-policy"}
		]`),
	})
	assert.JSONEq(t, `[
		{"tool":"shell_command","matches":{"command":"git status"},"action":"allow"},
		{"tool":"shell_command","matches":{"command":"rm *"},"action":"reject"},
		{"tool":"edit_file","action":"delegate","context":"thread","to":"/opt/leapmux/leapmux"},
		"not a rule",
		{"tool":"mcp__*","action":"delegate","to":"/usr/local/bin/my-policy"},
		{"tool":"*","action":"delegate","to":"/opt/leapmux/leapmux"}
	]`, string(out[settingPermissions]), "the user's rules keep their order, an ask rule asks through LeapMux, and the catch-all comes last")
}

func TestGenerateSettingsDropsAPermissionListAmpIgnores(t *testing.T) {
	t.Parallel()
	out := generated(t, map[string]json.RawMessage{settingPermissions: json.RawMessage(`{"tool":"*"}`)})
	assert.JSONEq(t, `[{"tool":"*","action":"delegate","to":"/opt/leapmux/leapmux"}]`, string(out[settingPermissions]))
}

func TestWriteSettingsWritesAPrivateCopyIntoTheAgentDirectory(t *testing.T) {
	t.Parallel()
	config := t.TempDir()
	userFile := filepath.Join(config, "amp", "settings.json")
	writeFile(t, userFile, `{"amp.terminal.theme": "dark",}`)
	stateDir := t.TempDir()
	c := launchConfig{
		stateDir:      stateDir,
		helperProgram: testHelperProgram,
		getenv:        envOf(map[string]string{envXDGConfigHome: config}),
		home:          t.TempDir(),
	}

	path, err := c.writeSettings(userSettingsPath(c.getenv, c.home))
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(stateDir, settingsFileName), path)
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	var out map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(content, &out))
	assert.JSONEq(t, `"dark"`, string(out["amp.terminal.theme"]))
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "the copy can hold the user's secrets")
	}

	// The next process reads the user's edit.
	writeFile(t, userFile, `{"amp.terminal.theme": "light"}`)
	_, err = c.writeSettings(userSettingsPath(c.getenv, c.home))
	require.NoError(t, err)
	content, err = os.ReadFile(path)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(content, &out))
	assert.JSONEq(t, `"light"`, string(out["amp.terminal.theme"]))

	userContent, err := os.ReadFile(userFile)
	require.NoError(t, err)
	assert.JSONEq(t, `{"amp.terminal.theme": "light"}`, string(userContent), "the user's own file is never written")
	entries, err := os.ReadDir(stateDir)
	require.NoError(t, err)
	assert.Len(t, entries, 1, "no temporary file stays behind")
}

// A write into an agent directory that no longer exists fails with the reason,
// and creates nothing.
func TestWriteSettingsFailsWhenTheAgentDirectoryIsGone(t *testing.T) {
	t.Parallel()
	stateDir := filepath.Join(t.TempDir(), "gone")
	c := launchConfig{
		stateDir:      stateDir,
		helperProgram: testHelperProgram,
		getenv:        envOf(nil),
		home:          t.TempDir(),
	}
	_, err := c.writeSettings(userSettingsPath(c.getenv, c.home))
	assert.ErrorContains(t, err, "write the Amp settings")
	_, statErr := os.Stat(stateDir)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestWriteSettingsRefusesAnUnreadableUserFile(t *testing.T) {
	t.Parallel()
	config := t.TempDir()
	writeFile(t, filepath.Join(config, "amp", "settings.json"), `[`)
	c := launchConfig{
		stateDir:      t.TempDir(),
		helperProgram: testHelperProgram,
		getenv:        envOf(map[string]string{envXDGConfigHome: config}),
	}
	_, err := c.writeSettings(userSettingsPath(c.getenv, c.home))
	assert.ErrorContains(t, err, "settings.json")
}

// The agent reads the user's shell once, keeps what it read, and falls back to
// the worker's environment while the probe establishes nothing.
func TestUserSettingsFileReadsTheShellOnce(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.launch.getenv = envOf(map[string]string{envSettingsFile: "/worker/amp.json"})
	var probes int
	result := launch.ProbeUnknown
	h.agent.launch.shellEnv = func(_ context.Context, names []string) (map[string]string, launch.ProbeResult) {
		probes++
		assert.Equal(t, userSettingsEnvNames, names)
		return map[string]string{envSettingsFile: "/shell/amp.json"}, result
	}

	assert.Equal(t, "/worker/amp.json", h.agent.userSettingsFile(), "an inconclusive probe falls back to the worker")
	result = launch.ProbeYes
	assert.Equal(t, "/shell/amp.json", h.agent.userSettingsFile(), "the next start probes again")
	assert.Equal(t, "/shell/amp.json", h.agent.userSettingsFile())
	assert.Equal(t, 2, probes, "the agent keeps a probe that answered")
}

// The default settings file lies under the home that the shell states.
func TestUserSettingsFileUsesTheShellsHome(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.launch.shellEnv = func(context.Context, []string) (map[string]string, launch.ProbeResult) {
		return map[string]string{homeEnvName(): "/shell-home"}, launch.ProbeYes
	}
	assert.Equal(t, filepath.Join("/shell-home", ".config", "amp", "settings.json"), h.agent.userSettingsFile())
}

// A shell that states no home leaves the agent's own home in place. The other
// values of the shell still win over the worker's environment.
func TestUserSettingsFileKeepsTheAgentsHomeWhenTheShellStatesNone(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.launch.home = "/agent-home"
	h.agent.launch.getenv = envOf(map[string]string{envXDGConfigHome: "/worker-xdg"})
	h.agent.launch.shellEnv = func(context.Context, []string) (map[string]string, launch.ProbeResult) {
		return map[string]string{}, launch.ProbeYes
	}
	assert.Equal(t, filepath.Join("/agent-home", ".config", "amp", "settings.json"), h.agent.userSettingsFile(),
		"the shell stated no XDG_CONFIG_HOME, and the worker's own value does not reach the path")
}
