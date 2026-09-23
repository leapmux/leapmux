package acp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// StatusIsFinal decides three things: whether a tool_call is persisted as an
// immediate closer, whether a late spawn may give its span back, and whether a
// provider hook produces a closing observation. An unknown status is NOT final,
// so an unrecognized state leaves the call open rather than closing it early.
func TestACPStatusIsFinal(t *testing.T) {
	t.Parallel()

	for _, s := range []string{"completed", "failed", "cancelled"} {
		assert.True(t, StatusIsFinal(s), "%q ends the call", s)
	}
	for _, s := range []string{"", "pending", "in_progress", "Completed", "completed "} {
		assert.False(t, StatusIsFinal(s), "%q does not end the call", s)
	}
}
