package service

import (
	"context"
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/util/userid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ApplyTabArchiveState gives archive tests one fresh authoritative observation.
// Production captures observations before Hub reads through its owning pass.
func (svc *Service) ApplyTabArchiveState(ctx context.Context, state leapmuxv1.WorkspaceArchiveState, owner userid.UserID, tabs []*leapmuxv1.TabIdent) ([]string, error) {
	observation := svc.beginArchiveObservation()
	defer observation.done()
	return svc.applyObservedTabArchiveState(ctx, state, owner, tabs, observation)
}

func TestArchiveObservation_RejectsOlderSameResourceAndPrunesVersions(t *testing.T) {
	t.Parallel()
	var tracker archiveObservationTracker
	older := tracker.begin()
	newer := tracker.begin()
	require.NoError(t, newer.accept([]string{"agent:a"}))
	newer.done()
	require.ErrorIs(t, older.accept([]string{"agent:a"}), errArchiveObservationStale)
	require.NoError(t, older.accept([]string{"agent:b"}), "an unrelated newer application does not invalidate this key")
	tracker.mu.Lock()
	assert.Len(t, tracker.versions, 2)
	tracker.mu.Unlock()
	older.done()
	tracker.mu.Lock()
	assert.Empty(t, tracker.versions)
	assert.Empty(t, tracker.active)
	tracker.mu.Unlock()
	require.ErrorIs(t, older.accept([]string{"agent:a"}), errArchiveObservationClosed)
}
