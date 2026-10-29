package control_test

import (
	"errors"
	"testing"

	"connectrpc.com/connect/v2"
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
//
// Connect v2 carries no metadata on an error, so the marker rides the
// response header the caller passes in.
func TestNeedsElevation(t *testing.T) {
	t.Parallel()

	markedHeader := new(connect.Header)
	markedHeader.Set(control.ElevationRequiredHeader, "1")

	marked := connect.NewError(connect.CodeFailedPrecondition, "verify your identity and try again")
	assert.True(t, control.NeedsElevation(marked, markedHeader))

	// The same code, no marker: a precondition a step-up cannot repair.
	unmarked := connect.NewError(connect.CodeFailedPrecondition,
		"this credential cannot verify your identity; sign in from a browser")
	assert.False(t, control.NeedsElevation(unmarked, new(connect.Header)),
		"a refusal with no remedy must not open a ceremony that ends in the same refusal")

	// Wrapped, because an error crosses a call boundary before the
	// interceptor tests it.
	assert.True(t, control.NeedsElevation(errors.Join(errors.New("context"), marked), markedHeader))

	// No refusal at all, marker or not.
	assert.False(t, control.NeedsElevation(nil, markedHeader))
	assert.False(t, control.NeedsElevation(errors.New("plain"), new(connect.Header)))
	assert.False(t, control.NeedsElevation(
		connect.NewError(connect.CodeUnauthenticated, "token expired"), new(connect.Header)))
}
