package crdt

import (
	"testing"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bootstrap is what makes "exactly one bootstrap frame goes on the wire, with
// both identity fields stamped" a property of the TYPE rather than of a
// correctly-written if/else at the single call site.
//
// The arms had already drifted while the caller owned the stamping: the delta
// arm set user_id and the initial arm did not, because the manager happened to
// stamp that one itself. Two stamping rules, one per arm, is exactly how the
// next field gets added to only one of them.
func TestSubscribeOutcome_Bootstrap_StampsBothArms(t *testing.T) {
	t.Run("delta", func(t *testing.T) {
		out := newSubscribeDeltaOutcome(&leapmuxv1.ResumeDelta{}, func() {})
		evt := out.Bootstrap("client-7", "user-9")

		delta := evt.GetDelta()
		require.NotNil(t, delta, "the delta arm must wrap a Delta event")
		assert.Equal(t, "client-7", delta.GetSubscriberClientId())
		assert.Equal(t, "user-9", delta.GetUserId())
		assert.Nil(t, evt.GetInitial())
	})

	t.Run("initial", func(t *testing.T) {
		out := newSubscribeInitialOutcome(&leapmuxv1.UserMaterialized{}, func() {})
		evt := out.Bootstrap("client-7", "user-9")

		initial := evt.GetInitial()
		require.NotNil(t, initial, "the initial arm must wrap an Initial event")
		assert.Equal(t, "client-7", initial.GetSubscriberClientId())
		// Stamped here too, not just on the delta arm: the client fails closed on
		// a foreign payload for BOTH, so one stamping rule beats one per arm.
		assert.Equal(t, "user-9", initial.GetUserId())
		assert.Nil(t, evt.GetDelta())
	})
}

// A zero-valued outcome names no arm. Panicking beats returning a nil frame the
// caller would happily write to the socket -- the same reason Delta()/Initial()
// are assertion-gated.
func TestSubscribeOutcome_Bootstrap_PanicsOnInvalidMode(t *testing.T) {
	assert.Panics(t, func() {
		_ = SubscribeOutcome{}.Bootstrap("c", "u")
	})
}
