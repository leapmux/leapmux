package service

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/sqltime"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestTerminalCatalog_UnifiesSQLiteAndQuakeTerminals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc, _, _ := setupTestService(t)

	dir := t.TempDir()
	require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
		ID: "term-db", WorkingDir: dir, Title: "DB Shell", Cols: 80, Rows: 24, Screen: []byte("hi"),
	}))
	_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID: "term-quake", WorkingDir: dir, Title: "Quake Shell", Cols: 100, Rows: 30,
	})
	require.True(t, created)

	cat := svc.terminalCatalog()

	// Point lookup returns typed domain records with IsQuake set accurately.
	dbRec, err := cat.get(ctx, "term-db")
	require.NoError(t, err)
	assert.False(t, dbRec.IsQuake)
	assert.Equal(t, "DB Shell", dbRec.Title)

	quakeRec, err := cat.get(ctx, "term-quake")
	require.NoError(t, err)
	assert.True(t, quakeRec.IsQuake)
	assert.Equal(t, "Quake Shell", quakeRec.Title)

	_, err = cat.get(ctx, "missing")
	assert.ErrorIs(t, err, sql.ErrNoRows)

	// Batched lookup merges SQLite and Quake records without duplicates.
	listed, err := cat.listByIDs(ctx, []string{"term-db", "term-quake", "missing"})
	require.NoError(t, err)
	require.Len(t, listed, 2)

	// Title and startup-error mutations route transparently to the owning store.
	require.NoError(t, cat.updateTitle(ctx, "term-db", "Renamed DB"))
	require.NoError(t, cat.updateTitle(ctx, "term-quake", "Renamed Quake"))
	require.NoError(t, cat.setStartupError(ctx, "term-db", "db err"))
	require.NoError(t, cat.setStartupError(ctx, "term-quake", "quake err"))

	dbRec, err = cat.get(ctx, "term-db")
	require.NoError(t, err)
	assert.Equal(t, "Renamed DB", dbRec.Title)
	assert.Equal(t, "db err", dbRec.StartupError)

	quakeRec, err = cat.get(ctx, "term-quake")
	require.NoError(t, err)
	assert.Equal(t, "Renamed Quake", quakeRec.Title)
	assert.Equal(t, "quake err", quakeRec.StartupError)

	// Busy title resolution resolves quake terminals in memory and SQLite terminals via query.
	_, createdUntitled := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
		ID: "term-quake-untitled", WorkingDir: t.TempDir(),
	})
	require.True(t, createdUntitled)
	titles := cat.resolveBusyTitles(ctx, []string{"term-db", "term-quake", "term-quake-untitled"})
	assert.Equal(t, "Renamed DB", titles["term-db"])
	assert.Equal(t, "Renamed Quake", titles["term-quake"])
	assert.Equal(t, "Quake Terminal", titles["term-quake-untitled"])
}

type failingTerminalCatalogDBTX struct {
	db.DBTX
}

func (f failingTerminalCatalogDBTX) QueryContext(ctx context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return f.DBTX.QueryContext(ctx, "SELECT missing_terminal_catalog_column")
}

