package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScopeTracker_OpenClose(t *testing.T) {
	tracker := &ScopeTracker{}

	// Initially empty.
	assert.Equal(t, "[]", tracker.ThreadLines())
	assert.Equal(t, int32(0), tracker.DepthFor(""))
	assert.Equal(t, int32(0), tracker.DepthFor("scope-1"))

	// Open first scope (subagent).
	tracker.OpenScope("scope-1", "")
	assert.Equal(t, int32(1), tracker.DepthFor("scope-1"))

	lines := tracker.ThreadLines()
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "scope-1", parsed[0].ScopeID)
	assert.Equal(t, 0, parsed[0].Color)

	// Close the scope.
	tracker.CloseScope("scope-1")
	assert.Equal(t, "[]", tracker.ThreadLines())
	assert.Equal(t, int32(0), tracker.DepthFor("scope-1"))
}

func TestScopeTracker_NestedScopes(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-1", "")
	tracker.OpenScope("scope-2", "scope-1")

	assert.Equal(t, int32(1), tracker.DepthFor("scope-1"))
	assert.Equal(t, int32(2), tracker.DepthFor("scope-2"))

	lines := tracker.ThreadLines()
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)
	assert.Equal(t, "scope-1", parsed[0].ScopeID)
	assert.Equal(t, "scope-2", parsed[1].ScopeID)
	assert.Equal(t, 0, parsed[0].Color)
	assert.Equal(t, 1, parsed[1].Color)
}

func TestScopeTracker_ColumnReuse(t *testing.T) {
	tracker := &ScopeTracker{}

	// Open two scopes.
	tracker.OpenScope("scope-1", "")
	tracker.OpenScope("scope-2", "")

	// Close first scope — column 0 is freed.
	tracker.CloseScope("scope-1")

	// Open a new scope — should reuse column 0.
	tracker.OpenScope("scope-3", "")

	lines := tracker.ThreadLines()
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)

	// Column 0 should be scope-3 (reused), column 1 should be scope-2.
	assert.Equal(t, "scope-3", parsed[0].ScopeID)
	assert.Equal(t, "scope-2", parsed[1].ScopeID)
}

func TestScopeTracker_ParallelScopes(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-A", "")
	tracker.OpenScope("scope-B", "")
	tracker.OpenScope("scope-C", "")

	lines := tracker.ThreadLines()
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	// Each gets a different column and color.
	assert.Equal(t, 0, parsed[0].Color)
	assert.Equal(t, 1, parsed[1].Color)
	assert.Equal(t, 2, parsed[2].Color)
	assert.Equal(t, "scope-A", parsed[0].ScopeID)
	assert.Equal(t, "scope-B", parsed[1].ScopeID)
	assert.Equal(t, "scope-C", parsed[2].ScopeID)
}

func TestScopeTracker_CloseNonexistent(t *testing.T) {
	tracker := &ScopeTracker{}

	// Should not panic.
	tracker.CloseScope("nonexistent")
	assert.Equal(t, "[]", tracker.ThreadLines())
}

func TestScopeTracker_DepthForMainScope(t *testing.T) {
	tracker := &ScopeTracker{}

	// Empty string always returns 0 (main agent scope).
	tracker.OpenScope("scope-1", "")
	assert.Equal(t, int32(0), tracker.DepthFor(""))
}

func TestScopeTracker_ColorIncrements(t *testing.T) {
	tracker := &ScopeTracker{}

	// Colors increment even when scopes are closed.
	tracker.OpenScope("scope-1", "")
	tracker.CloseScope("scope-1")
	tracker.OpenScope("scope-2", "")

	lines := tracker.ThreadLines()
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)

	// scope-2 should have color 1 (not 0, which was used by scope-1).
	assert.Equal(t, 1, parsed[0].Color)
}

func TestScopeTracker_ThreadLinesNullSlots(t *testing.T) {
	tracker := &ScopeTracker{}

	// Open 3 scopes, close the middle one.
	tracker.OpenScope("scope-A", "")
	tracker.OpenScope("scope-B", "")
	tracker.OpenScope("scope-C", "")
	tracker.CloseScope("scope-B")

	lines := tracker.ThreadLines()
	var parsed []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	// Column 0 = scope-A, column 1 = null (freed), column 2 = scope-C.
	assert.NotEqual(t, "null", string(parsed[0]))
	assert.Equal(t, "null", string(parsed[1]))
	assert.NotEqual(t, "null", string(parsed[2]))
}
