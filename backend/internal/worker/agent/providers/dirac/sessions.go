package dirac

import (
	"context"
	"encoding/json"
	"fmt"
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
// handle `session/load` consumes. A task directory without a history row
// has no session handle to display in the catalog.

// diracHistoryRecord is one row of `taskHistory.json`.
type diracHistoryRecord struct {
	ID   string `json:"id"`
	ULID string `json:"ulid"`
	TS   int64  `json:"ts"`
	Task string `json:"task"`
	CWD  string `json:"cwdOnTaskInitialization"`
}

// diracRuntimeLink is the decoded `acp-session-runtime-configs/<session>.json`
// record that links one ACP session to its native root task and runtime mode.
type diracRuntimeLink struct {
	TaskID string
	Mode   string
}

// decodeDiracRuntimeLink decodes one runtime-link record. The task ID must be a
// safe path component. Return an empty mode when settings do not specify a
// string. Tasklet readers need only the task ID. Checkpoint confirmation
// requires a supported mode.
func decodeDiracRuntimeLink(raw []byte) (diracRuntimeLink, error) {
	var record struct {
		TaskID   string          `json:"taskId"`
		Settings json.RawMessage `json:"settings"`
	}
	if err := json.Unmarshal(raw, &record); err != nil || !safeDiracComponent(record.TaskID) {
		return diracRuntimeLink{}, fmt.Errorf("the Dirac session has no valid native root task")
	}
	link := diracRuntimeLink{TaskID: record.TaskID}
	var settings struct {
		Mode string `json:"mode"`
	}
	if json.Unmarshal(record.Settings, &settings) == nil {
		link.Mode = settings.Mode
	}
	return link, nil
}

// diracHistoryMatch reports whether the history index holds exactly one row for
// taskID and that row is owned by sessionID. A duplicate task row or a row owned
// by another session is not a usable link.
func diracHistoryMatch(records []diracHistoryRecord, taskID, sessionID string) bool {
	matched := 0
	var row diracHistoryRecord
	for _, record := range records {
		if record.ID == taskID {
			matched++
			row = record
		}
	}
	return matched == 1 && row.ULID == sessionID
}

// diracHome resolves the root that holds `data/`. `DIRAC_DIR` wins, then the
// home directory's `.dirac` -- the same order the CLI resolves it in.
func diracHome(q agent.StoredSessionQuery) string {
	if dir := strings.TrimSpace(q.Env("DIRAC_DIR")); dir != "" {
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
			}
		}
	}

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
