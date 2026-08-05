package crdt

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bootstrap is what makes "exactly one bootstrap frame goes on the wire, with
// both identity fields stamped" a property of the TYPE rather than of a
// correctly-written if/else at the single call site.
//
// The arms had already drifted while the caller owned the stamping: the delta
// arm set user_id and the initial arm did not, because the manager happened to
// stamp that one itself. Two stamping rules, one per arm, is exactly how the
// next field gets added to only one of them.
func TestSubscribeOutcome_Bootstrap_StampsBothArms(t *testing.T) {
	t.Run("delta", func(t *testing.T) {
		out := newSubscribeDeltaOutcome(&leapmuxv1.ResumeDelta{}, func() {})
		evt := out.Bootstrap("client-7", "user-9")

		delta := evt.GetDelta()
		require.NotNil(t, delta, "the delta arm must wrap a Delta event")
		assert.Equal(t, "client-7", delta.GetSubscriberClientId())
		assert.Equal(t, "user-9", delta.GetUserId())
		assert.Nil(t, evt.GetInitial())
	})

	t.Run("initial", func(t *testing.T) {
		out := newSubscribeInitialOutcome(&leapmuxv1.UserMaterialized{}, SubscribeReasonNoCursor, func() {})
		evt := out.Bootstrap("client-7", "user-9")

		initial := evt.GetInitial()
		require.NotNil(t, initial, "the initial arm must wrap an Initial event")
		assert.Equal(t, "client-7", initial.GetSubscriberClientId())
		// Stamped here too, not just on the delta arm: the client fails closed on
		// a foreign payload for BOTH, so one stamping rule beats one per arm.
		assert.Equal(t, "user-9", initial.GetUserId())
		assert.Nil(t, evt.GetDelta())
	})
}

// A zero-valued outcome names no arm. Panicking beats returning a nil frame the
// caller would happily write to the socket -- the same reason Delta()/Initial()
// are assertion-gated.
func TestSubscribeOutcome_Bootstrap_PanicsOnInvalidMode(t *testing.T) {
	assert.Panics(t, func() {
		_ = SubscribeOutcome{}.Bootstrap("c", "u")
	})
}

// Both constructors must set a reason. It is a metric label, so a construction
// path that forgot one would silently publish a series named "invalid" rather
// than fail -- which is exactly the class of bug subscribeReasonInvalid exists
// to make visible, and this is what keeps it visible at authoring time.
func TestSubscribeOutcome_ConstructorsAlwaysSetAReason(t *testing.T) {
	assert.Equal(t, SubscribeReasonResumed,
		newSubscribeDeltaOutcome(&leapmuxv1.ResumeDelta{}, func() {}).Reason(),
		"a delta outcome is resumed by definition")
	assert.Equal(t, SubscribeReasonStaleEpoch,
		newSubscribeInitialOutcome(&leapmuxv1.UserMaterialized{}, SubscribeReasonStaleEpoch, func() {}).Reason())
	assert.Equal(t, subscribeReasonInvalid, SubscribeOutcome{}.Reason(),
		"the zero outcome carries no arm, so it carries no reason either")
}

// The Label() values are metric LABEL values, so they are part of the
// interface a dashboard is written against. Pinning them here makes a rename a
// deliberate act rather than a silent break of every existing query.
//
// They live on Label() rather than String() precisely so a cosmetic edit to how
// a reason PRINTS cannot reach them -- String() is what %v and slog invoke
// implicitly, and while the vocabulary rode on it, "make the logs nicer" and
// "rename every dashboard label" were the same edit.
func TestSubscribeReason_LabelIsAStableMetricVocabulary(t *testing.T) {
	assert.Equal(t, map[SubscribeReason]string{
		subscribeReasonInvalid:               "invalid",
		SubscribeReasonResumed:               "resumed",
		SubscribeReasonNoCursor:              "no_cursor",
		SubscribeReasonBelowRetentionFloor:   "below_retention_floor",
		SubscribeReasonStaleEpoch:            "stale_epoch",
		SubscribeReasonTailOverBudget:        "tail_over_budget",
		SubscribeReasonDeltaOverFrameCeiling: "delta_over_frame_ceiling",
		SubscribeReasonCorruptRow:            "corrupt_row",
		SubscribeReasonPostScanDrift:         "post_scan_drift",
		SubscribeReasonParkOverflow:          "park_overflow",
	}, allSubscribeReasonLabels())
	assert.Equal(t, "reason_99", SubscribeReason(99).Label(),
		"an unnamed value must still produce a label, not a blank one")
	assert.Equal(t, "SubscribeReason(99)", SubscribeReason(99).String(),
		"...and must still be printable Go-side")
	// The two are DIFFERENT vocabularies, which is the whole point of the split:
	// a test that passed with String() aliased to Label() would pin nothing.
	assert.Equal(t, "SubscribeReasonNoCursor", SubscribeReasonNoCursor.String(),
		"String() names the Go constant, Label() names the metric value")
}

// allSubscribeReasonLabels walks every declared reason so a NEW member with no
// Label() arm shows up as an unnamed value in the map above rather than going
// unnoticed.
//
// The walk deliberately runs PAST the last declared member instead of stopping
// at it. Bounding the loop by the current last member (`r <=
// SubscribeReasonParkOverflow`) made it blind to exactly the change it is meant
// to catch: a reason APPENDED to the iota block -- the normal way to extend it
// -- was never visited, so a missing String() arm left the map unchanged and
// the test green while the new reason shipped to Prometheus as
// "reason_10", a label no dashboard matches. Only a mid-block insertion was
// caught, and the literal above would have caught that anyway.
//
// The bound is now the subscribeReasonMax sentinel, which sits last in the iota
// block, so an appended member is walked automatically and shows up here as its
// unnamed "reason_N" -- failing the comparison above until it gets both a
// Label() arm and an entry in the literal. golangci-lint's exhaustive check
// cannot cover this: SubscribeReason.Label() has a default arm, and the repo
// sets default-signifies-exhaustive.
func allSubscribeReasonLabels() map[SubscribeReason]string {
	out := map[SubscribeReason]string{}
	for r := subscribeReasonInvalid; r < subscribeReasonMax; r++ {
		out[r] = r.Label()
	}
	return out
}
