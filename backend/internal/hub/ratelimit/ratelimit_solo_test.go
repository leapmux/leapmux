package ratelimit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHiddenInSoloIsADeliberatePerOperationAnswer pins the two answers apart.
//
// hiddenInSolo was a constant true while elevation was the only operation, and
// a blanket true reads as "rate limits do not apply to solo" rather than as the
// narrower fact it was: elevation is keyed by USER, and solo has one. An
// address-keyed limit on an endpoint solo also serves is a different case, and
// hiding its key would leave a `leapmux solo -listen 0.0.0.0:4327` operator no
// way to reach it -- not through the preferences dialog and not through
// `leapmux control admin settings`, because HiddenInSolo drops the key from
// ListSettings itself.
//
// A new operation that copies the wrong neighbour fails here.
func TestHiddenInSoloIsADeliberatePerOperationAnswer(t *testing.T) {
	t.Parallel()

	expected := map[Operation]bool{
		OpElevation:      true,
		OpEmailChange:    true, // solo refuses email changes outright (rejectSolo)
		OpOAuthAnonymous: false,
	}
	for _, op := range KnownOperations() {
		want, recorded := expected[op]
		require.Truef(t, recorded,
			"operation %q has no recorded solo answer; state whether its key belongs on a solo hub and add it here", op)
		spec, ok := defaults[op]
		require.True(t, ok)
		assert.Equalf(t, want, spec.hiddenInSolo, "operation %q has the wrong solo visibility", op)

		key, ok := LimitKey(op)
		require.True(t, ok)
		assert.Equalf(t, want, key.UI().HiddenInSolo,
			"operation %q's catalogue answer did not reach its settings key", op)
	}
}
