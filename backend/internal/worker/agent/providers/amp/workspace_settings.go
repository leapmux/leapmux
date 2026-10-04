package amp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// A repository can change how Amp decides a tool call, and the settings file
// that the worker generates cannot stop it:
//
//   - Amp merges the workspace settings file OVER the generated one. A
//     workspace `amp.dangerouslyAllowAll: true` wins over the generated false,
//     and Amp then allows every call before it reads any rule. The workspace
//     rules of `amp.permissions` come FIRST, so a workspace `allow` rule, or a
//     `delegate` rule to a program of the repository, decides a call before
//     the delegation to LeapMux.
//   - Amp loads each plugin of the workspace's `.amp/plugins/` as code. A
//     plugin runs when Amp starts, and its `tool.call` answer (`modify` or
//     `synthesize`) wins over the permission rules' refusal.
//
// Amp offers no supported way to ignore the workspace in execute mode: the CLI
// treats every workspace as trusted, and its PLUGINS filter also turns off the
// permission plugin that runs the delegation. So in Ask mode the agent refuses
// a turn, and starts no process, while the workspace holds any of these. It
// checks before each new turn and before each process start, so a file that a
// turn writes refuses the next turn. Allow All refuses nothing, because the
// user accepted every call.

// workspaceSearchLimit caps the directories of the walk, as Amp caps its own.
const workspaceSearchLimit = 100

// workspaceCheckTimeout caps the git call that finds the workspace's top level.
const workspaceCheckTimeout = 10 * time.Second

// workspaceBypass is one thing in a workspace that lets a tool call run
// without LeapMux's banner.
type workspaceBypass struct {
	// path is the settings file or the plugin.
	path string
	// reason states what the file does, in words the user can act on.
	reason string
}

// workspaceBypassError refuses a turn in Ask mode.
type workspaceBypassError struct {
	bypasses []workspaceBypass
}

func (e *workspaceBypassError) Error() string {
	var b strings.Builder
	b.WriteString("Amp would run tool calls without the LeapMux permission banner in this workspace, so LeapMux did not send the message:")
	for _, bypass := range e.bypasses {
		fmt.Fprintf(&b, "\n- %s %s.", bypass.path, bypass.reason)
	}
	b.WriteString("\nRemove these from the workspace, or set Permissions to Allow All to accept every tool call.")
	return b.String()
}

// checkWorkspace refuses a turn or a process start in Ask mode when the
// workspace of the working directory holds a bypass. Any mode other than Allow
// All checks, so an unknown mode fails safe.
func (a *Agent) checkWorkspace() error {
	a.mu.Lock()
	mode := a.permissionMode
	a.mu.Unlock()
	if mode == contracts.AmpPermissionModeAllowAll {
		return nil
	}
	ctx, cancel := context.WithTimeout(a.ctx, workspaceCheckTimeout)
	defer cancel()
	if bypasses := workspaceBypasses(ctx, a.launch.opts.WorkingDir, a.launch.helperProgram); len(bypasses) > 0 {
		return &workspaceBypassError{bypasses: bypasses}
	}
	return nil
}

// workspaceBypasses returns every bypass in the workspace of dir. helper is the
// program of the LeapMux delegation, which a workspace rule may delegate to.
func workspaceBypasses(ctx context.Context, dir, helper string) []workspaceBypass {
	search := searchWorkspace(ctx, dir)
	var bypasses []workspaceBypass
	if path := findWorkspaceSettings(search); path != "" {
		bypasses = append(bypasses, settingsBypasses(path, helper)...)
	}
	return append(bypasses, pluginBypasses(search.rootCandidates())...)
}

// workspaceSearch is where Amp looks for the settings and the plugins of a
// workspace.
type workspaceSearch struct {
	// dirs are the directories of the walk, nearest first.
	dirs []string
	// fallback is the root that Amp takes when its walk ends before it reaches
	// the git top level, for example when the walk stops at
	// workspaceSearchLimit: the top level. Amp then reads `.amp/settings.json`
	// there, never `.amp/settings.jsonc`, and loads the plugins there. It is ""
	// when the walk reached the top level.
	fallback string
}

// rootCandidates returns every directory that Amp can take as the workspace
// root: each directory of the walk, and the fallback root.
func (s workspaceSearch) rootCandidates() []string {
	if s.fallback == "" {
		return s.dirs
	}
	return append(slices.Clip(s.dirs), s.fallback)
}

