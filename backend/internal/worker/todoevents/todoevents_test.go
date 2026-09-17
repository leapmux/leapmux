package todoevents

import (
	"testing"

	"github.com/stretchr/testify/assert"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
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

// Regression: a KindDetail event built from a payload that carried NO status
// must not downgrade the existing row. Claude's TaskGet omits the field for a
// task whose status did not change, and StatusFromProviderWord("") is the
// StatusUnspecified that MergeDetail reads as "keep what the row has".
//
// The detail is built through the parser rather than with a literal, because
// the bug this guards lives in the seam between the two: for as long as ""
// parsed to StatusPending, no value of Item could tell "absent" from
// "reported pending", and MergeDetail had to drop both.
func TestMergeDetail_PreservesStatusWhenTheDetailReportedNone(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusInProgress}
	detail := Item{ID: "1", Content: "new", Status: StatusFromProviderWord("")}
	got := MergeDetail(base, detail)
	assert.Equal(t, "new", got.Content)
	assert.Equal(t, StatusInProgress, got.Status)
}

// The other half of that seam: a detail that DID report a status moves the row,
// including back to pending. The old zero-means-pending rule could not express
// this -- it dropped a reported `pending` and an absent one alike.
func TestMergeDetail_AppliesAStatusTheDetailReported(t *testing.T) {
	base := Item{ID: "1", Content: "old", Status: StatusInProgress}
	got := MergeDetail(base, Item{ID: "1", Status: StatusFromProviderWord("pending")})
	assert.Equal(t, StatusPending, got.Status)
}

// A row always carries a real state, so the write resolves an unreported status
// to pending. MergeDetail runs first, which is why an absent status preserves
// the row rather than resetting it.
func TestOrPendingResolvesOnlyTheUnreportedStatus(t *testing.T) {
	assert.Equal(t, StatusPending, StatusUnspecified.OrPending())
	for _, s := range []Status{StatusPending, StatusInProgress, StatusCompleted, StatusDeleted} {
		assert.Equal(t, s, s.OrPending(), "status %s", s)
	}
}

// --- StatusFromWire / StatusWire --------------------------------------

// Both spellings of the in-progress status reach the wire: the snake_case
// one that most providers send, and the camelCase one Codex sends.
// The words every provider spells its to-do states with. This parser reads
// inward only, from vocabularies this project does not own, so the mapping is
// pinned literally.
func TestStatusFromProviderWord(t *testing.T) {
	for word, want := range map[string]Status{
		"pending":     StatusPending,
		"in_progress": StatusInProgress,
		"inProgress":  StatusInProgress,
		"completed":   StatusCompleted,
		"deleted":     StatusDeleted,
	} {
		assert.Equal(t, want, StatusFromProviderWord(word), "word %q", word)
	}
}

// A word this parser does not know is still a REPORT, so it reads as pending --
// the state that claims the least about it.
func TestStatusFromProviderWordReadsAnUnknownWordAsPending(t *testing.T) {
	for _, word := range []string{"nonsense", "Completed", "in progress"} {
		assert.Equal(t, StatusPending, StatusFromProviderWord(word), "word %q", word)
	}
}

// The empty word is the one case that is NOT a report. Keeping it apart from an
// unknown word is what lets MergeDetail preserve a row.
func TestStatusFromProviderWordReadsTheEmptyWordAsUnspecified(t *testing.T) {
	assert.Equal(t, StatusUnspecified, StatusFromProviderWord(""))
}

// The domain type is defined over the proto enum, so agent_todos stores these
// ordinals verbatim and the browser reads the same ones.
func TestStatusOrdinalsMatchTheProtoEnum(t *testing.T) {
	for status, want := range map[Status]leapmuxv1.TodoStatus{
		StatusUnspecified: leapmuxv1.TodoStatus_TODO_STATUS_UNSPECIFIED,
		StatusPending:     leapmuxv1.TodoStatus_TODO_STATUS_PENDING,
		StatusInProgress:  leapmuxv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
		StatusCompleted:   leapmuxv1.TodoStatus_TODO_STATUS_COMPLETED,
		StatusDeleted:     leapmuxv1.TodoStatus_TODO_STATUS_DELETED,
	} {
		assert.Equal(t, want, leapmuxv1.TodoStatus(status), "status %s", status)
		assert.Equal(t, status, Status(want), "status %s, back again", status)
	}
}

func TestStatusIsFinished(t *testing.T) {
	assert.False(t, StatusPending.IsFinished())
	assert.False(t, StatusInProgress.IsFinished())
	assert.True(t, StatusCompleted.IsFinished(), "a completed row is eligible for cap-eviction")
	assert.True(t, StatusDeleted.IsFinished(), "so is a tombstone")
}

// A provider that CANCELS a task reports the end state StatusDeleted tombstones.
// Cursor spells it `cancelled`; the American spelling is here because the word
// travels as prose and no protocol fixes it.
func TestStatusFromProviderWordReadsACancelledTaskAsDeleted(t *testing.T) {
	assert.Equal(t, StatusDeleted, StatusFromProviderWord("cancelled"))
	assert.Equal(t, StatusDeleted, StatusFromProviderWord("canceled"))
}

// KindMerge and KindSnapshot carry their rows in DIFFERENT fields on purpose. One
// field for both would make "replace the list with these" and "upsert these, keep the
// rest" the same value, and a reader that forgot to switch on Kind would delete rows.
func TestMergeAndSnapshotCarryTheirRowsApart(t *testing.T) {
	merge := Event{Kind: KindMerge, Items: []Item{{ID: "1", Content: "one"}}}
	assert.Empty(t, merge.Snapshot)
	snapshot := Event{Kind: KindSnapshot, Snapshot: []Item{{ID: "1", Content: "one"}}}
	assert.Empty(t, snapshot.Items)
}

// --- EventKind membership --------------------------------------------

// TestEventKind_HasNoVariantWithoutAProducer pins the two ordinals that a
// re-added variant would move. Every member of EventKind must have a provider
// that builds it; a member nothing builds adds a switch case to every consumer
// and a branch no test can reach.
//
// KindDelete was that member. A tombstone now travels as a KindUpdate whose
// Patch carries StatusDeleted, so one frame that cancels a task and renames it
// keeps both halves. Re-adding a variant before KindDetail shifts these two
// ordinals, and this test fails.
func TestEventKind_HasNoVariantWithoutAProducer(t *testing.T) {
	assert.Equal(t, EventKind(3), KindDetail)
	assert.Equal(t, EventKind(4), KindMerge)
}
