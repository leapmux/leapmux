package amp

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/gitutil"
)

// Amp keeps its threads on its server, so no local store exists to read, and
// the one reader is the CLI's own `amp threads list --json`. It lists the whole
// account, newest first, and states each thread's workspace as a `tree` URI:
// the git top level of the directory the thread started in, or the directory
// itself outside a repository. The picker keeps the threads of the working
// directory's workspace.
//
// The call asks Amp's server, so it can be slow or fail: it runs under a
// deadline, and any failure is the empty list. Archived threads stay out, as
// Amp's own list keeps them out.

// threadsListTimeout caps the call to Amp's server.
const threadsListTimeout = 10 * time.Second

// threadsListLimit is how many of the account's newest threads the call
// fetches before the filter keeps one workspace's. It is the largest page Amp
// takes.
const threadsListLimit = 500

// maxThreadsListOutput caps what the reader keeps of the call's output. 500
// threads take a few hundred kilobytes.
const maxThreadsListOutput = 8 << 20

// threadEntry is one element of `amp threads list --json`.
type threadEntry struct {
	ID           string  `json:"id"`
	Title        string  `json:"title"`
	Updated      string  `json:"updated"`
	Tree         *string `json:"tree"`
	MessageCount int     `json:"messageCount"`
}

// storedSessions lists the threads of q's workspace. cli finds the Amp CLI.
func storedSessions(ctx context.Context, cli launch.Locator, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	root := workspaceRoot(ctx, q.WorkingDir)
	if root == "" {
		return nil, nil
	}
	output, err := runThreadsList(ctx, cli, q)
	if err != nil {
		slog.Warn("amp threads list failed", "working_dir", q.WorkingDir, "error", err)
		return nil, nil
	}
	entries, err := parseThreadList(output)
	if err != nil {
		slog.Warn("amp threads list output unreadable", "error", err)
		return nil, nil
	}
	return workspaceSessions(entries, root, q.EffectiveLimit()), nil
}

// workspaceSessions keeps the threads whose workspace is root, newest first,
// and at most limit of them.
func workspaceSessions(entries []threadEntry, root string, limit int) []agent.StoredSession {
	sessions := make([]agent.StoredSession, 0, len(entries))
	for _, entry := range entries {
		// A thread with no message holds nothing to resume.
		if entry.ID == "" || entry.MessageCount <= 0 || entry.Tree == nil || !sameWorkspace(*entry.Tree, root) {
			continue
		}
		updated, _ := time.Parse(time.RFC3339Nano, entry.Updated)
		sessions = append(sessions, agent.StoredSession{
			Handle:    entry.ID,
			Title:     entry.Title,
			UpdatedAt: updated,
		})
	}
	return agent.SortAndCapSessions(sessions, limit)
}

// workspaceRoot is the workspace Amp records for a thread started in dir: the
// git top level, or dir itself outside a repository. It resolves the links of
// both, as Amp does. It answers "" for a directory that does not exist.
func workspaceRoot(ctx context.Context, dir string) string {
	if dir == "" {
		return ""
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return ""
	}
	root := dir
	if toplevel := gitutil.GetToplevel(ctx, dir); toplevel != "" {
		root = toplevel
	}
	return resolvePath(root)
}

// resolvePath cleans a path and resolves its links when it exists.
func resolvePath(path string) string {
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// windowsDrivePath matches the path of a `file:` URI on Windows, which starts
// with a slash before the drive letter.
var windowsDrivePath = regexp.MustCompile(`^/[A-Za-z]:`)

// sameWorkspace reports whether a thread's `tree` URI identifies the workspace
// root.
func sameWorkspace(tree, root string) bool {
	parsed, err := url.Parse(tree)
	if err != nil || parsed.Scheme != "file" || parsed.Path == "" {
		return false
	}
	path := parsed.Path
	if windowsDrivePath.MatchString(path) {
		path = path[1:]
	}
	path = resolvePath(filepath.FromSlash(path))
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		// Both file systems ignore case by default, and git states the case that
		// the directory has on disk, which a working directory can spell apart.
		return strings.EqualFold(path, root)
	}
	return path == root
}

// parseThreadList reads the JSON list at the start of the output. Amp can print
// a line after it ("No threads found."), which the decoder leaves alone.
func parseThreadList(output []byte) ([]threadEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var entries []threadEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// runThreadsList runs `amp threads list --json` through the shell the worker
// launches agents through, so the CLI finds the same login as the agent. It
// returns the output after the wrapper's preamble. The shell wrapper turns the
// update check off after the user's profile runs, so a profile export cannot
// turn it back on.
func runThreadsList(ctx context.Context, cli launch.Locator, q agent.StoredSessionQuery) ([]byte, error) {
	return providerkit.RunCLI(ctx, providerkit.CLIRun{
		Locator:      cli,
		Label:        "Amp",
		Shell:        q.Shell,
		LoginShell:   q.LoginShell,
		Args:         []string{"threads", "list", "--json", "--limit", strconv.Itoa(threadsListLimit)},
		WorkingDir:   q.WorkingDir,
		StripEnvKeys: ampIdentityEnvKeys,
		SetEnv:       []string{envSkipUpdateCheck + "=1"},
		Env:          func(env []string) []string { return queryEnv(env, q) },
		Timeout:      threadsListTimeout,
		MaxOutput:    maxThreadsListOutput,
	})
}

// sessionEnvKeys are the variables that locate Amp's settings, its data, its
// login and its server.
var sessionEnvKeys = []string{
	envXDGConfigHome, "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
	envSettingsFile, "AMP_URL", "AMP_API_KEY",
}

// queryEnv makes the CLI's environment follow the query's seams, as each other
// provider's reader follows them to its store. The query's home becomes the
// CLI's home. When the query states its own environment, each variable of
// sessionEnvKeys comes from it alone, and the CLI gets no variable that the
// query does not state. A hermetic query therefore cannot reach the user's real
// Amp login through the worker's own environment.
//
// The service's query states the worker's own home and no environment, so for
// it the CLI's environment does not change.
func queryEnv(env []string, q agent.StoredSessionQuery) []string {
	if q.HomeDir != "" {
		env = envutil.PinEnv(env, homeEnvName()+"="+q.HomeDir)
	}
	if q.Getenv == nil {
		return env
	}
	env = envutil.FilterEnv(env, sessionEnvKeys...)
	for _, key := range sessionEnvKeys {
		if value := q.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}
