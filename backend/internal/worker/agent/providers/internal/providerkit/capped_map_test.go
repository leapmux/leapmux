package providerkit

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCappedMapZeroAndNonPositiveCap(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		m    CappedMap[string, int]
	}{
		{name: "zero value", m: CappedMap[string, int]{}},
		{name: "zero cap", m: NewCappedMap[string, int](0)},
		{name: "negative cap", m: NewCappedMap[string, int](-5)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := tc.m
			assert.Zero(t, m.Cap())
			assert.Zero(t, m.Len())
			assert.False(t, m.Has("a"))
			v, ok := m.Get("a")
			assert.False(t, ok)
			assert.Zero(t, v)

			m.Set("a", 1)
			assert.Zero(t, m.Len())
			assert.False(t, m.Has("a"))
			assert.Empty(t, m.order)

			m.Delete("a")
			m.DeleteFunc(func(string, int) bool { return true })
			m.Reset()
			assert.Zero(t, m.Len())
		})
	}
}

func TestCappedMapCapacityOne(t *testing.T) {
	t.Parallel()

	m := NewCappedMap[string, int](1)
	assert.Equal(t, 1, m.Cap())

	m.Set("first", 0)
	v, ok := m.Get("first")
	require.True(t, ok)
	assert.Zero(t, v, "a stored zero value must remain distinguishable from absence")
	assert.Equal(t, 1, m.Len())

	m.Set("first", 42)
	v, ok = m.Get("first")
	require.True(t, ok)
	assert.Equal(t, 42, v)
	assert.Len(t, m.order, 1)

	m.Set("second", 99)
	assert.False(t, m.Has("first"))
	v, ok = m.Get("second")
	require.True(t, ok)
	assert.Equal(t, 99, v)
	assert.Equal(t, 1, m.Len())
	assert.Equal(t, []string{"second"}, m.order)
}

func TestCappedMapOverwritePreservesFIFOOrder(t *testing.T) {
	t.Parallel()

	m := NewCappedMap[string, int](3)
	m.Set("a", 1)
	m.Set("b", 2)
	m.Set("c", 3)

	// Overwriting the oldest key replaces its value without moving it to the tail
	// or adding a duplicate entry to the eviction slice.
	m.Set("a", 10)
	v, ok := m.Get("a")
	require.True(t, ok)
	assert.Equal(t, 10, v)
	assert.Equal(t, 3, m.Len())
	assert.Equal(t, []string{"a", "b", "c"}, m.order)

	// Inserting a fourth key must still evict "a" first.
	m.Set("d", 4)
	assert.False(t, m.Has("a"))
	assert.True(t, m.Has("b"))
	assert.True(t, m.Has("c"))
	assert.True(t, m.Has("d"))
	assert.Equal(t, []string{"b", "c", "d"}, m.order)
}

func TestCappedMapEvictionOrderAtCapacity(t *testing.T) {
	t.Parallel()

	const limit = 4
	m := NewCappedMap[string, int](limit)
	for i := range 10 {
		m.Set(fmt.Sprintf("k-%d", i), i)
	}

	assert.Equal(t, limit, m.Len())
	assert.Equal(t, []string{"k-6", "k-7", "k-8", "k-9"}, m.order)
	for i := range 6 {
		assert.False(t, m.Has(fmt.Sprintf("k-%d", i)))
	}
	for i := 6; i < 10; i++ {
		v, ok := m.Get(fmt.Sprintf("k-%d", i))
		require.True(t, ok)
		assert.Equal(t, i, v)
	}
}

func TestCappedMapDeleteAndReinsert(t *testing.T) {
	t.Parallel()

	m := NewCappedMap[string, string](3)
	m.Set("target", "v1")
	m.Set("b", "vb")
	m.Set("c", "vc")

	// Deleting a missing key does nothing.
	m.Delete("missing")
	assert.Equal(t, 3, m.Len())

	// Delete "target", fill to capacity, then re-insert "target" as the newest key.
	m.Delete("target")
	assert.False(t, m.Has("target"))
	assert.Equal(t, []string{"b", "c"}, m.order)
	backing := m.order[:cap(m.order)]
	assert.Empty(t, backing[2], "Delete must zero the vacated tail slot")

	m.Set("d", "vd")
	assert.Equal(t, []string{"b", "c", "d"}, m.order)

	m.Set("target", "v2")
	assert.False(t, m.Has("b"), "oldest live key must be evicted")
	assert.Equal(t, []string{"c", "d", "target"}, m.order)

	// One more insertion must evict "c", leaving the re-inserted "target" intact.
	m.Set("e", "ve")
	assert.False(t, m.Has("c"))
	v, ok := m.Get("target")
	require.True(t, ok)
	assert.Equal(t, "v2", v)
	assert.Equal(t, []string{"d", "target", "e"}, m.order)
}

func TestCappedMapDeleteFunc(t *testing.T) {
	t.Parallel()

	m := NewCappedMap[string, string](5)
	m.Set("c1", "root-1")
	m.Set("c2", "root-2")
	m.Set("c3", "root-1")
	m.Set("c4", "root-3")

	// Nil predicate is a no-op.
	m.DeleteFunc(nil)
	assert.Equal(t, 4, m.Len())

	var visited []string
	m.DeleteFunc(func(k, owner string) bool {
		visited = append(visited, k)
		return owner != "root-1"
	})
	assert.Equal(t, []string{"c1", "c2", "c3", "c4"}, visited, "DeleteFunc must visit keys in insertion order")
	assert.Equal(t, 2, m.Len())
	assert.Equal(t, []string{"c1", "c3"}, m.order)
	assert.True(t, m.Has("c1"))
	assert.True(t, m.Has("c3"))
	assert.False(t, m.Has("c2"))
	assert.False(t, m.Has("c4"))

	backing := m.order[:cap(m.order)]
	for i := len(m.order); i < len(backing); i++ {
		assert.Empty(t, backing[i], "DeleteFunc must zero vacated tail slots")
	}

	// Deleting everything leaves an empty map with its limit intact.
	m.DeleteFunc(func(string, string) bool { return true })
	assert.Zero(t, m.Len())
	assert.Empty(t, m.order)
	assert.Equal(t, 5, m.Cap())
}

func TestCappedMapResetPreservesCap(t *testing.T) {
	t.Parallel()

	type activityKey struct {
		id   string
		kind string
	}

	m := NewCappedMap[activityKey, struct{}](2)
	k1 := activityKey{id: "1", kind: "started"}
	k2 := activityKey{id: "2", kind: "completed"}
	k3 := activityKey{id: "3", kind: "started"}
	m.Set(k1, struct{}{})
	m.Set(k2, struct{}{})
	require.Equal(t, 2, m.Len())

	m.Reset()
	assert.Zero(t, m.Len())
	assert.Equal(t, 2, m.Cap())
	assert.False(t, m.Has(k1))
	assert.False(t, m.Has(k2))
	assert.Empty(t, m.order)

	m.Set(k2, struct{}{})
	m.Set(k3, struct{}{})
	m.Set(k1, struct{}{})
	assert.False(t, m.Has(k2))
	assert.True(t, m.Has(k3))
	assert.True(t, m.Has(k1))
}
