package tunnelflow

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// The flow-control invariants are enforced at compile time by the uint()
// conversion block in tunnelflow.go. This test cannot be what catches a bad
// retune and must not be read as such: it lives in the SAME package, so a
// violated invariant underflows one of those constant conversions and the
// package fails to compile -- the test binary is never built and these
// assertions never run. The build breaks first, every time.
//
// It earns its place only as executable documentation: it names each
// relationship in plain Go with the consequence of breaking it, which the
// bare `_ = uint(A - B)` lines cannot express, and it becomes the live guard
// if that block is ever removed. Assert nothing here that the compile-time
// block does not already pin, or the two will disagree about which is
// authoritative.
func TestFlowControlInvariants(t *testing.T) {
	assert.LessOrEqual(t, WriteWindowFrames, MaxWriteSeqLookahead,
		"client send window must fit under the worker's write-seq lookahead, else legitimate in-window frames are NAKed")
	assert.Less(t, InitialReadWindow, ReadBufFrames,
		"worker read window must stay below the client read buffer, else recvLoop starves on a slow tunnel consumer")
	assert.Less(t, ReadCreditBatch, InitialReadWindow,
		"client credit batch must stay below the worker read window, else a steady consumer stalls waiting for a grant")
}
