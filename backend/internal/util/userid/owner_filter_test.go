package userid

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestOwnerFilter pins the single-row half of the store-bind rule: a zero id
// must unwrap to a refusal rather than to "", which matches every blank-owner
// row instead of none. The bulk half lives with the keys it filters, in
// store.FilterTabIndexKeys.
func TestOwnerFilter(t *testing.T) {
	t.Run("refuses a zero id", func(t *testing.T) {
		owner, ok := OwnerFilter(UserID{})
		assert.False(t, ok, "an unminted caller owns nothing")
		assert.Empty(t, owner, "the refused value must not be bindable")
	})

	t.Run("unwraps a real id", func(t *testing.T) {
		owner, ok := OwnerFilter(MustNew("u-real"))
		assert.True(t, ok)
		assert.Equal(t, "u-real", owner)
	})
}
