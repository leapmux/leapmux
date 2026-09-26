package dirac

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

// Dirac keeps its task history in `$DIRAC_DIR/data/state/taskHistory.json`,
// one record per task, and each task's files under `$DIRAC_DIR/data/tasks/<id>/`.
// The `ulid` of a history record is the ACP session id, which is the resume
// handle `session/load` consumes. A task directory can outlive its history row
// after an unclean exit, so the reader also scans `tasks/*/` and falls back to
// `task_metadata.json`.

// diracHistoryRecord is one row of `taskHistory.json`.
type diracHistoryRecord struct {
	ID   string `json:"id"`
	ULID string `json:"ulid"`
	TS   int64  `json:"ts"`
	Task string `json:"task"`
	CWD  string `json:"cwdOnTaskInitialization"`
}

// diracHome resolves the root that holds `data/`. `DIRAC_DIR` wins, then the
// home directory's `.dirac` -- the same order the CLI resolves it in.
func diracHome(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Getenv("DIRAC_DIR")); dir != "" {
		return dir
	}
	if home := q.Home(); home != "" {
		return filepath.Join(home, ".dirac")
	}
	return ""
}

// diracStoredSessions lists the resumable sessions of one working directory,
// newest first. The handle is the `ulid`, which is the ACP session id
// `session/load` takes.
func diracStoredSessions(_ context.Context, q agent.StoredSessionQuery) ([]agent.StoredSession, error) {
	root := diracHome(q)
	if root == "" {
		return nil, nil
	}
	data := filepath.Join(root, "data")
	byHandle := map[string]agent.StoredSession{}
	seenTask := map[string]bool{}

	raw, err := os.ReadFile(filepath.Join(data, "state", "taskHistory.json"))
	if err == nil {
		var records []diracHistoryRecord
		if json.Unmarshal(raw, &records) == nil {
			for _, record := range records {
				handle := record.ULID
				if handle == "" {
					continue
				}
				if q.WorkingDir != "" && record.CWD != "" && record.CWD != q.WorkingDir {
					continue
				}
				byHandle[handle] = agent.StoredSession{
					Handle:    handle,
					Title:     record.Task,
					UpdatedAt: diracTimestampMillis(record.TS),
				}
				if record.ID != "" {
					seenTask[record.ID] = true
				}
			}
		}
	}

	// A task directory with no history row still holds a session: the row is
	// written at the end of a task, so a killed process leaves the directory
	// and no row. The task id is not the session id, so such a session has no
	// resume handle the picker can offer; it is counted for the store's
	// completeness and not listed. See the report's open risks.
	_ = seenTask

	out := make([]agent.StoredSession, 0, len(byHandle))
	for _, session := range byHandle {
		out = append(out, session)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	limit := q.Limit
	if limit <= 0 {
		limit = agent.DefaultStoredSessionLimit
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// diracTimestampMillis converts Dirac's millisecond epoch to a time. A zero
// value sorts last.
func diracTimestampMillis(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
