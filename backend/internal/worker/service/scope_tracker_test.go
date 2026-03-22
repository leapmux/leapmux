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
	depth, lines := tracker.Snapshot("")
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)

	depth, _ = tracker.Snapshot("scope-1")
	assert.Equal(t, int32(0), depth)

	// Open first scope (subagent).
	tracker.OpenScope("scope-1", "")
	depth, lines = tracker.Snapshot("scope-1")
	assert.Equal(t, int32(1), depth)

	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, "scope-1", parsed[0].ScopeID)
	assert.Equal(t, 0, parsed[0].Color)

	// Close the scope.
	tracker.CloseScope("scope-1")
	depth, lines = tracker.Snapshot("scope-1")
	assert.Equal(t, int32(0), depth)
	assert.Equal(t, "[]", lines)
}

func TestScopeTracker_NestedScopes(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-1", "")
	tracker.OpenScope("scope-2", "scope-1")

	depth1, _ := tracker.Snapshot("scope-1")
	depth2, lines := tracker.Snapshot("scope-2")
	assert.Equal(t, int32(1), depth1)
	assert.Equal(t, int32(2), depth2)

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

	tracker.OpenScope("scope-1", "")
	tracker.OpenScope("scope-2", "")
	tracker.CloseScope("scope-1")
	tracker.OpenScope("scope-3", "")

	_, lines := tracker.Snapshot("")
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 2)

	assert.Equal(t, "scope-3", parsed[0].ScopeID)
	assert.Equal(t, "scope-2", parsed[1].ScopeID)
}

func TestScopeTracker_ParallelScopes(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-A", "")
	tracker.OpenScope("scope-B", "")
	tracker.OpenScope("scope-C", "")

	_, lines := tracker.Snapshot("")
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.Equal(t, 0, parsed[0].Color)
	assert.Equal(t, 1, parsed[1].Color)
	assert.Equal(t, 2, parsed[2].Color)
}

func TestScopeTracker_CloseNonexistent(t *testing.T) {
	tracker := &ScopeTracker{}
	tracker.CloseScope("nonexistent")
	_, lines := tracker.Snapshot("")
	assert.Equal(t, "[]", lines)
}

func TestScopeTracker_DepthForMainScope(t *testing.T) {
	tracker := &ScopeTracker{}
	tracker.OpenScope("scope-1", "")
	depth, _ := tracker.Snapshot("")
	assert.Equal(t, int32(0), depth)
}

func TestScopeTracker_ColorIncrements(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-1", "")
	tracker.CloseScope("scope-1")
	tracker.OpenScope("scope-2", "")

	_, lines := tracker.Snapshot("scope-2")
	var parsed []*ThreadLine
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 1)
	assert.Equal(t, 1, parsed[0].Color)
}

func TestScopeTracker_ThreadLinesNullSlots(t *testing.T) {
	tracker := &ScopeTracker{}

	tracker.OpenScope("scope-A", "")
	tracker.OpenScope("scope-B", "")
	tracker.OpenScope("scope-C", "")
	tracker.CloseScope("scope-B")

	_, lines := tracker.Snapshot("")
	var parsed []json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(lines), &parsed))
	require.Len(t, parsed, 3)

	assert.NotEqual(t, "null", string(parsed[0]))
	assert.Equal(t, "null", string(parsed[1]))
	assert.NotEqual(t, "null", string(parsed[2]))
}
