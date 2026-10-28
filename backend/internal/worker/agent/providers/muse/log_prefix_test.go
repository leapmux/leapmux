package muse

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNativePrefixRequiresEveryEarlierPositionAndTheExactTail(t *testing.T) {
	scenario := nativeParentInputCases(t)[0]
	log := newNativeLog(scenario.ChildSessionID)
	tail, err := decodeNativeRecord(scenario.ChildRecords[64], scenario.ChildSessionID)
	require.NoError(t, err)
	changed := log.changed
	require.NoError(t, log.add(scenario.ChildRecords[64]))
	select {
	case <-changed:
	default:
		t.Fatal("native record admission did not signal its waiters")
	}
	complete, err := log.hasNativePrefixThrough(nativePosition{ID: tail.ID, Sequence: tail.Sequence})
	require.NoError(t, err)
	assert.False(t, complete, "the highest received sequence cannot prove its missing prefix")
	assert.Zero(t, log.contiguousThrough.Sequence)
	for _, raw := range scenario.ChildRecords[:64] {
		require.NoError(t, log.add(raw))
	}
	complete, err = log.hasNativePrefixThrough(nativePosition{ID: tail.ID, Sequence: tail.Sequence})
	require.NoError(t, err)
	assert.True(t, complete)
	assert.Equal(t, nativePosition{ID: tail.ID, Sequence: tail.Sequence}, log.contiguousThrough)
	_, err = log.hasNativePrefixThrough(nativePosition{ID: "foreign-tail", Sequence: tail.Sequence})
	require.Error(t, err)
}

func TestNativePrefixContradictionInvalidatesEarlierAuthority(t *testing.T) {
	scenario := nativeParentInputCases(t)[0]
	log := newNativeLog(scenario.ChildSessionID)
	for _, raw := range scenario.ChildRecords[:65] {
		require.NoError(t, log.add(raw))
	}
	conflict, err := decodeNativeRecord(scenario.ChildRecords[9], scenario.ChildSessionID)
	require.NoError(t, err)
	changed := log.changed
	raw := rewriteNativeInputJSON(t, scenario.ChildRecords[9], json.RawMessage(`0`), "recorded_at")
	require.Error(t, log.add(raw))
	select {
	case <-changed:
	default:
		t.Fatal("a native contradiction did not signal its waiters")
	}
	assert.Equal(t, conflict.Sequence-1, log.contiguousThrough.Sequence)
	_, err = log.hasNativePrefixThrough(nativePosition{ID: conflict.ID, Sequence: conflict.Sequence})
	require.Error(t, err)
	complete, err := log.hasNativePrefixThrough(log.contiguousThrough)
	require.NoError(t, err)
	assert.True(t, complete)
}

func TestNativePrefixReturnsOnlyTheRequestedProjectionInterval(t *testing.T) {
	scenario := nativeParentInputCases(t)[0]
	log := newNativeLog(scenario.ChildSessionID)
	for _, raw := range scenario.ChildRecords {
		require.NoError(t, log.add(raw))
	}
	start, err := decodeNativeRecord(scenario.ChildRecords[49], scenario.ChildSessionID)
	require.NoError(t, err)
	end, err := decodeNativeRecord(scenario.ChildRecords[64], scenario.ChildSessionID)
	require.NoError(t, err)
	records, complete, err := log.nativeRecordsBetween(nativePosition{ID: start.ID, Sequence: start.Sequence}, nativePosition{ID: end.ID, Sequence: end.Sequence})
	require.NoError(t, err)
	require.True(t, complete)
	require.Len(t, records, 15)
	assert.Equal(t, int64(51), records[0].record.Sequence)
	assert.Equal(t, end.ID, records[len(records)-1].record.ID)
	assert.Equal(t, int64(65), records[len(records)-1].record.Sequence)
}
