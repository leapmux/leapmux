package authscope

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
)

// The set is a 32-bit bitmask. A scope past the width used to shift silently
// to zero, which made the scope allow nothing everywhere with no failure;
// the guard makes the vocabulary's growth past the width a loud one instead.
func TestBitPanicsOutsideTheSetWidth(t *testing.T) {
	require.Panics(t, func() { bit(leapmuxv1.Scope(32)) })
	require.Panics(t, func() { bit(leapmuxv1.Scope(-1)) })
	assert.NotZero(t, bit(leapmuxv1.Scope_SCOPE_ADMIN_WORKERS),
		"the highest real scope today must keep its bit")
}
