package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSoloInstance mirrors solo.Instance's contract: hubErr is assigned once, so
// Wait() and Stop() both hand back the IDENTICAL error value (Stop ends in Wait).
// A fake that returned two distinct errors would hide the double-report bug this
// file pins, because errors.Join of two different values reads as two real
// failures.
type fakeSoloInstance struct {
	hubErr error
	// hubExited gates Wait so a test can decide whether the hub ends on its own
	// (exitHub before waitSolo) or only once the caller stops it.
	hubExited chan struct{}
	exitOnce  sync.Once

	mu      sync.Mutex
	stopped int
}

func (f *fakeSoloInstance) Wait() error {
	<-f.hubExited
	return f.hubErr
}

// exitHub ends the hub's serve loop. Idempotent because both a test (hub dies on
// its own) and Stop (caller shuts it down) may reach it.
func (f *fakeSoloInstance) exitHub() {
	f.exitOnce.Do(func() { close(f.hubExited) })
}

func (f *fakeSoloInstance) Stop() error {
	f.mu.Lock()
	f.stopped++
	f.mu.Unlock()
	f.exitHub() // Stop cancels the instance, so a blocked Wait unblocks.
	return f.hubErr
}

func (f *fakeSoloInstance) stopCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stopped
}

// newFakeSoloInstance returns an instance whose hub has NOT exited yet.
func newFakeSoloInstance(hubErr error) *fakeSoloInstance {
	return &fakeSoloInstance{hubErr: hubErr, hubExited: make(chan struct{})}
}

// The Hub's terminal error must reach the user exactly ONCE.
//
// Wait() and Stop() return the same value, so a waitSolo that both returns the
// error from the hubExited arm AND joins Stop's error prints it twice --
// handleRunError does a plain Fprintln of the joined error, and errors.Join
// renders one message per line.
func TestWaitSoloReportsHubErrorOnce(t *testing.T) {
	sentinel := errors.New("hub serve: lease lost")
	inst := newFakeSoloInstance(sentinel)
	inst.exitHub() // The hub has already exited on its own.

	err := waitSolo(context.Background(), inst)

	require.Error(t, err)
	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, strings.Count(err.Error(), "lease lost"),
		"the hub error must be reported once, not joined with itself")
	assert.Equal(t, 1, inst.stopCount(), "the instance must be stopped exactly once")
}

// A clean Ctrl-C is not a failure: the context fires, the hub exits without an
// error, and waitSolo returns nil so main exits 0.
func TestWaitSoloSignalShutdownReturnsNil(t *testing.T) {
	inst := newFakeSoloInstance(nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	assert.NoError(t, waitSolo(ctx, inst))
	assert.Equal(t, 1, inst.stopCount(), "the instance must be stopped on the signal path")
}

// A hub error surfacing during the Ctrl-C path still reaches the user: the signal
// arm reports nothing itself, so the deferred Stop is what carries it -- and it
// must carry it only once here too.
func TestWaitSoloSignalShutdownSurfacesHubError(t *testing.T) {
	sentinel := errors.New("hub serve: listener closed unexpectedly")
	inst := newFakeSoloInstance(sentinel)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitSolo(ctx, inst)

	require.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, strings.Count(err.Error(), "listener closed unexpectedly"))
}
