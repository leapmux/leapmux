package windowed

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnchorOpensOneWindowPerKey(t *testing.T) {
	t.Parallel()

	var w Windows[string]
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	first := w.Anchor("a", now, time.Minute)
	require.NotNil(t, first)
	first.Count = 3

	// The same key inside its window returns the SAME entry; a second key
	// gets its own.
	same := w.Anchor("a", now.Add(30*time.Second), time.Minute)
	require.Same(t, first, same, "a live window must not re-anchor")
	assert.Equal(t, int64(3), same.Count)
	other := w.Anchor("b", now.Add(30*time.Second), time.Minute)
	require.NotSame(t, first, other)
	assert.Equal(t, 2, w.Len())
}

func TestGetPeeksWithoutAnchoring(t *testing.T) {
	t.Parallel()

	var w Windows[string]
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	// A miss anchors nothing: the peek a caller makes before deciding to
	// spend must not open a window.
	assert.Nil(t, w.Get("a", now))
	assert.Equal(t, 0, w.Len(), "Get must not anchor")

	e := w.Anchor("a", now, time.Minute)
	e.Count = 1
	require.Same(t, e, w.Get("a", now.Add(time.Second)))

	// Expired reads as absent, live or not in the map until the sweep.
	assert.Nil(t, w.Get("a", now.Add(61*time.Second)))
}

// The sweep's gate is the package's one performance contract: a map of
// live windows never pays a traversal, and an expired entry leaves at the
// first call after its window closed -- including an entry the caller
// never reads again.
func TestSweepRunsOnlyWhenAWindowExpired(t *testing.T) {
	t.Parallel()

	var w Windows[string]
	base := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)

	// Staggered anchors: "early" closes at base+1m, the rest at base+1m30s.
	early := w.Anchor("early", base, time.Minute)
	early.Count = 5
	for i := 0; i < 3; i++ {
		w.Anchor(string(rune('a'+i)), base.Add(30*time.Second), time.Minute).Count = 1
	}

	// A sweep while every window is live deletes nothing.
	w.Sweep(base.Add(45 * time.Second))
	assert.Equal(t, 4, w.Len(), "no traversal may delete live windows")

	// Past "early"'s close: the sweep drops exactly the expired entry and
	// keeps the live three.
	w.Sweep(base.Add(61 * time.Second))
	assert.Equal(t, 3, w.Len())
	assert.Nil(t, w.Get("early", base.Add(61*time.Second)))
	assert.NotNil(t, w.Get("a", base.Add(61*time.Second)))

	// Once everything expired, one sweep empties the map and re-arms on
	// "no earliest".
	w.Sweep(base.Add(3 * time.Minute))
	assert.Equal(t, 0, w.Len())
}

func TestDeleteDropsOneEntry(t *testing.T) {
	t.Parallel()

	var w Windows[string]
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	w.Anchor("a", now, time.Minute)
	w.Anchor("b", now, time.Minute)

	w.Delete("a")
	assert.Equal(t, 1, w.Len())
	assert.Nil(t, w.Get("a", now))
	assert.NotNil(t, w.Get("b", now))
}
