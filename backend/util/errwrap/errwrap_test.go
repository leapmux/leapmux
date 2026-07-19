package errwrap

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapIsNilSafeAndPreservesChain(t *testing.T) {
	// The nil-short-circuit is the helper's reason to exist: fmt.Errorf("%w",
	// nil) would return a non-nil "%!w(<nil>)" error, so a cleanup site must not
	// build a message around a nil cause.
	require.Nil(t, Wrap(nil, "ctx"))

	cause := errors.New("boom")
	wrapped := Wrap(cause, "ctx")
	require.ErrorIs(t, wrapped, cause)
	require.Equal(t, "ctx: boom", wrapped.Error())
}
