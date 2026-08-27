package service

import (
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRejectSolo pins the one refusal eleven handlers used to write out.
//
// The sentence states the actor, and that is what lets one template serve a
// plural subject and a singular one alike: the hand-written copies each
// chose their own verb ("changes are", "sign-up is"), so a passive template
// had to be wrong for some of them. One of the nine already lost its
// subject entirely and read only "not available in solo mode".
func TestRejectSolo(t *testing.T) {
	t.Parallel()

	assert.NoError(t, rejectSolo(false, "password changes"))

	err := rejectSolo(true, "password changes")
	require.Error(t, err)
	assert.Equal(t, connect.CodeFailedPrecondition, connect.CodeOf(err))
	assert.Equal(t, "failed_precondition: solo mode does not provide password changes", err.Error())

	// A plural subject and a singular one read the same way, which is the
	// property the shared template exists for.
	assert.EqualError(t, rejectSolo(true, "sign-up"),
		"failed_precondition: solo mode does not provide sign-up")

	// The two named wrappers delegate, so their subjects cannot drift from
	// the shape every other refusal takes.
	assert.NoError(t, rejectSoloElevation(false))
	assert.EqualError(t, rejectSoloElevation(true),
		"failed_precondition: solo mode does not provide session elevation")
	assert.NoError(t, rejectSoloPasskeyManagement(false))
	assert.EqualError(t, rejectSoloPasskeyManagement(true),
		"failed_precondition: solo mode does not provide passkey management")
}
