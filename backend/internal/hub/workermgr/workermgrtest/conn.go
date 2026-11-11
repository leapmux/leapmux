// Package workermgrtest provides shared Conn fixtures for hub packages that
// exercise the worker registry without owning the worker stream handler.
package workermgrtest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
	"github.com/leapmux/leapmux/internal/workerkeys/workerkeystest"
)

// TestHello returns the WorkerHello the fixtures here build every Conn with: a
// classic-mode worker presenting the COMPLETE key set, because the Hub refuses a
// hello that carries only some of the three keys whatever its mode. The bytes
// come from workerkeystest, so a test can register a row with the same set. A
// test that registers a Conn of its own passes it to workermgr.NewConn.
func TestHello() *leapmuxv1.WorkerHello {
	return &leapmuxv1.WorkerHello{
		Keys:           workerkeystest.Keys(workerkeystest.SeededWorkerFill),
		EncryptionMode: leapmuxv1.EncryptionMode_ENCRYPTION_MODE_CLASSIC,
	}
}

// NewConn builds an auto-drained connection that discards written frames.
// Use NewConnWithWrite when a test needs to observe or block transport writes.
func NewConn(tb testing.TB, workerID string) *workermgr.Conn {
	tb.Helper()
	return NewConnWithWrite(tb, workerID, func(*leapmuxv1.ConnectResponse) error { return nil })
}

// NewConnWithWrite builds an auto-drained Conn with a custom write function.
// Use when the test needs to park, fail, or otherwise intercept the write.
// The pump is owned exclusively by the background drain goroutine and is not
// returned -- a second Drain would panic.
//
// Each fixture gets its OWN single-member pool, so a test that fills one Conn's
// queue cannot change what a sibling test's Conn is admitted -- and so no test
// depends on the host's memory. Tests that want to exercise shared-pool pressure
// build the pool themselves and call workermgr.NewConn directly.
//
// It comes from NewMaxBytesPoolForTest rather than a bare Capacity, because a
// pool left on the default floors grants a lone member max(Capacity-used,
// DefaultMaxFloor), which settles at about half of Capacity. A fixture whose
// ceiling is half the number it names would let an over-budget test pass while
// never reaching the branch it claims to exercise.
//
// The fixture names no owner. A Manager built by a test is unlimited unless it
// calls SetMaxWorkersPerUser, so the empty owner these conns share costs nothing
// -- but a test that DOES set that cap must build its conns with
// workermgr.NewConn and real owners, or every one of them lands in the same
// bucket.
//
// The Conn is built with TestHello. Use NewConnWithHello for a test that
// reads the handshake parameters.
func NewConnWithWrite(tb testing.TB, workerID string, write func(*leapmuxv1.ConnectResponse) error) *workermgr.Conn {
	tb.Helper()
	return NewConnWithHello(tb, workerID, TestHello(), write)
}

// NewConnWithHello is NewConnWithWrite for a Conn opened with the given
// WorkerHello, so GetWorkerHandshakeParams answers with its keys and mode.
func NewConnWithHello(tb testing.TB, workerID string, hello *leapmuxv1.WorkerHello, write func(*leapmuxv1.ConnectResponse) error) *workermgr.Conn {
	tb.Helper()
	require.NotNil(tb, write)
	require.NotNil(tb, hello)
	ctx, cancel := context.WithCancel(context.Background())
	pool := sendq.NewMaxBytesPoolForTest()
	conn, pump := workermgr.NewConn(ctx, cancel, workerID, "", pool, func(_ context.Context, msg *leapmuxv1.ConnectResponse) error { return write(msg) }, nil, hello)
	tb.Cleanup(conn.Fence)
	go drainUntilDone(conn, pump)
	return conn
}

func drainUntilDone(conn *workermgr.Conn, pump *workermgr.SendPump) {
	for {
		select {
		case <-pump.Ready():
			_ = pump.Drain()
		case <-conn.Done():
			return
		}
	}
}
