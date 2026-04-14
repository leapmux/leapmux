package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/msgcodec"
	db "github.com/leapmux/leapmux/internal/worker/generated/db"
)

func TestOutputHandlerUpdatePlan_MaterializesPlanFileAndStoresCanonicalPath(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	watchers := NewWatcherManager()
	h := NewOutputHandler(queries, watchers, nil, nil)
	h.DataDir = dataDir
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC) }

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "Agent 1",
	}))

	content := []byte("# Design: Rendering fixes\n\n- item\n")
	compressed, compression := msgcodec.Compress(content)
	h.updatePlan("agent-1", "/provider/path.md", compressed, compression, "Rendering fixes")

	row, err := queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)

	wantPath := filepath.Join(dataDir, "plans", "2026", "04", "Rendering fixes.md")
	assert.Equal(t, wantPath, row.PlanFilePath)
	assert.Equal(t, "Rendering fixes", row.PlanTitle)

	got, err := os.ReadFile(row.PlanFilePath)
	require.NoError(t, err)
	assert.Equal(t, string(content), string(got))
}

func TestOutputHandlerUpdatePlan_CollisionAppendsCounterSuffix(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	h := NewOutputHandler(queries, NewWatcherManager(), nil, nil)
	h.DataDir = dataDir
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC) }

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "Agent 1",
	}))

	dir := filepath.Join(dataDir, "plans", "2026", "04")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "Some Plan.md"), []byte("existing"), 0o644))

	compressed, compression := msgcodec.Compress([]byte("# Plan: Some Plan\n"))
	h.updatePlan("agent-1", "", compressed, compression, "Some Plan")

	row, err := queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "Some Plan (1).md"), row.PlanFilePath)
}

func TestOutputHandlerUpdatePlan_StoresUntitledFallback(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	h := NewOutputHandler(queries, NewWatcherManager(), nil, nil)
	h.DataDir = dataDir
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC) }

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "Agent 1",
	}))

	compressed, compression := msgcodec.Compress([]byte("- item\n"))
	h.updatePlan("agent-1", "", compressed, compression, "")

	row, err := queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dataDir, "plans", "2026", "04", "Untitled Plan.md"), row.PlanFilePath)
}

func TestOutputHandlerMaterializePlanFile_PicksFirstFreeCounter(t *testing.T) {
	dataDir := t.TempDir()
	h := NewOutputHandler(nil, nil, nil, nil)
	h.DataDir = dataDir
	now := time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC)
	dir := filepath.Join(dataDir, "plans", "2026", "04")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	for _, name := range []string{"Some Plan.md", "Some Plan (1).md"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644))
	}

	path, err := h.materializePlanFile("Some Plan", []byte("content"), now)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(dir, "Some Plan (2).md"), path)
}

func TestOutputHandlerUpdatePlan_PreservesCompressedContentInDB(t *testing.T) {
	_, queries := setupTestDB(t)
	ctx := context.Background()
	dataDir := t.TempDir()
	h := NewOutputHandler(queries, NewWatcherManager(), nil, nil)
	h.DataDir = dataDir
	h.now = func() time.Time { return time.Date(2026, time.April, 14, 9, 0, 0, 0, time.UTC) }

	require.NoError(t, queries.CreateAgent(ctx, db.CreateAgentParams{
		ID:          "agent-1",
		WorkspaceID: "ws-1",
		WorkingDir:  t.TempDir(),
		HomeDir:     t.TempDir(),
		Title:       "Agent 1",
	}))

	content := []byte(fmt.Sprintf("# Plan: %s\n", "Canonical Plan"))
	compressed, compression := msgcodec.Compress(content)
	h.updatePlan("agent-1", "", compressed, compression, "Canonical Plan")

	row, err := queries.GetAgentByID(ctx, "agent-1")
	require.NoError(t, err)
	decompressed, err := msgcodec.Decompress(row.PlanContent, row.PlanContentCompression)
	require.NoError(t, err)
	assert.Equal(t, string(content), string(decompressed))
}
