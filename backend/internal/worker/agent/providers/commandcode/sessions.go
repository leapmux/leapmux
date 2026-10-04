package commandcode

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

type sessionRecord struct {
	Type    string `json:"type"`
	Version int    `json:"version"`
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Name    string `json:"name"`
	Message struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// storedSessions follows native headers instead of copying the CLI's Unicode slug algorithm.
func storedSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	if query.WorkingDir == "" {
		return nil, nil
	}
	home := userHome(query)
	if home == "" {
		return nil, nil
	}
	projects := filepath.Join(home, ".commandcode", "projects")
	dirs, err := os.ReadDir(projects)
	if err != nil {
		return nil, nil
	}
	var sessions []agent.StoredSession
	for _, dir := range dirs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !dir.IsDir() {
			continue
		}
		entries, err := sessionstore.NewestEntries(filepath.Join(projects, dir.Name()), 0, sessionstore.EntryItself(func(file os.DirEntry) bool {
			return !file.IsDir() && strings.HasSuffix(file.Name(), ".jsonl") && !strings.HasSuffix(file.Name(), ".checkpoints.jsonl")
		}))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if session, ok := readSession(entry, query.WorkingDir); ok {
				sessions = append(sessions, session)
			}
		}
	}
	return agent.SortAndCapSessions(sessions, query.EffectiveLimit()), nil
}

func readSession(entry sessionstore.Entry, workingDir string) (agent.StoredSession, bool) {
	head, atEOF, err := sessionstore.JSONLHead(entry.Path, sessionstore.JSONLProbeBytes)
	if err != nil || len(head) == 0 {
		return agent.StoredSession{}, false
	}
	var header sessionRecord
	if json.Unmarshal(head[0], &header) != nil || header.Type != "session" || header.Version != 3 || header.ID == "" ||
		header.ID != strings.TrimSuffix(entry.Name, ".jsonl") || filepath.Clean(header.Cwd) != filepath.Clean(workingDir) {
		return agent.StoredSession{}, false
	}
	if _, err := (commandcodeProvider{}).ResolveResumeHandle(header.ID, ""); err != nil {
		return agent.StoredSession{}, false
	}
	session := agent.StoredSession{Handle: header.ID, UpdatedAt: entry.ModTime}
	for _, raw := range head[1:] {
		var record sessionRecord
		if json.Unmarshal(raw, &record) == nil && record.Type == "message" && record.Message.Role == "user" {
			text := nativeText(record.Message.Content)
			if strings.TrimSpace(text) != "" {
				session.Title = strings.SplitN(text, "\n", 2)[0]
				break
			}
		}
	}
	records := head
	if !atEOF {
		if tail, err := sessionstore.JSONLTail(entry.Path, sessionstore.JSONLProbeBytes); err == nil {
			records = tail
		}
	}
	for _, raw := range records {
		var record sessionRecord
		if json.Unmarshal(raw, &record) == nil && record.Type == "session_info" && record.Name != "" {
			session.Title = record.Name
		}
	}
	return session, true
}
