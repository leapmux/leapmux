// Package workermgrtest provides shared Conn fixtures for hub packages that
// exercise the worker registry without owning the Connect handler.
package workermgrtest

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	leapmuxv1 "github.com/leapmux/leapmux/generated/proto/leapmux/v1"
	"github.com/leapmux/leapmux/internal/hub/workermgr"
	"github.com/leapmux/leapmux/internal/sendq"
)

// Recorder is a mutex-guarded capture of ConnectResponse frames. A drained
// Conn invokes Write from the pump goroutine, so unguarded counters race under
// -race.
type Recorder struct {
	mu      sync.Mutex
	msgs    []*leapmuxv1.ConnectResponse
	err     error
	onWrite func(*leapmuxv1.ConnectResponse)
}

// Write records msg (or returns a previously SetErr). Safe for concurrent use.
func (r *Recorder) Write(msg *leapmuxv1.ConnectResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.msgs = append(r.msgs, msg)
	if r.onWrite != nil {
		r.onWrite(msg)
	}
	return nil
}

// Messages returns a snapshot of recorded frames.
func (r *Recorder) Messages() []*leapmuxv1.ConnectResponse {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*leapmuxv1.ConnectResponse, len(r.msgs))
	copy(out, r.msgs)
	return out
}

// Len returns the number of recorded frames.
func (r *Recorder) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.msgs)
}

// SetErr makes subsequent Write calls return err.
func (r *Recorder) SetErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.err = err
}

// SetOnWrite installs a hook invoked under the recorder lock after each
// successful write. Use it to Complete pending requests or signal channels.
func (r *Recorder) SetOnWrite(fn func(*leapmuxv1.ConnectResponse)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onWrite = fn
}

// NewRecordedConn builds a Conn whose pump is drained by a background
// goroutine, so Send/SendControl become observable via the Recorder. The conn
// is fenced on cleanup.
func NewRecordedConn(tb testing.TB, workerID string) (*workermgr.Conn, *Recorder) {
	tb.Helper()
	rec := &Recorder{}
	return NewConnWithWrite(tb, workerID, rec.Write), rec
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
func NewConnWithWrite(tb testing.TB, workerID string, write func(*leapmuxv1.ConnectResponse) error) *workermgr.Conn {
	tb.Helper()
	require.NotNil(tb, write)
	ctx, cancel := context.WithCancel(context.Background())
	pool := sendq.NewMaxBytesPoolForTest()
	conn, pump := workermgr.NewConn(ctx, cancel, workerID, "", pool, write, nil)
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
