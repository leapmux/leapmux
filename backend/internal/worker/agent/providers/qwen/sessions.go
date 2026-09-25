package qwen

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Qwen writes one transcript per session, grouped by a sanitized copy of the
// working directory:
//
//	<runtime>/projects/<sanitized cwd>/chats/<session id>.jsonl
//
// Each line is one ChatRecord. The first record states the session id and the
// cwd; a `custom_title` record states the title; a `parent_session` record marks
// a session that another session branched off.

// qwenSessionFile matches the name of one session transcript.
var qwenSessionFile = regexp.MustCompile(`^[0-9a-fA-F-]{32,36}\.jsonl$`)

// qwenHome resolves Qwen's global directory: `$QWEN_HOME`, else `~/.qwen`.
func qwenHome(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, "QWEN_HOME", ".qwen")
}

// qwenRuntimeDir resolves the directory that holds the transcripts, in Qwen's
// own order: `$QWEN_RUNTIME_DIR`, the user setting `advanced.runtimeOutputDir`,
// else the global directory.
func qwenRuntimeDir(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Env("QWEN_RUNTIME_DIR")); dir != "" {
		return pathutil.ExpandHome(dir, q.Home())
	}
	home := qwenHome(q)
	if home == "" {
		return ""
	}
	var settings struct {
		Advanced struct {
			RuntimeOutputDir string `json:"runtimeOutputDir"`
		} `json:"advanced"`
	}
	if err := sessionstore.ReadSidecarFile(filepath.Join(home, "settings.json"), sessionstore.MaxSidecarBytes, func(data []byte) error {
		return json.Unmarshal(data, &settings)
	}); err == nil {
		if dir := strings.TrimSpace(settings.Advanced.RuntimeOutputDir); dir != "" {
			if dir = pathutil.ExpandHome(dir, q.Home()); filepath.IsAbs(dir) {
				return dir
			}
		}
	}
	return home
}

// qwenSanitizeCwd reproduces Qwen's `sanitizeCwd`: every character but an ASCII
// letter or digit becomes a hyphen, after a fold to lower case on Windows. It
// works on UTF-16 code units, as JavaScript does, so a character outside the
// basic plane becomes two hyphens.
func qwenSanitizeCwd(cwd string) string {
	if runtime.GOOS == "windows" {
		cwd = strings.ToLower(cwd)
	}
	var b strings.Builder
	b.Grow(len(cwd))
	for _, r := range cwd {
		switch {
		case ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9'):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString("--")
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// qwenStoredSessions is Qwen's Provider.ListStoredSessions.
func qwenStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	root := qwenRuntimeDir(q)
	if root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, "projects", qwenSanitizeCwd(workingDir), "chats")
	// The sanitized name is lossy -- two directories can share it -- so only the
	// cwd in the transcript places a session. The walk is uncapped for the same
	// reason: a cut before the read would drop this directory's sessions in
	// favour of its twin's.
	entries, err := sessionstore.NewestEntries(dir, 0, sessionstore.EntryItself(func(entry os.DirEntry) bool {
		return !entry.IsDir() && qwenSessionFile.MatchString(entry.Name())
	}))
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.EffectiveLimit()
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readQwenSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// qwenHeadRecords is how many records Qwen's own lister reads from the head of
// a transcript for its cwd and its first prompt.
const qwenHeadRecords = 10

// qwenRecord is the part of one ChatRecord that the picker reads.
type qwenRecord struct {
	SessionID     string          `json:"sessionId"`
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	Cwd           string          `json:"cwd"`
	Message       *chatContent    `json:"message"`
	SystemPayload json.RawMessage `json:"systemPayload"`
}

// readQwenSession derives one session from its transcript, or rejects it.
func readQwenSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, _, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	if len(head) > qwenHeadRecords {
		head = head[:qwenHeadRecords]
	}
	var first qwenRecord
	if json.Unmarshal(head[0], &first) != nil || !sessionstore.SameDir(strings.TrimSpace(first.Cwd), workingDir) {
		return agent.StoredSession{}, false
	}
	prompt := ""
	for _, line := range head {
		var record qwenRecord
		if json.Unmarshal(line, &record) != nil {
			continue
		}
		// A branch of another session is not one a user resumes from this list.
		if record.Type == "system" && record.Subtype == "parent_session" {
			return agent.StoredSession{}, false
		}
		if prompt == "" && record.Type == "user" && record.Subtype == "" {
			prompt = qwenPromptText(record)
		}
	}
	id := strings.TrimSuffix(entry.Name, ".jsonl")
	return agent.StoredSession{
		Handle:    id,
		Title:     sessionstore.TrimTitle(sessionstore.FirstNonBlank(qwenCustomTitle(entry.Path), prompt)),
		UpdatedAt: entry.ModTime,
	}, true
}

// qwenPromptText is the text of a user record: the text that Qwen showed for
// it, else its first text part.
func qwenPromptText(record qwenRecord) string {
	var payload struct {
		DisplayText string `json:"displayText"`
	}
	if json.Unmarshal(record.SystemPayload, &payload) == nil && strings.TrimSpace(payload.DisplayText) != "" {
		return payload.DisplayText
	}
	if record.Message == nil {
		return ""
	}
	for _, part := range record.Message.Parts {
		if part.Text != nil && strings.TrimSpace(*part.Text) != "" {
			return *part.Text
		}
	}
	return ""
}

// qwenCustomTitle reads the last title that the tail of a transcript states.
func qwenCustomTitle(path string) string {
	tail, err := sessionstore.JSONLTail(path, sessionstore.JSONLProbeBytes)
	if err != nil {
		return ""
	}
	for i := len(tail) - 1; i >= 0; i-- {
		var record qwenRecord
		if json.Unmarshal(tail[i], &record) != nil || record.Type != "system" || record.Subtype != "custom_title" {
			continue
		}
		var payload struct {
			CustomTitle string `json:"customTitle"`
		}
		if json.Unmarshal(record.SystemPayload, &payload) == nil && strings.TrimSpace(payload.CustomTitle) != "" {
			return payload.CustomTitle
		}
	}
	return ""
}
