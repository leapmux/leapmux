package crdt_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/crdt"
)

func projectionWithTab(workspaceID, tabID, tileID, position string) *crdt.Projection {
	row := &crdt.RenderedTab{
		OrgID:       "org",
		WorkspaceID: workspaceID,
		TabType:     leapmuxv1.TabType_TAB_TYPE_AGENT,
		TabID:       tabID,
		WorkerID:    "w1",
		TileID:      tileID,
		Position:    position,
	}
	return &crdt.Projection{
		OrgID:        "org",
		Workspaces:   map[string]*crdt.WorkspaceProjection{},
		OwnedTabs:    []*crdt.RenderedTab{row},
		RenderedTabs: []*crdt.RenderedTab{row},
	}
}

func TestDiffProjection_NewTab_GeneratesUpserts(t *testing.T) {
	prev := &crdt.Projection{OrgID: "org", Workspaces: map[string]*crdt.WorkspaceProjection{}}
	next := projectionWithTab("ws1", "t1", "tile-A", "a")
	diff := crdt.DiffProjection(prev, next)
	assert.Len(t, diff.OwnedUpserts, 1)
	assert.Len(t, diff.RenderedUpserts, 1)
	assert.Empty(t, diff.OwnedDeletes)
	assert.Empty(t, diff.RenderedDeletes)
	assert.Equal(t, "tile-A", diff.OwnedUpserts[0].TileID)
}

func TestDiffProjection_RemovedTab_GeneratesDeletes(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := &crdt.Projection{OrgID: "org", Workspaces: map[string]*crdt.WorkspaceProjection{}}
	diff := crdt.DiffProjection(prev, next)
	assert.Empty(t, diff.OwnedUpserts)
	assert.Len(t, diff.OwnedDeletes, 1)
	assert.Equal(t, "t1", diff.OwnedDeletes[0].TabID)
}

func TestDiffProjection_MovedTab_GeneratesUpsertOnly(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := projectionWithTab("ws2", "t1", "tile-B", "a")
	diff := crdt.DiffProjection(prev, next)
	assert.Len(t, diff.OwnedUpserts, 1, "moving the tab should yield an upsert, not delete+insert")
	assert.Empty(t, diff.OwnedDeletes)
	assert.Equal(t, "ws2", diff.OwnedUpserts[0].WorkspaceID)
	assert.Equal(t, "tile-B", diff.OwnedUpserts[0].TileID)
}

func TestDiffProjection_NoChange_NoOps(t *testing.T) {
	prev := projectionWithTab("ws1", "t1", "tile-A", "a")
	next := projectionWithTab("ws1", "t1", "tile-A", "a")
	diff := crdt.DiffProjection(prev, next)
	assert.Empty(t, diff.OwnedUpserts)
	assert.Empty(t, diff.OwnedDeletes)
	assert.Empty(t, diff.RenderedUpserts)
	assert.Empty(t, diff.RenderedDeletes)
}

// recordingTabIndexWriter captures the bulk-call arguments ApplyDiff
// forwards. It records each call's slice as-is so the test can
// observe both batching (one call per non-empty phase) and the
// per-row contents.
type recordingTabIndexWriter struct {
	ownedUpserts    [][]crdt.TabIndexRow
	ownedDeletes    [][]crdt.TabKey
	renderedUpserts [][]crdt.TabIndexRow
	renderedDeletes [][]crdt.TabKey

	// failOn names the bulk phase the writer should fail in. Empty
	// string means "never fail".
	failOn string
}

var errBulkInjected = errors.New("recording writer: injected failure")

func (w *recordingTabIndexWriter) BulkUpsertOwned(_ context.Context, rows []crdt.TabIndexRow) error {
	if w.failOn == "owned-upsert" {
		return errBulkInjected
	}
	w.ownedUpserts = append(w.ownedUpserts, append([]crdt.TabIndexRow(nil), rows...))
	return nil
}

