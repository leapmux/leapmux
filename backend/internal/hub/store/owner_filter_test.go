package store_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/hub/store"
	"github.com/leapmux/leapmux/internal/util/userid"
)

// FilterTabIndexKeys is the bind-time refusal for BULK tab-index deletes, which
// bind `WHERE (user_id, tab_id) IN ((?, ?), ...)`. A zero owner unwraps to "",
// and "" does not fail to match -- it MATCHES every blank-owner row -- so an
// unfiltered blank key deletes rows the caller never named.
//
// The skip-vs-refuse distinction is the point of this helper: unlike the
// single-row predicates (where the one key IS the whole statement, so refusing
// it refuses everything), one unusable key here must not cancel the deletes
// queued for its valid neighbours.
//
// The zero userid.UserID is reachable despite TabIndexKey.UserID being typed --
// Go cannot forbid userid.UserID{}, and that is exactly the value
// service.tabIndexKeys produces for a blank crdt-side owner string -- so the
// type moved the refusal here rather than removing the need for it.
func TestFilterTabIndexKeys(t *testing.T) {
	uid := userid.MustNew("u-real")

	t.Run("drops a zero owner", func(t *testing.T) {
		bound, dropped := store.FilterTabIndexKeys([]store.TabIndexKey{
			{UserID: userid.UserID{}, TabID: "t1"},
		})
		assert.Empty(t, bound)
		assert.Equal(t, 1, dropped)
	})

	t.Run("keeps a real owner's key alongside a zero one", func(t *testing.T) {
		// The regression this guards: dropping the filter makes sqlite/mysql
		// bind "" and over-delete every blank-owner row. Returning early on the
		// zero key instead of skipping it (the shape postgres used to have)
		// silently discards "real" too, so the batch reports success having
		// deleted nothing.
		got, _ := store.FilterTabIndexKeys([]store.TabIndexKey{
			{UserID: userid.UserID{}, TabID: "blank"},
			{UserID: uid, TabID: "real"},
		})
		if assert.Len(t, got, 1, "one unusable key must not cancel its neighbours") {
			assert.Equal(t, uid.String(), got[0].Owner())
			assert.Equal(t, "real", got[0].TabID())
		}
	})

	t.Run("keeps every valid key after a zero one", func(t *testing.T) {
		got, _ := store.FilterTabIndexKeys([]store.TabIndexKey{
			{UserID: userid.UserID{}, TabID: "blank"},
			{UserID: uid, TabID: "a"},
			{UserID: uid, TabID: "b"},
			{UserID: uid, TabID: "c"},
		})
		assert.Len(t, got, 3)
	})

	t.Run("unwraps every survivor for binding", func(t *testing.T) {
		// The survivors' whole purpose is to be bound, so Owner() must never be
		// the empty string a dialect would splice into the IN-list.
		got, _ := store.FilterTabIndexKeys([]store.TabIndexKey{
			{UserID: uid, TabID: "a"},
			{UserID: userid.UserID{}, TabID: "blank"},
			{UserID: userid.MustNew("u-other"), TabID: "b"},
		})
		require.Len(t, got, 2)
		for _, k := range got {
			assert.NotEmpty(t, k.Owner(), "a survivor must carry a bindable owner")
		}
	})

	t.Run("empty input", func(t *testing.T) {
		bound, dropped := store.FilterTabIndexKeys(nil)
		assert.Empty(t, bound)
		assert.Zero(t, dropped)

		bound, dropped = store.FilterTabIndexKeys([]store.TabIndexKey{})
		assert.Empty(t, bound)
		assert.Zero(t, dropped)
	})
}

// TestFilterTabIndexKeys_ReportsItsDrops pins the second return value.
//
// A drop is SKIPPED rather than fatal (see TestFilterTabIndexKeys), but skipped
// must not mean silent: reaching a zero owner here means an upstream tenancy
// invariant broke, which is the same condition service.errBlankTenant treats as
// an error on the single-row paths. Returning the count is what lets the bulk
// callers log it instead of swallowing it.
func TestFilterTabIndexKeys_ReportsItsDrops(t *testing.T) {
	uid := userid.MustNew("u-real")

	bound, dropped := store.FilterTabIndexKeys([]store.TabIndexKey{
		{UserID: userid.UserID{}, TabID: "blank-1"},
		{UserID: uid, TabID: "real"},
		{UserID: userid.UserID{}, TabID: "blank-2"},
	})
	assert.Equal(t, 2, dropped, "both unusable keys must be reported")
	if assert.Len(t, bound, 1, "the valid neighbour must survive") {
		assert.Equal(t, "real", bound[0].TabID())
	}

	// Control: a clean batch reports nothing, so a caller logging on
	// dropped > 0 stays quiet in the normal case.
	bound, dropped = store.FilterTabIndexKeys([]store.TabIndexKey{{UserID: uid, TabID: "real"}})
	assert.Zero(t, dropped)
	assert.Len(t, bound, 1)
}
