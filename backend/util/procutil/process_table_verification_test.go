package procutil

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type testProcessVerification struct {
	identities  map[int]ProcessIdentity
	parents     map[int]int
	parentErr   map[int]error
	parentCalls map[int]int
	afterParent func(int)
}

func (v *testProcessVerification) identity(pid int) (ProcessIdentity, bool) {
	identity, exists := v.identities[pid]
	return identity, exists
}

func (v *testProcessVerification) parent(_ context.Context, pid int) (int, error) {
	v.parentCalls[pid]++
	if v.afterParent != nil {
		v.afterParent(pid)
	}
	return v.parents[pid], v.parentErr[pid]
}

func forestFixture() (*ProcessTable, *testProcessVerification) {
	table := &ProcessTable{byParent: map[int][]int{10: {11, 13}, 11: {12}, 20: {21}}}
	verifier := &testProcessVerification{
		identities: map[int]ProcessIdentity{
			10: {PID: 10, StartTime: 1000}, 11: {PID: 11, StartTime: 1100},
			12: {PID: 12, StartTime: 1200}, 13: {PID: 13, StartTime: 1300},
			20: {PID: 20, StartTime: 2000}, 21: {PID: 21, StartTime: 2100},
		},
		parents:   map[int]int{11: 10, 12: 11, 13: 10, 21: 20},
		parentErr: make(map[int]error), parentCalls: make(map[int]int),
	}
	return table, verifier
}

func TestVerifiedForestTraversesEachChildOnceAcrossOverlappingRoots(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	roots := []ProcessIdentity{verifier.identities[10], verifier.identities[20], verifier.identities[10], verifier.identities[11]}
	owned, err := table.verifyForest(t.Context(), roots, verifier)
	require.NoError(t, err)
	assert.ElementsMatch(t, []ProcessIdentity{verifier.identities[12], verifier.identities[13], verifier.identities[21]}, owned)
	assert.Equal(t, map[int]int{12: 1, 13: 1, 21: 1}, verifier.parentCalls)
}

func TestVerifiedForestRejectsAReplacedRootWithoutAdoptingItsChildren(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	old := verifier.identities[10]
	verifier.identities[10] = ProcessIdentity{PID: 10, StartTime: old.StartTime + 1}
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{old}, verifier)
	require.NoError(t, err)
	assert.Empty(t, owned)
	assert.Empty(t, verifier.parentCalls)
}

func TestVerifiedForestRejectsChangedParentsAndRetainsOtherChildren(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	verifier.parents[11] = 999
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{verifier.identities[10]}, verifier)
	require.ErrorContains(t, err, "process 11 changed its captured parent")
	assert.Equal(t, []ProcessIdentity{verifier.identities[13]}, owned)
	assert.NotContains(t, verifier.parentCalls, 12)
}

func TestVerifiedForestRejectsAChildWhoseIdentityChangesDuringVerification(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	verifier.afterParent = func(pid int) {
		if pid == 11 {
			verifier.identities[11] = ProcessIdentity{PID: 11, StartTime: 1101}
		}
	}
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{verifier.identities[10]}, verifier)
	require.NoError(t, err)
	assert.Equal(t, []ProcessIdentity{verifier.identities[13]}, owned)
	assert.NotContains(t, verifier.parentCalls, 12)
}

func TestVerifiedForestKeepsAnEndedParentAsNoNewOwnership(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	root := verifier.identities[10]
	verifier.afterParent = func(pid int) {
		if pid == 11 {
			delete(verifier.identities, 10)
		}
	}
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{root}, verifier)
	require.NoError(t, err)
	assert.Empty(t, owned)
}

func TestVerifiedForestPreservesUnreadableParentErrorsAndValidChildren(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	verifier.parentErr[11] = os.ErrPermission
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{verifier.identities[10]}, verifier)
	require.ErrorIs(t, err, os.ErrPermission)
	assert.ErrorContains(t, err, "11")
	assert.Equal(t, []ProcessIdentity{verifier.identities[13]}, owned)
}

func TestVerifiedForestStopsAtCycles(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	table.byParent[12] = []int{10, 11}
	owned, err := table.verifyForest(t.Context(), []ProcessIdentity{verifier.identities[10]}, verifier)
	require.NoError(t, err)
	assert.ElementsMatch(t, []ProcessIdentity{verifier.identities[11], verifier.identities[12], verifier.identities[13]}, owned)
	assert.Equal(t, map[int]int{11: 1, 12: 1, 13: 1}, verifier.parentCalls)
}

func TestVerifiedForestHandlesEmptyInvalidAndCancelledRequests(t *testing.T) {
	t.Parallel()
	table, verifier := forestFixture()
	owned, err := table.verifyForest(t.Context(), nil, verifier)
	require.NoError(t, err)
	assert.Empty(t, owned)
	for _, invalid := range []ProcessIdentity{{}, {PID: -1, StartTime: 1}, {PID: 10, StartTime: 0}, {PID: 10, StartTime: -1}} {
		_, err := table.verifyForest(t.Context(), []ProcessIdentity{invalid}, verifier)
		require.Error(t, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = table.VerifiedForest(ctx, nil)
	require.ErrorIs(t, err, context.Canceled)
	var absentContext context.Context
	_, err = table.VerifiedForest(absentContext, nil)
	require.ErrorContains(t, err, "context is absent")
	_, err = (*ProcessTable)(nil).VerifiedForest(t.Context(), nil)
	require.ErrorContains(t, err, "no table")
	assert.Empty(t, verifier.parentCalls)
}
