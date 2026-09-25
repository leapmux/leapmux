package cline

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/util/envutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// The session picker.
//
// Cline keeps its sessions in the user's data directory: a row of
// `db/sessions.db` and a directory of files for each one. The hub's
// `session.list` reads that table, but a hub runs only while an agent does, and
// the picker opens before any agent starts. So the reader is the CLI's own
// `cline history --json`, which lists the same rows through the same code
// (`listSessions`). It lists every workspace, newest first, and the picker keeps
// the root sessions of the working directory.
//
// The command runs with the local session backend (localRuntimeEnv), so it
// neither attaches to a running hub nor starts one. Any failure is the empty
// list.

// historyTimeout caps the command. It starts a login shell and a Bun process,
// and it reads a local database.
const historyTimeout = 15 * time.Second

// historyLimit is how many of the newest sessions the command lists before the
// filter keeps one directory's.
const historyLimit = 500

// maxHistoryOutput caps what the reader keeps of the command's output. 500
// records with their metadata take a few hundred kilobytes.
const maxHistoryOutput = 8 << 20

// historyEntry is one element of `cline history --json`.
type historyEntry struct {
	SessionID  string          `json:"sessionId"`
	Cwd        string          `json:"cwd"`
	IsSubagent bool            `json:"isSubagent"`
	Prompt     string          `json:"prompt"`
	StartedAt  string          `json:"startedAt"`
	EndedAt    string          `json:"endedAt"`
	UpdatedAt  string          `json:"updatedAt"`
	Metadata   historyMetadata `json:"metadata"`
}

// historyMetadata holds the metadata fields the picker reads.
type historyMetadata struct {
	Title string `json:"title"`
}

// storedSessions lists the root sessions of q's working directory. cli finds
// the Cline CLI.
func storedSessions(ctx context.Context, cli launch.Locator, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	dir := resolvePath(q.WorkingDir)
	if dir == "" {
		return nil, nil
	}
	output, err := providerkit.RunCLI(ctx, providerkit.CLIRun{
		Locator:    cli,
		Label:      "Cline",
		Shell:      q.Shell,
		LoginShell: q.LoginShell,
		Args:       []string{"history", "--json", "--limit", strconv.Itoa(historyLimit)},
		WorkingDir: q.WorkingDir,
		SetEnv:     localRuntimeEnv,
		Env:        func(env []string) []string { return historyEnv(env, q) },
		Timeout:    historyTimeout,
		MaxOutput:  maxHistoryOutput,
	})
	if err != nil {
		slog.Warn("cline history failed", "working_dir", q.WorkingDir, "error", err)
		return nil, nil
	}
	entries, err := parseHistory(output)
	if err != nil {
		slog.Warn("cline history output unreadable", "error", err)
		return nil, nil
	}
	return directorySessions(entries, dir, q.EffectiveLimit()), nil
}

// historyEnv is the environment that the command's shell starts with. It
// follows the query's seams, as each other provider's reader follows them to
// its store: the query's home becomes the command's home, and when the query
// states its own environment, each variable that locates Cline's data comes
// from it alone. A hermetic query therefore cannot reach the user's real Cline
// data through the worker's own environment. The shell wrapper sets
// localRuntimeEnv after the profile runs, so it is not here.
func historyEnv(env []string, q agent.StoredSessionQuery) []string {
	if q.HomeDir != "" {
		env = envutil.PinEnv(env, homeEnvName()+"="+q.HomeDir)
	}
	if q.Getenv == nil {
		return env
	}
	env = envutil.FilterEnv(env, dataEnvKeys...)
	for _, key := range dataEnvKeys {
		if value := q.Getenv(key); value != "" {
			env = append(env, key+"="+value)
		}
	}
	return env
}

// homeEnvName is the variable that states the home directory on this platform.
func homeEnvName() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
}

// parseHistory reads the JSON list at the start of the output. The CLI can
// print a line after it, which the decoder leaves alone.
func parseHistory(output []byte) ([]historyEntry, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var entries []historyEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// directorySessions keeps the root sessions that ran in dir, newest first, and
// at most limit of them.
func directorySessions(entries []historyEntry, dir string, limit int) []agent.StoredSession {
	sessions := make([]agent.StoredSession, 0, len(entries))
	for _, entry := range entries {
		// A subagent's and a teammate's session belongs to its root session, and
		// a resume of it alone would lose the conversation that started it.
		if entry.SessionID == "" || entry.IsSubagent || !samePath(entry.Cwd, dir) {
			continue
		}
		sessions = append(sessions, agent.StoredSession{
			Handle:    entry.SessionID,
			Title:     sessionTitle(entry),
			UpdatedAt: entryTime(entry),
		})
	}
	return agent.SortAndCapSessions(sessions, limit)
}

// sessionTitle is the title Cline stored for a session, or its first prompt
// without the wrapper Cline stores around it, on one line and capped: the
// prompt is the user's own text, of any length and any number of lines.
func sessionTitle(entry historyEntry) string {
	return sessionstore.TrimTitle(sessionstore.FirstNonBlank(entry.Metadata.Title, stripUserInput(entry.Prompt)))
}

// stripUserInput removes the `<user_input mode="...">...</user_input>` wrapper
// that Cline stores around each user message.
func stripUserInput(text string) string {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, userInputOpen) {
		return text
	}
	end := strings.IndexByte(text, '>')
	if end < 0 {
		return text
	}
	return strings.TrimSpace(strings.TrimSuffix(text[end+1:], userInputClose))
}

// entryTime is the last activity of a session: the update time, else the end,
// else the start.
func entryTime(entry historyEntry) time.Time {
	for _, value := range []string{entry.UpdatedAt, entry.EndedAt, entry.StartedAt} {
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}

// resolvePath cleans a path and resolves its links when it exists. It answers
// "" for an empty path.
func resolvePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	path = filepath.Clean(path)
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}

// samePath reports whether a recorded directory is dir. Both sides resolve
// their links first, because Cline records the directory as its process saw it.
func samePath(recorded, dir string) bool {
	recorded = resolvePath(recorded)
	if recorded == "" {
		return false
	}
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		// Both file systems ignore case by default.
		return strings.EqualFold(recorded, dir)
	}
	return recorded == dir
}
