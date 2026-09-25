package amp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
)

// The settings file that the worker generates for each agent.
//
// Amp reads one global settings file, `--settings-file`, and merges the
// workspace's `.amp/settings.json` into it. The worker writes its own global
// file into the agent's directory, and never into the user's repository or the
// user's Amp configuration. The file starts from the user's own global settings
// and changes five of them:
//
//   - `amp.tools.disable` gains `ask_user_choice`. Amp rejects a question in
//     stream-JSON mode and ENDS the session, so the tool must not reach the
//     model.
//   - `amp.guardedFiles.allowlist` gains `/**`, which matches every file. Amp
//     asks about a guarded file (`.env`, `.git/`, `~/.config/`, and more) BEFORE
//     it reads any permission rule, through a dialog that stream-JSON mode
//     cannot answer, and the session ends. With the entry, the permission
//     rules decide each edit, so Ask shows a banner and Allow All allows it.
//     Amp's guard against a symlink to a guarded file does not read the list,
//     so it stays in force.
//   - `amp.updates.mode` is `disabled`. An update must not replace the CLI under
//     a running agent.
//   - `amp.dangerouslyAllowAll` is false, because it turns every permission rule
//     off, and LeapMux's permission mode must govern a LeapMux session.
//   - `amp.permissions` keeps the user's rules and ends with a catch-all rule:
//     a `delegate` rule for every tool, which runs the worker's helper. A user
//     rule that says `ask` delegates too, because execute mode turns `ask` into
//     a silent refusal. The rules are the same in both permission modes (see
//     permissionRules). A user rule that allows, rejects or delegates a call
//     still decides it.
//
// Amp merges the workspace's `.amp/settings.json` OVER this file, and its
// `amp.permissions` rules come first. The worker does not change that file,
// because it belongs to the repository. A workspace `ask` or `reject` rule
// refuses its calls without a banner, which is safe. A workspace setting that
// allows a call without a banner makes the agent refuse the turn in Ask mode
// (see workspace_settings.go).
//
// The worker writes the file before each process starts, so an edit of the
// user's own settings reaches the next process. A change of the permission mode
// writes nothing.

// settingsFileName is the generated settings file inside the agent's directory.
const settingsFileName = "settings.json"

// maxUserSettingsBytes caps the user's own settings file. A settings file holds
// a few kilobytes, so a larger one is not a file the user wrote for Amp.
const maxUserSettingsBytes = 4 << 20

// userSettingsPath returns the settings file that Amp itself reads when its
// command line gives no `--settings-file`: AMP_SETTINGS_FILE when it is set, else
// `$XDG_CONFIG_HOME/amp/settings.json` (or `~/.config/amp/settings.json`), and
// the `settings.jsonc` beside it when only that one exists.
func userSettingsPath(getenv func(string) string, home string) string {
	if path := strings.TrimSpace(getenv(envSettingsFile)); path != "" {
		return path
	}
	configHome := strings.TrimSpace(getenv(envXDGConfigHome))
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	path := filepath.Join(configHome, "amp", "settings.json")
	if fileExists(path) {
		return path
	}
	if jsonc := filepath.Join(configHome, "amp", "settings.jsonc"); fileExists(jsonc) {
		return jsonc
	}
	return path
}

// userSettingsEnvNames are the variables that locate the user's settings file:
// the two that userSettingsPath reads, and the home of each platform, which
// Node reads for the default (HOME, and USERPROFILE on Windows).
var userSettingsEnvNames = []string{envSettingsFile, envXDGConfigHome, "HOME", "USERPROFILE"}

// homeEnvName is the variable that holds the user's home for Amp, which is a
// Node program: os.homedir() reads USERPROFILE on Windows and HOME elsewhere.
func homeEnvName() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// readSettingsFile reads one Amp settings file: the user's own, or a
// workspace's. An absent file is an empty set of settings, exactly as Amp reads
// it.
//
// The path must be a regular file of at most maxUserSettingsBytes. The worker
// holds sendMu while it reads, so a FIFO would block the agent for ever, and a
// huge file would fill the worker's memory. The check comes before the open,
// because the open of a FIFO blocks too.
func readSettingsFile(path string) (map[string]json.RawMessage, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Amp settings file %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("the Amp settings file %s is not a regular file", path)
	}
	if info.Size() > maxUserSettingsBytes {
		return nil, fmt.Errorf("the Amp settings file %s is larger than %d bytes", path, maxUserSettingsBytes)
	}
	text, err := readAtMost(path, maxUserSettingsBytes)
	if err != nil {
		return nil, fmt.Errorf("read the Amp settings file %s: %w", path, err)
	}
	if len(bytes.TrimSpace(text)) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	// Amp reads the file as UTF-8 text and its JSONC parser refuses a leading
	// byte-order mark, so the worker refuses it too, with the reason.
	if bytes.HasPrefix(text, []byte("\xef\xbb\xbf")) {
		return nil, fmt.Errorf("the Amp settings file %s starts with a byte-order mark, which Amp does not accept. Save the file as UTF-8 without one", path)
	}
	plain, err := jsoncToJSON(text)
	if err != nil {
		return nil, fmt.Errorf("read the Amp settings file %s: %w", path, err)
	}
	settings := map[string]json.RawMessage{}
	if err := json.Unmarshal(plain, &settings); err != nil {
		return nil, fmt.Errorf("the Amp settings file %s is not a JSON object: %w", path, err)
	}
	return settings, nil
}

// readAtMost reads the file at path, and refuses one that holds more than limit
// bytes. The limit applies to the read itself, so a file that grows after the
// check of its size cannot pass it.
func readAtMost(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	text, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(text)) > limit {
		return nil, fmt.Errorf("the file is larger than %d bytes", limit)
	}
	return text, nil
}

