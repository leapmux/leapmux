package fastagent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// fast-agent keeps each session in its own directory under
// `<home>/sessions/<session-id>/session.json`. The home is the `--home` flag,
// then `FAST_AGENT_HOME`, then `./.fast-agent` of the process working
// directory -- the same order the CLI resolves it in, less the flag this
// package never passes.

// fastagentSessionMeta is the subset of `session.json` that the picker needs.
type fastagentSessionMeta struct {
	SessionID string `json:"session_id"`
	CreatedAt string `json:"created_at"`
	Last      string `json:"last_activity"`
	Metadata  struct {
		Title            string `json:"title"`
		FirstUserPreview string `json:"first_user_preview"`
	} `json:"metadata"`
	Continuation struct {
		CWD string `json:"cwd"`
	} `json:"continuation"`
}

// fastagentHome resolves the home directory that holds the session store.
// `FAST_AGENT_HOME` wins, then the working directory's `.fast-agent`.
func fastagentHome(q agent.StoredSessionQuery) string {
	if home := strings.TrimSpace(q.Getenv("FAST_AGENT_HOME")); home != "" {
		return home
	}
	return filepath.Join(q.WorkingDir, ".fast-agent")
}

// fastagentStoredSessions lists the resumable sessions of one working
// directory, newest first. A session whose `session.json` is unreadable is
// skipped rather than failing the whole list: a half-written session directory
// is a normal outcome of a killed process, and the picker must still offer the
// sessions that are whole.
func fastagentStoredSessions(_ context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	sessionsDir := filepath.Join(fastagentHome(q), "sessions")
	entries, err := os.ReadDir(sessionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = agent.DefaultStoredSessionLimit
	}
	out := make([]agent.StoredSession, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(sessionsDir, entry.Name(), "session.json"))
		if err != nil {
			continue
		}
		var meta fastagentSessionMeta
		if json.Unmarshal(raw, &meta) != nil {
			continue
		}
		id := meta.SessionID
		if id == "" {
			id = entry.Name()
		}
		if q.WorkingDir != "" && meta.Continuation.CWD != "" && meta.Continuation.CWD != q.WorkingDir {
			continue
		}
		title := meta.Metadata.Title
		if title == "" {
			title = meta.Metadata.FirstUserPreview
		}
		out = append(out, agent.StoredSession{
			Handle:    id,
			Title:     title,
			UpdatedAt: fastagentTimestamp(meta.Last, meta.CreatedAt),
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// fastagentTimestamp parses the two timestamp fields, which carry no timezone
// marker in the shapes the probe observed. A value the parser cannot read
// becomes the zero time, which sorts last -- the same place a session whose
// store gave no timestamp takes.
func fastagentTimestamp(last, created string) time.Time {
	for _, value := range []string{last, created} {
		if value == "" {
			continue
		}
		if parsed, err := time.Parse(time.RFC3339Nano, value); err == nil {
			return parsed
		}
		if parsed, err := time.Parse("2006-01-02T15:04:05.999999", value); err == nil {
			return parsed
		}
	}
	return time.Time{}
}
