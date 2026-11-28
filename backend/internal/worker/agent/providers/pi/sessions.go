package pi

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
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

// piSessionHeader is the first record of the current Pi transcript.
type piSessionHeader struct {
	Type    string `json:"type"`
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
	if header.Type != "session" {
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

// piSessionTimestampLayout matches Pi's ISO-8601 UTC millisecond header timestamp.
const piSessionTimestampLayout = "2006-01-02T15:04:05.000Z"

// piSessionFileTimestampLayout matches Pi's file-safe UTC timestamp prefix.
const piSessionFileTimestampLayout = "2006-01-02T15-04-05-000Z"

// forkPiSessionFile atomically clones a Pi JSONL session file into the target
// working directory's session folder with a fresh UUID, timestamp, target cwd,
// and parentSession pointer while keeping all subsequent entry lines verbatim.
// It returns the absolute path of the newly written session file.
func forkPiSessionFile(q agent.StoredSessionQuery, forkFromHandle, srcWorkingDir, targetWorkingDir string) (string, error) {
	resolved, err := (piProvider{}).ResolveResumeHandle(forkFromHandle, q.Home())
	if err != nil {
		return "", fmt.Errorf("cannot fork Pi session %q: invalid session handle: %w", forkFromHandle, err)
	}
	targetWorkingDir = strings.TrimSpace(targetWorkingDir)
	if targetWorkingDir == "" {
		return "", errors.New("cannot fork Pi session: target working directory is empty")
	}

	srcPath, err := locatePiSessionFile(q, resolved, cmp.Or(strings.TrimSpace(srcWorkingDir), targetWorkingDir), targetWorkingDir)
	if err != nil {
		return "", err
	}

	root := piSessionsRoot(q)
	if root == "" {
		return "", errors.New("cannot resolve Pi sessions root directory")
	}
	newUUID, err := uuid.NewRandom()
	if err != nil {
		return "", fmt.Errorf("create forked Pi session ID: %w", err)
	}
	newID := newUUID.String()
	now := time.Now().UTC()

	targetDir := filepath.Join(root, manglePiPath(filepath.Clean(targetWorkingDir)))
	dstPath := filepath.Join(targetDir, now.Format(piSessionFileTimestampLayout)+"_"+newID+".jsonl")

	err = providerkit.StreamForkJSONL(srcPath, dstPath, 0o600, func(lr *providerkit.JSONLLineReader) ([][]byte, error) {
		firstLine, readErr := lr.ReadLine()
		if errors.Is(readErr, io.EOF) || (readErr == nil && len(firstLine) == 0) {
			return nil, fmt.Errorf("source Pi session %q is empty", srcPath)
		}
		if readErr != nil {
			return nil, fmt.Errorf("read Pi source session %q: %w", srcPath, readErr)
		}

		var header map[string]any
		if err := json.Unmarshal(firstLine, &header); err != nil {
			return nil, fmt.Errorf("parse Pi session header in %q: %w", srcPath, err)
		}
		if typ, _ := header["type"].(string); typ != "session" {
			return nil, fmt.Errorf("source Pi session %q does not start with a session header", srcPath)
		}

		header["id"] = newID
		header["timestamp"] = now.Format(piSessionTimestampLayout)
		header["cwd"] = filepath.Clean(targetWorkingDir)
		header["parentSession"] = srcPath

		encodedHeader, err := json.Marshal(header)
		if err != nil {
			return nil, fmt.Errorf("encode forked Pi session header: %w", err)
		}
		return [][]byte{encodedHeader}, nil
	})
	if err != nil {
		return "", err
	}
	return dstPath, nil
}

// locatePiSessionFile resolves either a session file path or a bare session ID
// to an existing Pi JSONL transcript file on disk.
func locatePiSessionFile(q agent.StoredSessionQuery, resolved, srcWorkingDir, targetWorkingDir string) (string, error) {
	if filepath.IsAbs(resolved) || strings.HasSuffix(resolved, ".jsonl") || strings.ContainsAny(resolved, `/\`) {
		info, err := os.Stat(resolved)
		if err != nil {
			return "", fmt.Errorf("stat source Pi session file %q: %w", resolved, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("source Pi session %q is not a regular file", resolved)
		}
		return resolved, nil
	}

	root := piSessionsRoot(q)
	if root == "" {
		return "", errors.New("cannot resolve Pi sessions root directory")
	}
	dirs := []string{filepath.Join(root, manglePiPath(filepath.Clean(srcWorkingDir)))}
	if targetDir := filepath.Join(root, manglePiPath(filepath.Clean(targetWorkingDir))); targetDir != dirs[0] {
		dirs = append(dirs, targetDir)
	}
	for _, dir := range dirs {
		if path, ok := findPiSessionByID(dir, resolved); ok {
			return path, nil
		}
	}
	return "", fmt.Errorf("source Pi session %q not found", resolved)
}

func findPiSessionByID(dir, sessionID string) (string, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	// Pass 1: zero-I/O filename match (newest-first by timestamp prefix).
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !isPiTranscript(entry) {
			continue
		}
		if piSessionIDFromFileName(entry.Name()) == sessionID {
			return filepath.Join(dir, entry.Name()), true
		}
	}
	// Pass 2: header scan newest-first for files whose name does not embed the ID.
	for i := len(entries) - 1; i >= 0; i-- {
		entry := entries[i]
		if !isPiTranscript(entry) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		head, _, err := sessionstore.JSONLHead(path, sessionstore.JSONLProbeBytes)
		if err != nil || len(head) == 0 {
			continue
		}
		var header piSessionHeader
		if json.Unmarshal(head[0], &header) == nil && header.Type == "session" && strings.TrimSpace(header.ID) == sessionID {
			return path, true
		}
	}
	return "", false
}