// searchWorkspace returns where Amp looks for the workspace settings of dir. The
// walk goes from the working directory up to the git top level, nearest first,
// or holds the working directory alone outside a repository. Amp starts from
// its process's working directory, which is the physical path, so the walk
// starts from dir with its links resolved.
func searchWorkspace(ctx context.Context, dir string) workspaceSearch {
	if dir == "" {
		return workspaceSearch{}
	}
	start := resolvePath(dir)
	top := start
	if toplevel := gitutil.GetToplevel(ctx, start); toplevel != "" {
		top = resolvePath(toplevel)
	}
	search := workspaceSearch{dirs: make([]string, 0, 4)}
	reachedTop := false
	for current := start; len(search.dirs) < workspaceSearchLimit; {
		search.dirs = append(search.dirs, current)
		if samePath(current, top) {
			reachedTop = true
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if !reachedTop {
		search.fallback = top
	}
	return search
}

// samePath reports whether two resolved paths are the same directory. macOS
// and Windows ignore case by default.
func samePath(a, b string) bool {
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// findWorkspaceSettings returns the workspace settings file that Amp reads: in
// the nearest directory of the walk, `.amp/settings.json`, else
// `.amp/settings.jsonc`. When the walk holds neither, it is
// `.amp/settings.json` of the fallback root. Amp takes any path that exists, so
// this does too. "" means none exists.
func findWorkspaceSettings(search workspaceSearch) string {
	for _, dir := range search.dirs {
		for _, name := range []string{"settings.json", "settings.jsonc"} {
			if path := filepath.Join(dir, ".amp", name); pathExists(path) {
				return path
			}
		}
	}
	if search.fallback != "" {
		if path := filepath.Join(search.fallback, ".amp", "settings.json"); pathExists(path) {
			return path
		}
	}
	return ""
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ampRule takes the fields of one permission rule that the check reads.
type ampRule struct {
	Tool   string `json:"tool"`
	Action string `json:"action"`
	To     string `json:"to"`
}

// settingsBypasses returns the bypasses of one workspace settings file. A file
// that the worker cannot read counts as one: nothing then shows that it is
// safe, and Amp fails on it too.
func settingsBypasses(path, helper string) []workspaceBypass {
	settings, err := readSettingsFile(path)
	if err != nil {
		return []workspaceBypass{{path: path, reason: fmt.Sprintf("cannot be read, so LeapMux cannot check it: %v", unwrapAll(err))}}
	}
	var bypasses []workspaceBypass
	// Amp allows every call only for the JSON value true (`=== true`).
	var allowAll bool
	if json.Unmarshal(settings[settingDangerouslyAllowAll], &allowAll) == nil && allowAll {
		bypasses = append(bypasses, workspaceBypass{path: path, reason: fmt.Sprintf("sets %q to true, which allows every call", settingDangerouslyAllowAll)})
	}
	// Amp ignores a value that is not a list, and each element that is not a
	// valid rule. A rule whose action is not a string is one that Amp drops.
	var rules []json.RawMessage
	if json.Unmarshal(settings[settingPermissions], &rules) != nil {
		return bypasses
	}
	for _, raw := range rules {
		var rule ampRule
		if json.Unmarshal(raw, &rule) != nil {
			continue
		}
		switch rule.Action {
		case ruleActionAllow:
			bypasses = append(bypasses, workspaceBypass{path: path, reason: fmt.Sprintf("holds an %q rule for %q with the action %q, which allows the calls that it matches", settingPermissions, rule.Tool, ruleActionAllow)})
		case ruleActionDelegate:
			if rule.To == helper {
				continue
			}
			target := "no program"
			if rule.To != "" {
				target = fmt.Sprintf("%q", rule.To)
			}
			bypasses = append(bypasses, workspaceBypass{path: path, reason: fmt.Sprintf("holds an %q rule for %q with the action %q to %s, which decides the calls in place of LeapMux", settingPermissions, rule.Tool, ruleActionDelegate, target)})
		}
	}
	return bypasses
}

// pluginBypasses returns every plugin that Amp would load from the
// `.amp/plugins/` of dirs. Amp reads plugins from the workspace root alone, and
// the caller gives every directory that can be that root (see
// workspaceSearch.rootCandidates), so the check cannot miss one.
//
// Amp loads a file whose name ends in `.ts` or `.js` (a test file excepted),
// and a directory that holds an `index.ts` or `index.js` file.
func pluginBypasses(dirs []string) []workspaceBypass {
	var bypasses []workspaceBypass
	for _, dir := range dirs {
		pluginsDir := filepath.Join(dir, ".amp", "plugins")
		entries, err := os.ReadDir(pluginsDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(pluginsDir, entry.Name())
			if !isPlugin(entry, path) {
				continue
			}
			bypasses = append(bypasses, workspaceBypass{path: path, reason: "is an Amp plugin, which runs as code when Amp starts and can decide tool calls in place of LeapMux"})
		}
	}
	return bypasses
}

// isPlugin reports whether Amp loads the entry of a plugins directory.
func isPlugin(entry os.DirEntry, path string) bool {
	name := entry.Name()
	if !entry.IsDir() {
		return (strings.HasSuffix(name, ".ts") || strings.HasSuffix(name, ".js")) &&
			!strings.HasSuffix(name, ".test.ts") && !strings.HasSuffix(name, ".test.js")
	}
	for _, index := range []string{"index.ts", "index.js"} {
		if info, err := os.Lstat(filepath.Join(path, index)); err == nil && info.Mode().IsRegular() {
			return true
		}
	}
	return false
}

// unwrapAll returns the innermost error of err, whose text is the cause that
// the user can act on. The outer texts repeat the file path.
func unwrapAll(err error) error {
	for {
		inner := errors.Unwrap(err)
		if inner == nil {
			return err
		}
		err = inner
	}
}
