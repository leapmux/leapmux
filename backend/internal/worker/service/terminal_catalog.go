package service

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	db "github.com/leapmux/leapmux/internal/worker/generated/db"
	"github.com/leapmux/leapmux/util/validate"
)

// terminalRecord is the domain metadata for a terminal across both persistent
// SQLite tab terminals and in-memory quake terminals.
type terminalRecord struct {
	ID                string
	WorkingDir        string
	ShellStartDir     string
	Shell             string
	Title             string
	Cols              int64
	Rows              int64
	Screen            []byte
	StartupError      string
	WorkspaceArchived bool
	CreatedAt         time.Time
	IsQuake           bool
}

func terminalRecordFromDB(row db.Terminal) terminalRecord {
	return terminalRecord{
		ID:                row.ID,
		WorkingDir:        row.WorkingDir,
		ShellStartDir:     row.ShellStartDir,
		Shell:             row.Shell,
		Title:             row.Title,
		Cols:              row.Cols,
		Rows:              row.Rows,
		Screen:            row.Screen,
		StartupError:      row.StartupError,
		WorkspaceArchived: row.WorkspaceArchived,
		CreatedAt:         row.CreatedAt.Time,
		IsQuake:           false,
	}
}

func terminalRecordFromQuake(q QuakeTerminal) terminalRecord {
	return terminalRecord{
		ID:            q.ID,
		WorkingDir:    q.WorkingDir,
		ShellStartDir: q.ShellStartDir,
		Shell:         q.Shell,
		Title:         q.Title,
		Cols:          q.Cols,
		Rows:          q.Rows,
		Screen:        []byte{},
		StartupError:  q.StartupError,
		CreatedAt:     q.CreatedAt,
		IsQuake:       true,
	}
}

// terminalCatalog unifies terminal metadata lookups and writes across
// persistent SQLite terminals and in-memory quake terminals.
type terminalCatalog struct {
	queries *db.Queries
	quake   *QuakeTerminalStore
}

func (svc *Service) terminalCatalog() terminalCatalog {
	return terminalCatalog{queries: svc.Queries, quake: svc.QuakeTerminals}
}

func (c terminalCatalog) get(ctx context.Context, id string) (terminalRecord, error) {
	if c.quake != nil {
		if q, ok := c.quake.Get(id); ok {
			return terminalRecordFromQuake(q), nil
		}
	}
	row, err := c.queries.GetTerminal(ctx, id)
	if err != nil {
		return terminalRecord{}, err
	}
	return terminalRecordFromDB(row), nil
}

func (c terminalCatalog) lookupID(ctx context.Context, id string) (db.GetTerminalIDRow, error) {
	if c.quake != nil && c.quake.Has(id) {
		return db.GetTerminalIDRow{ID: id, WorkspaceArchived: false}, nil
	}
	return c.queries.GetTerminalID(ctx, id)
}

func (c terminalCatalog) listByIDs(ctx context.Context, ids []string) ([]terminalRecord, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	dbRows, err := c.queries.ListTerminalsByIDs(ctx, ids)
	if err != nil {
		return nil, err
	}
	records := make([]terminalRecord, 0, len(ids))
	seen := make(map[string]struct{}, len(dbRows))
	for i := range dbRows {
		seen[dbRows[i].ID] = struct{}{}
		records = append(records, terminalRecordFromDB(dbRows[i]))
	}
	if c.quake != nil {
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			if q, ok := c.quake.Get(id); ok {
				seen[id] = struct{}{}
				records = append(records, terminalRecordFromQuake(q))
			}
		}
	}
	return records, nil
}

func (c terminalCatalog) updateTitle(ctx context.Context, id, title string) error {
	if c.quake != nil && c.quake.Has(id) {
		c.quake.UpdateTitle(id, title)
		return nil
	}
	return c.queries.UpdateTerminalTitle(ctx, db.UpdateTerminalTitleParams{ID: id, Title: title})
}

func (c terminalCatalog) setStartupError(ctx context.Context, id, errMsg string) error {
	if c.quake != nil && c.quake.Has(id) {
		c.quake.SetStartupError(id, errMsg)
		return nil
	}
	return c.queries.SetTerminalStartupError(ctx, db.SetTerminalStartupErrorParams{
		StartupError: errMsg,
		ID:           id,
	})
}

func (c terminalCatalog) postSpawnState(ctx context.Context, id string, isQuake bool) (closed, archived bool, title string) {
	if isQuake {
		if c.quake == nil {
			return true, false, ""
		}
		q, ok := c.quake.Get(id)
		if !ok {
			return true, false, ""
		}
		return false, false, q.Title
	}
	postSpawn, err := c.queries.GetTerminalForReady(ctx, id)
	if err != nil {
		return false, false, ""
	}
	return postSpawn.ClosedAt.Valid, postSpawn.WorkspaceArchived, postSpawn.Title
}

// resolveBusyTitles fills missing titles for busy terminals from in-memory
// quake terminals first and falls back to one batched SQLite read for the rest.
func (c terminalCatalog) resolveBusyTitles(ctx context.Context, unresolvedIDs []string) map[string]string {
	resolved := make(map[string]string, len(unresolvedIDs))
	if len(unresolvedIDs) == 0 {
		return resolved
	}
	var dbIDs []string
	for _, id := range unresolvedIDs {
		if c.quake != nil {
			if q, ok := c.quake.Get(id); ok {
				if cleaned := validate.CleanName(q.Title); cleaned != "" {
					resolved[id] = cleaned
				} else {
					resolved[id] = "Quake Terminal"
				}
				continue
			}
		}
		dbIDs = append(dbIDs, id)
	}
	if len(dbIDs) == 0 || c.queries == nil || ctx.Err() != nil {
		return resolved
	}
	for start := 0; start < len(dbIDs); start += busyWorkTitleBatchSize {
		if ctx.Err() != nil {
			break
		}
		end := min(start+busyWorkTitleBatchSize, len(dbIDs))
		rows, err := c.queries.ListTerminalTitlesByIDs(ctx, dbIDs[start:end])
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			slog.Warn("list terminal titles for busy-work check failed", "count", end-start, "error", err)
			continue
		}
		for _, row := range rows {
			if cleaned := validate.CleanName(row.Title); cleaned != "" {
				resolved[row.ID] = cleaned
			}
		}
	}
	return resolved
}