// generateSettings returns the settings file for one agent: the user's
// settings, with the five changes the file comment lists.
func generateSettings(user map[string]json.RawMessage, helperProgram string) ([]byte, error) {
	settings := make(map[string]any, len(user)+5)
	for key, value := range user {
		settings[key] = value
	}
	settings[settingToolsDisable] = withListEntry(settingToolsDisable, user[settingToolsDisable], toolAskUserChoice)
	settings[settingGuardedFilesAllowlist] = withListEntry(settingGuardedFilesAllowlist, user[settingGuardedFilesAllowlist], guardedFilesCatchAll)
	settings[settingUpdatesMode] = updatesDisabled
	settings[settingDangerouslyAllowAll] = false
	settings[settingPermissions] = permissionRules(user[settingPermissions], helperProgram)
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode the Amp settings: %w", err)
	}
	return append(encoded, '\n'), nil
}

// withListEntry returns the user's list setting key, from raw, with entry added
// when the list lacks it. Amp ignores a value that is not a list of strings,
// with a warning, so this function drops it too.
func withListEntry(key string, raw json.RawMessage, entry string) []string {
	var list []string
	if len(raw) > 0 && json.Unmarshal(raw, &list) != nil {
		slog.Warn("amp ignores a list setting that is not a list of strings", "setting", key)
		list = nil
	}
	if slices.Contains(list, entry) {
		return list
	}
	return append(list, entry)
}

// permissionRules is the user's `amp.permissions` list with each `ask` rule
// turned into a delegation to the helper, followed by the catch-all delegation
// to the helper.
//
// The rules delegate in BOTH permission modes. The helper asks the agent, and
// the agent decides from its current mode (see decidePermission), so a switch
// of the mode applies to the very next call.
//
// A file that allowed every call in Allow All is possible, but it would break
// that guarantee. Amp reads a replaced settings file while it runs: a probe of
// the real CLI against the mock service showed that Amp polls the file every
// 1000 ms and applies it after a 100 ms debounce. So after a switch from Allow
// All to Ask, a call could still run without a banner for up to about 1.1 s,
// until Amp reads the rewritten file. The delegation costs one run of the helper
// for each call in Allow All, and the permission mode stays exact.
//
// A rule this code cannot read -- a value that is not an object -- stays as it
// is, and Amp judges it.
func permissionRules(raw json.RawMessage, helperProgram string) []any {
	delegate := func(fields map[string]any) map[string]any {
		fields["action"] = ruleActionDelegate
		fields["to"] = helperProgram
		return fields
	}
	var rules []json.RawMessage
	if len(raw) > 0 && json.Unmarshal(raw, &rules) != nil {
		slog.Warn("amp ignores a permissions setting that is not a list of rules")
		rules = nil
	}
	out := make([]any, 0, len(rules)+1)
	for _, rule := range rules {
		var fields map[string]json.RawMessage
		if json.Unmarshal(rule, &fields) != nil {
			out = append(out, rule)
			continue
		}
		var action string
		if json.Unmarshal(fields["action"], &action) == nil && action == ruleActionAsk {
			rewritten := make(map[string]any, len(fields)+1)
			for key, value := range fields {
				rewritten[key] = value
			}
			out = append(out, delegate(rewritten))
			continue
		}
		out = append(out, rule)
	}
	return append(out, delegate(map[string]any{"tool": ruleToolAll}))
}

// writeSettings writes the agent's settings file from the user's settings at
// userPath, and returns the file's path.
func (c launchConfig) writeSettings(userPath string) (string, error) {
	user, err := readSettingsFile(userPath)
	if err != nil {
		return "", err
	}
	content, err := generateSettings(user, c.helperProgram)
	if err != nil {
		return "", err
	}
	path := filepath.Join(c.stateDir, settingsFileName)
	// The user's settings can hold secrets (an MCP server's environment), so
	// only the owner can read the copy, as only the owner can read the
	// directory. atomicfile replaces the file in one step, so Amp, which
	// watches its settings file, never reads a half-written one.
	if err := atomicfile.WriteFile(path, content, 0o600); err != nil {
		return "", fmt.Errorf("write the Amp settings: %w", err)
	}
	return path, nil
}

// shellEnvTimeout limits the probe of the user's shell. A login profile can be
// slow, and the probe waits for it once for each agent.
const shellEnvTimeout = 30 * time.Second

// userSettingsFile returns the user's settings file at the path where Amp finds
// it: from the variables that the user's shell sets up, because the shell starts
// Amp.
//
// The agent reads the shell once, and keeps what it read. A probe that
// establishes nothing falls back to the worker's own environment for this start,
// and the next start probes again.
func (a *Agent) userSettingsFile() string {
	a.userEnvMu.Lock()
	defer a.userEnvMu.Unlock()
	if !a.userEnvRead && a.launch.shellEnv != nil {
		ctx, cancel := context.WithTimeout(a.ctx, shellEnvTimeout)
		values, result := a.launch.shellEnv(ctx, userSettingsEnvNames)
		cancel()
		if result == launch.ProbeYes {
			a.userEnvRead = true
			a.userGetenv = func(name string) string { return values[name] }
			a.userHome = values[homeEnvName()]
			if a.userHome == "" {
				a.userHome = a.launch.home
			}
		} else {
			slog.Warn("amp could not read the shell environment. The user settings come from the worker's environment.", "agent_id", a.agentID)
		}
	}
	if !a.userEnvRead {
		return userSettingsPath(a.launch.getenv, a.launch.home)
	}
	return userSettingsPath(a.userGetenv, a.userHome)
}
