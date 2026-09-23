package pi

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
	"github.com/leapmux/leapmux/util/pathutil"
)

// Pi writes one JSONL transcript per session, in a directory whose name derives
// from the working directory:
// `<agent dir>/sessions/--<mangled cwd>--/<timestamp>_<id>.jsonl`.
//
// Pi stores NO title. Its own picker shows the first user message, and so does
// this reader.

// piAgentDirEnv points at Pi's agent directory, and piSessionDirEnv straight at
// its sessions directory. Both are Pi's own variables; the second wins, exactly
// as Pi's `--session-dir` flag does.
const (
	piAgentDirEnv   = "PI_CODING_AGENT_DIR"
	piSessionDirEnv = "PI_CODING_AGENT_SESSION_DIR"
)

// piSessionsRoot resolves the directory holding Pi's per-working-directory
// session directories.
func piSessionsRoot(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Env(piSessionDirEnv)); dir != "" {
		return pathutil.ExpandHome(dir, q.Home())
	}
	agentDir := strings.TrimSpace(q.Env(piAgentDirEnv))
	if agentDir != "" {
		agentDir = pathutil.ExpandHome(agentDir, q.Home())
	} else {
		home := q.Home()
		if home == "" {
			return ""
		}
		agentDir = filepath.Join(home, ".pi", "agent")
	}
	return filepath.Join(agentDir, "sessions")
}

// manglePiPath reproduces Pi's `sessionDirectoryName`: drop one leading
// separator, replace every `/`, `\` and `:` with a hyphen, and wrap the result
// in a pair of double hyphens.
//
// Unlike Claude's rule this one has no length cap and no hash, so it is exact
// for every path.
func manglePiPath(cwd string) string {
	// Exactly one, which is what Pi's `^[/\\]` anchor removes. TrimLeft would
	// remove every leading separator, and a UNC path such as `//server/share`
	// starts with two -- Pi turns the second into a hyphen and this must too,
	// or the computed directory name is not the one Pi wrote.
	trimmed := cwd
	if len(trimmed) > 0 && (trimmed[0] == '/' || trimmed[0] == '\\') {
		trimmed = trimmed[1:]
	}
	replaced := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '-'
		}
		return r
	}, trimmed)
	return "--" + replaced + "--"
}

// piSessionHeader is the first record of a Pi transcript. Version 3 and version
// 4 differ elsewhere in the file; both carry these fields in the header, so
// this reader does not branch on the version.
type piSessionHeader struct {
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Version int    `json:"version"`
}

// piMessageRecord is a transcript message. The reader wants only the first
// user one, for its text.
type piMessageRecord struct {
	Type    string `json:"type"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// piStoredSessions is Pi's Provider.ListStoredSessions.
func piStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	if workingDir == "" {
		return nil, nil
	}
	root := piSessionsRoot(q)
	if root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, manglePiPath(filepath.Clean(workingDir)))

	limit := q.EffectiveLimit()
	// Capped at `limit`: unlike Claude, the directory name is an EXACT function
	// of the working directory, so every candidate here already belongs to it
	// and the newest few are the answer.
	entries, err := sessionstore.NewestEntries(dir, limit, sessionstore.EntryItself(isPiTranscript))
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}

	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readPiSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// isPiTranscript accepts the transcript FILES of a session directory.
//
// Directories are refused, which excludes the subagent transcripts: they live
// in a `<session stem>/tasks/` directory beside the session file, and a walk
// that took directories would offer a subagent as a resumable session.
func isPiTranscript(entry os.DirEntry) bool {
	return !entry.IsDir() && strings.HasSuffix(entry.Name(), ".jsonl")
}

// readPiSession derives one session from its transcript.
func readPiSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, _, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}

	var header piSessionHeader
	if json.Unmarshal(head[0], &header) != nil {
		return agent.StoredSession{}, false
	}
	// Version 3 marks the header with `"type":"session"`, version 4 with
	// `"kind":"header"`. A file whose first record is neither is not a session
	// transcript this reader understands.
	if header.Type != "session" && header.Kind != "header" {
		return agent.StoredSession{}, false
	}
	// The directory name already encodes the working directory, so this is a
	// cheap agreement check rather than the load-bearing filter it is for
	// Claude. A header with no cwd is accepted: the directory placed it.
	if header.Cwd != "" && !sessionstore.SameDir(header.Cwd, workingDir) {
		return agent.StoredSession{}, false
	}

	// The ID, not the path. `pi --session` takes either, but the running
	// process reports the ID through UpdateSessionID, and the handle has to
	// match that form for the caller to dedupe the two.
	handle := strings.TrimSpace(header.ID)
	if handle == "" {
		handle = piSessionIDFromFileName(entry.Name)
	}

	var title string
	for _, line := range head[1:] {
		var rec piMessageRecord
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Type != "message" || rec.Message.Role != "user" {
			continue
		}
		// The block shapes are the same as Claude's, so the same extractor
		// reads them: a plain string, or typed blocks of which only `text`
		// says anything about the session.
		if text := sessionstore.ContentBlockText(rec.Message.Content); text != "" {
			title = text
			break
		}
	}

	return agent.StoredSession{
		Handle:    handle,
		Title:     sessionstore.TrimTitle(title),
		UpdatedAt: entry.ModTime,
	}, true
}

// piSessionIDFromFileName recovers the id from `<timestamp>_<id>.jsonl`, for a
// transcript whose header this reader could not parse.
//
// It splits on the LAST underscore, because Pi's timestamp component contains
// hyphens but no underscore while an id may contain either.
func piSessionIDFromFileName(name string) string {
	stem := strings.TrimSuffix(name, ".jsonl")
	if i := strings.LastIndex(stem, "_"); i >= 0 {
		return stem[i+1:]
	}
	return stem
}
