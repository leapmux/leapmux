package antigravity

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/sessionstore"
)

func antigravityStoredSessions(ctx context.Context, query agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	if strings.TrimSpace(query.WorkingDir) == "" {
		return nil, nil
	}
	path := filepath.Join(query.HomeDir, ".gemini", "antigravity-cli", "conversation_summaries.db")
	db, err := sessionstore.OpenDB(ctx, path)
	if err != nil {
		if errors.Is(err, sessionstore.ErrAbsent) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = db.Close() }()
	workingDir := filepath.Clean(query.WorkingDir)
	resolved := workingDir
	if evaluated, err := filepath.EvalSymlinks(workingDir); err == nil {
		resolved = evaluated
	}
	rows, err := db.QueryContext(ctx, `SELECT conversation_id, title, preview, last_modified_time
		FROM conversation_summaries
		WHERE parent_conversation_id = ''
		  AND EXISTS (SELECT 1 FROM json_each(workspace_uris) WHERE value IN (?, ?))
		ORDER BY last_modified_time DESC LIMIT ?`, fileURI(workingDir), fileURI(resolved), query.EffectiveLimit())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var sessions []agent.StoredSession
	for rows.Next() {
		var id, title, preview, modified string
		if rows.Scan(&id, &title, &preview, &modified) != nil {
			continue
		}
		if title == "" {
			title = preview
		}
		sessions = append(sessions, agent.StoredSession{
			Handle: strings.TrimSpace(id), Title: sessionstore.TrimTitle(title), UpdatedAt: parseSQLiteTime(modified),
		})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return agent.SortAndCapSessions(sessions, query.EffectiveLimit()), nil
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

func parseSQLiteTime(value string) time.Time {
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
