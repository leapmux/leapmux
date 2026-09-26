package junie

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
)

// Junie appends one record per session to `$JUNIE_HOME/sessions/index.jsonl`,
// and keeps each session's files under `$JUNIE_HOME/sessions/<id>/`. The
// `sessionId` of a record is the resume handle `session/resume` and
// `session/load` both take.

// junieIndexRecord is one line of `index.jsonl`.
type junieIndexRecord struct {
	SessionID string `json:"sessionId"`
	CreatedAt int64  `json:"createdAt"`
	UpdatedAt int64  `json:"updatedAt"`
	Project   string `json:"projectDir"`
	TaskName  string `json:"taskName"`
}

// junieHome resolves the root that holds `sessions/`. `JUNIE_HOME` wins, then
// the home directory's `.junie` -- the same order the CLI resolves it in.
func junieHome(q agent.StoredSessionQuery) string {
	if home := strings.TrimSpace(q.Getenv("JUNIE_HOME")); home != "" {
		return home
	}
	if home := q.Home(); home != "" {
		return filepath.Join(home, ".junie")
	}
	return ""
}

// junieStoredSessions lists the resumable sessions of one working directory,
// newest first. A malformed line is skipped rather than failing the whole
// list: the index is append-only, and a killed process can leave a partial
// line at the end.
func junieStoredSessions(_ context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	root := junieHome(q)
	if root == "" {
		return nil, nil
	}
	path := filepath.Join(root, "sessions", "index.jsonl")
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer func() { _ = file.Close() }()

	limit := q.Limit
	if limit <= 0 {
		limit = agent.DefaultStoredSessionLimit
	}
	out := make([]agent.StoredSession, 0, 16)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record junieIndexRecord
		if json.Unmarshal([]byte(line), &record) != nil {
			continue
		}
		if record.SessionID == "" {
			continue
		}
		if q.WorkingDir != "" && record.Project != "" && record.Project != q.WorkingDir {
			continue
		}
		out = append(out, agent.StoredSession{
			Handle:    record.SessionID,
			Title:     record.TaskName,
			UpdatedAt: junieTimestampMillis(record.UpdatedAt, record.CreatedAt),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// junieTimestampMillis converts Junie's millisecond epoch to a time, falling
// back to the creation stamp. A zero value sorts last.
func junieTimestampMillis(updated, created int64) time.Time {
	ms := updated
	if ms <= 0 {
		ms = created
	}
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
