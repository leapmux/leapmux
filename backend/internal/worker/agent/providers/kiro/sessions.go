package kiro

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Kiro's v3 engine keeps each session in a directory of its own, grouped by
// the workspace that the session ran in:
//
//	~/.kiro/sessions/<workspace key>/<session id>/session.json
//
// The workspace key is the first 16 hex digits of the SHA-256 of the
// workspace's normalized path. session.json states the id, the title, the
// workspace and the times. messages.jsonl beside it holds the conversation.
// The engine resolves `~` from HOME alone: KIRO_HOME moves the settings of the
// CLI, not the sessions of the engine.
//
// A workflow step is a session of its own in the same group. It states its
// workflow in `_meta.kiro.workflow`, and it is no conversation that a user
// continues, so the picker leaves it out.

// Kiro's store layout.
const (
	kiroStoreDir        = ".kiro"
	kiroSessionsDir     = "sessions"
	kiroSessionFile     = "session.json"
	kiroMessagesFile    = "messages.jsonl"
	kiroWorkspaceKeyLen = 16
	// kiroUntitledTitle is the title of a session that no prompt named yet.
	kiroUntitledTitle = "New Session"
)

// kiroSessionRecord is the part of session.json that the picker reads.
type kiroSessionRecord struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	WorkspacePaths []string `json:"workspacePaths"`
	LastModifiedAt string   `json:"lastModifiedAt"`
	Meta           struct {
		Kiro struct {
			Workflow json.RawMessage `json:"workflow"`
		} `json:"kiro"`
	} `json:"_meta"`
}

// kiroNormalizePath reproduces the path normalization of Kiro's engine: an
// absolute path with forward slashes, cleaned, with no trailing separator
// except at a root, and in lower case on Windows.
//
// The engine uses Node's path.posix.normalize, which keeps a trailing slash
// that path.Clean drops. The strip step then removes that slash again, except
// after a bare drive, so only a drive root such as `C:/` needs the slash back.
func kiroNormalizePath(dir string) string {
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	// The engine replaces a backslash on every platform, not on Windows alone.
	slashed := strings.ReplaceAll(dir, `\`, "/")
	unc := runtime.GOOS == "windows" && strings.HasPrefix(slashed, "//")
	normalized := path.Clean(slashed)
	if unc && !strings.HasPrefix(normalized, "//") {
		normalized = "/" + normalized
	}
	if isWindowsDrive(normalized) && strings.HasSuffix(slashed, "/") {
		normalized += "/"
	}
	if runtime.GOOS == "windows" {
		return strings.ToLower(normalized)
	}
	return normalized
}

// isWindowsDrive reports whether a path is a bare drive such as `C:`.
func isWindowsDrive(p string) bool {
	return len(p) == 2 && p[1] == ':' && ((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// kiroWorkspaceKey is the name of the group that holds the sessions of one
// workspace.
func kiroWorkspaceKey(workingDir string) string {
	sum := sha256.Sum256([]byte(kiroNormalizePath(workingDir)))
	return hex.EncodeToString(sum[:])[:kiroWorkspaceKeyLen]
}

// kiroStoredSessions is Kiro's Provider.ListStoredSessions.
func kiroStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	home := q.Home()
	if home == "" {
		return nil, nil
	}
	group := filepath.Join(home, kiroStoreDir, kiroSessionsDir, kiroWorkspaceKey(workingDir))
	limit := q.EffectiveLimit()
	// Every candidate, newest first: a workflow step or an empty session
	// takes a place in the walk and none in the answer, so a cap here would
	// drop real sessions behind them.
	entries, err := sessionstore.NewestEntries(group, 0, sessionstore.NamedFileInside(kiroSessionFile))
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readKiroSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// readKiroSession derives one session from its record, or rejects it.
func readKiroSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	var record kiroSessionRecord
	if err := sessionstore.ReadSidecarFile(filepath.Join(entry.Path, kiroSessionFile), sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &record)
	}); err != nil {
		return agent.StoredSession{}, false
	}
	id := strings.TrimSpace(record.ID)
	if id == "" || id != entry.Name {
		return agent.StoredSession{}, false
	}
	if workflow := strings.TrimSpace(string(record.Meta.Kiro.Workflow)); workflow != "" && workflow != "null" {
		return agent.StoredSession{}, false
	}
	// Kiro groups a session by ALL of its workspace roots, so a session of this
	// group runs in this workspace alone.
	if len(record.WorkspacePaths) != 1 || kiroNormalizePath(record.WorkspacePaths[0]) != kiroNormalizePath(workingDir) {
		return agent.StoredSession{}, false
	}
	// A session that never took a prompt has no conversation to continue: Kiro
	// writes its message log with the first prompt.
	if info, err := os.Stat(filepath.Join(entry.Path, kiroMessagesFile)); err != nil || info.Size() == 0 {
		return agent.StoredSession{}, false
	}
	title := strings.TrimSpace(record.Title)
	if title == kiroUntitledTitle {
		title = ""
	}
	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(title),
		UpdatedAt: kiroSessionTime(record, entry),
	}, true
}

// kiroSessionTime is a session's last activity: the time its record states,
// else the record file's modification time.
func kiroSessionTime(record kiroSessionRecord, entry sessionstore.Entry) time.Time {
	if ts := sessionstore.ParseRFC3339(record.LastModifiedAt); !ts.IsZero() {
		return ts
	}
	return entry.ModTime
}
