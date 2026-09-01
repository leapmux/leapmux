package todoevents

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/ptrconv"
)

// --- ApplyPatch / MergeDetail ----------------------------------------

func TestApplyPatch_OverlaysProvidedFields(t *testing.T) {
	base := Item{ID: "1", Content: "Run tests", ActiveForm: "Running tests", Status: StatusPending}
	got := ApplyPatch(base, Patch{Status: ptrconv.Ptr(StatusInProgress)})
	assert.Equal(t, StatusInProgress, got.Status)
	// Untouched fields preserved.
	assert.Equal(t, "Run tests", got.Content)
	assert.Equal(t, "Running tests", got.ActiveForm)
}

func TestApplyPatch_NilFieldsPreserveBase(t *testing.T) {
	base := Item{ID: "1", Content: "x", ActiveForm: "Doing x"}
	got := ApplyPatch(base, Patch{Status: ptrconv.Ptr(StatusCompleted)})
	assert.Equal(t, "Doing x", got.ActiveForm)
	assert.Equal(t, "x", got.Content)
}

func TestMergeDetail_OverlaysNonZeroFields(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusPending}
	got := MergeDetail(base, Item{
		ID: "1", Content: "new", Description: "details", Status: StatusInProgress,
	})
	assert.Equal(t, "new", got.Content)
	assert.Equal(t, "details", got.Description)
	assert.Equal(t, StatusInProgress, got.Status)
}

// Regression: a KindDetail event whose Item carries the zero Status
// (StatusPending) must not silently downgrade the existing row's
// status. The read-only query that produces one may omit a populated
// status, which maps to StatusPending via StatusFromWire; preserving
// the existing status matches the wire's "field absent" intent.
func TestMergeDetail_PreservesStatusOnZeroValue(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusInProgress}
	got := MergeDetail(base, Item{ID: "1", Content: "new", Status: StatusPending})
	assert.Equal(t, "new", got.Content)
	assert.Equal(t, StatusInProgress, got.Status)
}

// --- StatusFromWire / StatusWire --------------------------------------

// Both spellings of the in-progress status reach the wire: the snake_case
// one that most providers send, and the camelCase one Codex sends.
func TestStatusFromWire(t *testing.T) {
	for wire, want := range map[string]Status{
		"pending":     StatusPending,
		"in_progress": StatusInProgress,
		"inProgress":  StatusInProgress,
		"completed":   StatusCompleted,
		"deleted":     StatusDeleted,
		"":            StatusPending,
		"nonsense":    StatusPending,
	} {
		assert.Equal(t, want, StatusFromWire(wire), "wire %q", wire)
	}
}

// The two directions agree, so a status that survives a round trip through the
// database column reads back as itself.
func TestStatusWire_RoundTrips(t *testing.T) {
	for _, status := range []Status{StatusPending, StatusInProgress, StatusCompleted, StatusDeleted} {
		assert.Equal(t, status, StatusFromWire(StatusWire(status)))
	}
}

func TestStatusIsFinished(t *testing.T) {
	assert.False(t, StatusPending.IsFinished())
	assert.False(t, StatusInProgress.IsFinished())
	assert.True(t, StatusCompleted.IsFinished(), "a completed row is eligible for cap-eviction")
	assert.True(t, StatusDeleted.IsFinished(), "so is a tombstone")
}
