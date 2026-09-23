package acptest

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AssertSecondaryFallback pins that acp.Start seeds a started agent's
// secondary-axis fallback from the same option groups that the provider
// registers. got is the fallback that acp.Start computes from groups, want is the
// provider's fallback list, and registered is what the registration serves.
//
// The provider passes one static-groups value both to its registration and to
// acp.Start. So no configure step restates the list, and a started agent serves
// the registered list before its session reports a catalog.
func AssertSecondaryFallback(t *testing.T, got, want []*leapmuxv1.AvailableOption, registered, groups []*leapmuxv1.AvailableOptionGroup) {
	t.Helper()
	require.Len(t, got, len(want), "fallback option count")
	for i := range want {
		assert.Equal(t, want[i].GetId(), got[i].GetId(), "option %d id", i)
		assert.Equal(t, want[i].GetName(), got[i].GetName(), "option %d name", i)
	}
	// The registration serves the very groups that acp.Start reads, not a copy.
	require.Len(t, registered, len(groups))
	for i := range groups {
		assert.Same(t, groups[i], registered[i], "group %d", i)
	}
}
