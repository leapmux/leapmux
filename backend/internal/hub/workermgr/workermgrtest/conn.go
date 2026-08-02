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
func NewConnWithWrite(tb testing.TB, workerID string, write func(*leapmuxv1.ConnectResponse) error) *workermgr.Conn {
	tb.Helper()
	require.NotNil(tb, write)
	ctx, cancel := context.WithCancel(context.Background())
	conn, pump := workermgr.NewConn(ctx, cancel, workerID, write, nil)
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