func TestTerminalCatalog_LookupPostSpawnListAndBusyTitles(t *testing.T) {
	t.Parallel()

	t.Run("lookupID", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()

		_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "term-quake", WorkingDir: dir, Title: "Quake",
		})
		require.True(t, created)
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: "term-archived", WorkingDir: dir, Title: "Archived", Cols: 80, Rows: 24, Screen: []byte{},
		}))
		_, err := svc.DB.ExecContext(ctx, "UPDATE terminals SET workspace_archived = 1 WHERE id = ?", "term-archived")
		require.NoError(t, err)

		cat := svc.terminalCatalog()

		qRow, err := cat.lookupID(ctx, "term-quake")
		require.NoError(t, err)
		assert.Equal(t, "term-quake", qRow.ID)
		assert.False(t, qRow.WorkspaceArchived)

		aRow, err := cat.lookupID(ctx, "term-archived")
		require.NoError(t, err)
		assert.Equal(t, "term-archived", aRow.ID)
		assert.True(t, aRow.WorkspaceArchived)

		_, err = cat.lookupID(ctx, "missing")
		assert.ErrorIs(t, err, sql.ErrNoRows)

		nilQuakeCat := terminalCatalog{queries: svc.Queries, quake: nil}
		aRow, err = nilQuakeCat.lookupID(ctx, "term-archived")
		require.NoError(t, err)
		assert.True(t, aRow.WorkspaceArchived)
		_, err = nilQuakeCat.lookupID(ctx, "term-quake")
		assert.ErrorIs(t, err, sql.ErrNoRows)
	})

	t.Run("postSpawnState", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()

		_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "quake-live", WorkingDir: dir, Title: "Live Quake",
		})
		require.True(t, created)
		_, created = svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "quake-deleted", WorkingDir: t.TempDir(), Title: "Gone Quake",
		})
		require.True(t, created)
		svc.QuakeTerminals.Delete("quake-deleted")

		for _, item := range []struct {
			id    string
			title string
		}{
			{"db-open", "Open DB"},
			{"db-closed", "Closed DB"},
			{"db-archived", "Archived DB"},
		} {
			require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
				ID: item.id, WorkingDir: dir, Title: item.title, Cols: 80, Rows: 24, Screen: []byte{},
			}))
		}
		_, err := svc.DB.ExecContext(ctx, "UPDATE terminals SET closed_at = ? WHERE id = ?", sqltime.NewSQLiteTime(nowMillis()), "db-closed")
		require.NoError(t, err)
		_, err = svc.DB.ExecContext(ctx, "UPDATE terminals SET workspace_archived = 1 WHERE id = ?", "db-archived")
		require.NoError(t, err)

		cat := svc.terminalCatalog()

		closed, archived, title := cat.postSpawnState(ctx, "quake-live", true)
		assert.False(t, closed)
		assert.False(t, archived)
		assert.Equal(t, "Live Quake", title)

		closed, archived, title = cat.postSpawnState(ctx, "quake-deleted", true)
		assert.True(t, closed)
		assert.False(t, archived)
		assert.Empty(t, title)

		nilQuakeCat := terminalCatalog{queries: svc.Queries, quake: nil}
		closed, archived, title = nilQuakeCat.postSpawnState(ctx, "quake-live", true)
		assert.True(t, closed)
		assert.False(t, archived)
		assert.Empty(t, title)

		closed, archived, title = cat.postSpawnState(ctx, "db-open", false)
		assert.False(t, closed)
		assert.False(t, archived)
		assert.Equal(t, "Open DB", title)

		closed, archived, title = cat.postSpawnState(ctx, "db-closed", false)
		assert.True(t, closed)
		assert.False(t, archived)
		assert.Equal(t, "Closed DB", title)

		closed, archived, title = cat.postSpawnState(ctx, "db-archived", false)
		assert.False(t, closed)
		assert.True(t, archived)
		assert.Equal(t, "Archived DB", title)

		closed, archived, title = cat.postSpawnState(ctx, "db-missing", false)
		assert.False(t, closed)
		assert.False(t, archived)
		assert.Empty(t, title)
	})

	t.Run("listByIDs", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()

		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: "term-db", WorkingDir: dir, Title: "DB Shell", Cols: 80, Rows: 24, Screen: []byte{},
		}))
		_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "term-quake", WorkingDir: dir, Title: "Quake Shell",
		})
		require.True(t, created)

		cat := svc.terminalCatalog()

		listed, err := cat.listByIDs(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, listed)

		listed, err = cat.listByIDs(ctx, []string{})
		require.NoError(t, err)
		assert.Nil(t, listed)

		listed, err = cat.listByIDs(ctx, []string{"term-quake", "term-quake", "term-db", "term-db"})
		require.NoError(t, err)
		require.Len(t, listed, 2)
		byID := make(map[string]terminalRecord, len(listed))
		for _, rec := range listed {
			byID[rec.ID] = rec
		}
		assert.True(t, byID["term-quake"].IsQuake)
		assert.False(t, byID["term-db"].IsQuake)

		badCat := terminalCatalog{queries: db.New(failingTerminalCatalogDBTX{DBTX: svc.DB}), quake: svc.QuakeTerminals}
		_, err = badCat.listByIDs(ctx, []string{"term-db"})
		require.Error(t, err)
	})

	t.Run("resolveBusyTitles", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		svc, _, _ := setupTestService(t)
		dir := t.TempDir()

		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: "db-blank", WorkingDir: dir, Title: "   ", Cols: 80, Rows: 24, Screen: []byte{},
		}))
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: "db-ctrl", WorkingDir: dir, Title: "\x00\x1b", Cols: 80, Rows: 24, Screen: []byte{},
		}))
		require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
			ID: "db-valid", WorkingDir: dir, Title: "Build Shell", Cols: 80, Rows: 24, Screen: []byte{},
		}))

		_, created := svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "quake-blank", WorkingDir: t.TempDir(), Title: "   ",
		})
		require.True(t, created)
		_, created = svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "quake-ctrl", WorkingDir: t.TempDir(), Title: "\x00\x1b",
		})
		require.True(t, created)
		_, created = svc.QuakeTerminals.RegisterOrAdopt(QuakeTerminal{
			ID: "quake-named", WorkingDir: t.TempDir(), Title: "Quake Logs",
		})
		require.True(t, created)

		cat := svc.terminalCatalog()

		// Empty input returns an empty map.
		assert.Empty(t, cat.resolveBusyTitles(ctx, nil))

		// Blank/control-char SQLite titles are omitted; quake titles fall back to "Quake Terminal".
		titles := cat.resolveBusyTitles(ctx, []string{"db-blank", "db-ctrl", "db-valid", "quake-blank", "quake-ctrl", "quake-named"})
		assert.NotContains(t, titles, "db-blank")
		assert.NotContains(t, titles, "db-ctrl")
		assert.Equal(t, "Build Shell", titles["db-valid"])
		assert.Equal(t, "Quake Terminal", titles["quake-blank"])
		assert.Equal(t, "Quake Terminal", titles["quake-ctrl"])
		assert.Equal(t, "Quake Logs", titles["quake-named"])

		// Cancelled ctx still resolves quake titles while skipping SQLite.
		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()
		cancelledTitles := cat.resolveBusyTitles(cancelledCtx, []string{"quake-named", "db-valid"})
		assert.Equal(t, "Quake Logs", cancelledTitles["quake-named"])
		assert.NotContains(t, cancelledTitles, "db-valid")

		// nil queries still resolves quake titles while skipping SQLite.
		noDBCat := terminalCatalog{queries: nil, quake: svc.QuakeTerminals}
		noDBTitles := noDBCat.resolveBusyTitles(ctx, []string{"quake-named", "db-valid"})
		assert.Equal(t, "Quake Logs", noDBTitles["quake-named"])
		assert.NotContains(t, noDBTitles, "db-valid")

		// DB query error still returns in-memory quake titles.
		badCat := terminalCatalog{queries: db.New(failingTerminalCatalogDBTX{DBTX: svc.DB}), quake: svc.QuakeTerminals}
		errTitles := badCat.resolveBusyTitles(ctx, []string{"quake-named", "db-valid"})
		assert.Equal(t, "Quake Logs", errTitles["quake-named"])
		assert.NotContains(t, errTitles, "db-valid")

		// Exceeding busyWorkTitleBatchSize resolves titles across multiple batches.
		const totalBatched = busyWorkTitleBatchSize + 5
		batchedIDs := make([]string, 0, totalBatched)
		for i := range totalBatched {
			id := fmt.Sprintf("batch-term-%04d", i)
			require.NoError(t, svc.Queries.UpsertTerminal(ctx, db.UpsertTerminalParams{
				ID: id, WorkingDir: dir, Title: fmt.Sprintf("Shell %d", i), Cols: 80, Rows: 24, Screen: []byte{},
			}))
			batchedIDs = append(batchedIDs, id)
		}
		batchedTitles := cat.resolveBusyTitles(ctx, batchedIDs)
		require.Len(t, batchedTitles, totalBatched)
		assert.Equal(t, "Shell 0", batchedTitles["batch-term-0000"])
		assert.Equal(t, fmt.Sprintf("Shell %d", totalBatched-1), batchedTitles[fmt.Sprintf("batch-term-%04d", totalBatched-1)])
	})
}
