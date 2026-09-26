package droid

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

// Factory Droid keeps every session under its home, in a directory keyed by the
// working directory:
//
//	<factory home>/sessions/<sanitized-cwd>/<uuid>.jsonl
//
// Line 1 of the transcript is the session_start record. The reader opens it
// read-only and never writes to the store.

// droidHomeEnv is the variable that moves Factory's home tree. It defaults to
// `~/.factory`.
const droidHomeEnv = "FACTORY_HOME_OVERRIDE"

// droidHomeDirName is the directory under HOME when no override is set.
const droidHomeDirName = ".factory"

// droidSessionStartType is the `type` of the first line of a transcript.
const droidSessionStartType = "session_start"

// droidDataRoot resolves Factory's home directory.
func droidDataRoot(q agent.StoredSessionQuery) string {
	return sessionstore.HomeDirFromEnv(q, droidHomeEnv, droidHomeDirName)
}

// droidSessionStart is the part of the session_start line the reader takes.
type droidSessionStart struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Title string `json:"title"`
	Cwd   string `json:"cwd"`
}

// droidStoredSessions is Droid's Provider.ListStoredSessions.
func droidStoredSessions(ctx context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	workingDir := strings.TrimSpace(q.WorkingDir)
	root := droidDataRoot(q)
	if workingDir == "" || root == "" {
		return nil, nil
	}
	dir := filepath.Join(root, droidSessionsDirName, droidSanitizeCwd(workingDir))
	entries, err := sessionstore.NewestEntries(dir, 0, droidSessionEntry)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.EffectiveLimit()
	sessions := sessionstore.Collect(ctx, entries, limit, func(entry sessionstore.Entry) (agent.StoredSession, bool) {
		return readDroidSession(entry, workingDir)
	})
	return agent.SortAndCapSessions(sessions, limit), nil
}

// droidSessionEntry keeps a transcript file, timed by that file.
func droidSessionEntry(dir string, entry os.DirEntry) (sessionstore.Entry, bool) {
	if !strings.HasSuffix(entry.Name(), droidSessionSuffix) {
		return sessionstore.Entry{}, false
	}
	return sessionstore.EntryItself(nil)(dir, entry)
}

// readDroidSession reads one transcript's session_start line.
func readDroidSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	start, ok := readDroidSessionStart(entry.Path)
	if !ok {
		return agent.StoredSession{}, false
	}
	id := strings.TrimSpace(start.ID)
	if id == "" {
		id = strings.TrimSuffix(filepath.Base(entry.Path), droidSessionSuffix)
	}
	if !sessionstore.SameDir(start.Cwd, workingDir) {
		return agent.StoredSession{}, false
	}
	title := sessionstore.TrimTitle(sessionstore.FirstNonBlank(start.Title))
	return agent.StoredSession{
		Handle:    id,
		Title:     title,
		UpdatedAt: entry.ModTime,
	}, true
}

// readDroidSessionStart reads the first line of a transcript.
func readDroidSessionStart(path string) (droidSessionStart, bool) {
	f, err := os.Open(path)
	if err != nil {
		return droidSessionStart{}, false
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	if !scanner.Scan() {
		return droidSessionStart{}, false
	}
	var start droidSessionStart
	if err := json.Unmarshal(scanner.Bytes(), &start); err != nil {
		return droidSessionStart{}, false
	}
	if start.Type != droidSessionStartType {
		return droidSessionStart{}, false
	}
	return start, true
}
