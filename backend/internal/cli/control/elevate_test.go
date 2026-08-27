package control_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/cli/control"
)

// NeedsElevation keys on the hub's MARKER, and the two things it must not key
// on are what these pin.
//
// The CODE alone is too broad: a FailedPrecondition also means "this account
// has no password", "this is your last passkey" and half a dozen other things
// a browser step-up cannot fix, and stopping a script to print a URL for one
// of those would be worse than reporting it. The MESSAGE is user-facing prose
// that somebody will reword, so matching it would break on the first edit.
func TestNeedsElevation(t *testing.T) {
	t.Parallel()

	marked := connect.NewError(connect.CodeFailedPrecondition, errors.New("verify your identity and try again"))
	marked.Meta().Set(control.ElevationRequiredHeader, "1")
	assert.True(t, control.NeedsElevation(marked))

	// The same code, no marker: a precondition a step-up cannot repair.
	unmarked := connect.NewError(connect.CodeFailedPrecondition,
		errors.New("this credential cannot verify your identity; sign in from a browser"))
	assert.False(t, control.NeedsElevation(unmarked),
		"a refusal with no remedy must not open a ceremony that ends in the same refusal")

	// Wrapped, because an error crosses a call boundary before the
	// interceptor tests it.
	assert.True(t, control.NeedsElevation(errors.Join(errors.New("context"), marked)))

	assert.False(t, control.NeedsElevation(nil))
	assert.False(t, control.NeedsElevation(errors.New("plain")))
	assert.False(t, control.NeedsElevation(
		connect.NewError(connect.CodeUnauthenticated, errors.New("token expired"))))
}