func (w *recordingTabIndexWriter) BulkDeleteOwned(_ context.Context, keys []crdt.TabKey) error {
	if w.failOn == "owned-delete" {
		return errBulkInjected
	}
	w.ownedDeletes = append(w.ownedDeletes, append([]crdt.TabKey(nil), keys...))
	return nil
}

func (w *recordingTabIndexWriter) BulkUpsertRendered(_ context.Context, rows []crdt.TabIndexRow) error {
	if w.failOn == "rendered-upsert" {
		return errBulkInjected
	}
	w.renderedUpserts = append(w.renderedUpserts, append([]crdt.TabIndexRow(nil), rows...))
	return nil
}

func (w *recordingTabIndexWriter) BulkDeleteRendered(_ context.Context, keys []crdt.TabKey) error {
	if w.failOn == "rendered-delete" {
		return errBulkInjected
	}
	w.renderedDeletes = append(w.renderedDeletes, append([]crdt.TabKey(nil), keys...))
	return nil
}

func TestApplyDiff_OnePhasePerNonEmptySlice(t *testing.T) {
	diff := crdt.IndexDiff{
		OwnedUpserts: []crdt.TabIndexRow{
			{OrgID: "org", WorkspaceID: "ws", TabID: "t1", TileID: "tile-1"},
			{OrgID: "org", WorkspaceID: "ws", TabID: "t2", TileID: "tile-2"},
			{OrgID: "org", WorkspaceID: "ws", TabID: "t3", TileID: "tile-3"},
		},
		OwnedDeletes: []crdt.TabKey{
			{OrgID: "org", TabID: "old-1"},
			{OrgID: "org", TabID: "old-2"},
		},
		RenderedUpserts: []crdt.TabIndexRow{
			{OrgID: "org", WorkspaceID: "ws", TabID: "t1", TileID: "tile-1"},
		},
		// RenderedDeletes intentionally empty.
	}
	w := &recordingTabIndexWriter{}
	require.NoError(t, crdt.ApplyDiff(context.Background(), w, diff))

	// Each non-empty phase must produce exactly one bulk call; the
	// empty phase must produce zero (proves ApplyDiff no longer
	// loops per row).
	require.Len(t, w.ownedUpserts, 1)
	require.Len(t, w.ownedDeletes, 1)
	require.Len(t, w.renderedUpserts, 1)
	require.Empty(t, w.renderedDeletes)

	// The slice forwarded to the writer is the diff slice verbatim.
	assert.Equal(t, diff.OwnedUpserts, w.ownedUpserts[0])
	assert.Equal(t, diff.OwnedDeletes, w.ownedDeletes[0])
	assert.Equal(t, diff.RenderedUpserts, w.renderedUpserts[0])
}

func TestApplyDiff_EmptyDiff_NoCalls(t *testing.T) {
	w := &recordingTabIndexWriter{}
	require.NoError(t, crdt.ApplyDiff(context.Background(), w, crdt.IndexDiff{}))
	assert.Empty(t, w.ownedUpserts)
	assert.Empty(t, w.ownedDeletes)
	assert.Empty(t, w.renderedUpserts)
	assert.Empty(t, w.renderedDeletes)
}

func TestApplyDiff_PropagatesPhaseError(t *testing.T) {
	diff := crdt.IndexDiff{
		OwnedUpserts: []crdt.TabIndexRow{{OrgID: "org", TabID: "t1"}},
		OwnedDeletes: []crdt.TabKey{{OrgID: "org", TabID: "old"}},
	}
	w := &recordingTabIndexWriter{failOn: "owned-delete"}
	err := crdt.ApplyDiff(context.Background(), w, diff)
	require.ErrorIs(t, err, errBulkInjected)
	// The error message must name the failed phase and the affected
	// row count so operators can correlate logs with diff size.
	assert.Contains(t, err.Error(), "bulk delete owned (1 keys)")
}
