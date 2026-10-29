package service

import (
	"context"
	"testing"

	"connectrpc.com/connect/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A raw ServerFunc invocation carries no CallInfo, and connect v2 documents that
// case: CallInfoForServerContext reports false. The accessor must not panic
// there, because response metadata is written before anyone checks.
func TestServerCallInfo_AbsentCallReadsAsEmptyAndSurvivesWrites(t *testing.T) {
	info := serverCallInfo(context.Background())
	require.NotNil(t, info, "a call with no transport must still get a usable handle")

	assert.Empty(t, info.RequestHeader().Get("Cookie"))
	assert.Empty(t, info.RequestHeader().Get("Authorization"))

	// Must not panic. The slide cookie and the elevation marker both write
	// before the call is known to carry a transport.
	info.ResponseHeader().Set("Leapmux-Elevation-Required", "1")
	assert.Equal(t, "1", info.ResponseHeader().Get("Leapmux-Elevation-Required"),
		"a detached header absorbs its own writes")
}

// A live RPC carries one CallInfo, and the accessor must hand back THAT one.
// Response metadata written through any other handle would never reach the
// wire, which is how a marker or a cookie goes missing with nothing to point
// at the cause.
func TestServerCallInfo_ReturnsTheCallsOwnInfo(t *testing.T) {
	var seen *connect.CallInfo
	got, err := callUnderServerInfo(func(ctx context.Context) error {
		seen = serverCallInfo(ctx)
		return nil
	})
	require.NoError(t, err)
	require.NotNil(t, seen)
	assert.Same(t, got, seen,
		"the response header must be the one the transport flushes")
}
