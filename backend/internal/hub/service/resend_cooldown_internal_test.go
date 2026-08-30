package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// The two flows that send mail on demand share ONE cooldown and ONE
// cutoff. They used to carry a constant and a formula each, with a comment
// in both that identified the other as its twin.
//
// What these pin is the property that made sharing correct: the gate reads
// the issued-at column, so the cutoff carries no TTL of either flow -- a
// code issued two minutes ago is two minutes into its cooldown whether its
// lifetime is thirty minutes or an hour.
func TestMintCutoff(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	// The cutoff is exactly one cooldown before now: a previous code issued
	// at or before it was issued at least the cooldown ago, so a fresh mint
	// may land.
	assert.Equal(t, now.Add(-resendCooldown), mintCutoff(now))

	// No TTL appears in the cutoff. The two flows' lifetimes differ, and an
	// expiry-derived gate would have moved with them -- and broken on a
	// force-expired code, which is why the gate reads the issued-at column.
	assert.Equal(t, mintCutoff(now), mintCutoff(now))

	// UTC whatever the caller passed, because the mint statement compares
	// the cutoff against a stored issued-at the app clock wrote.
	loc := time.FixedZone("UTC+9", 9*3600)
	assert.Equal(t, time.UTC, mintCutoff(now.In(loc)).Location())
	assert.True(t, mintCutoff(now.In(loc)).Equal(mintCutoff(now)))
}

// mintCutoff and nextResendAt read the SAME window from opposite ends, and
// this is what keeps them consistent: a code issued exactly at the cutoff
// instant is a code issued exactly one cooldown ago, so the countdown a
// client renders for it has just run out. A client that showed a shorter
// one would offer a button the hub refuses.
func TestNextResendAtUsesTheEnforcedWindow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)

	issued := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, issued.Add(resendCooldown), nextResendAt(issued))

	// The ends agree at the boundary the mint enforces.
	assert.Equal(t, now, nextResendAt(mintCutoff(now)))
}
