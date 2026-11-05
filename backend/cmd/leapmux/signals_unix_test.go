//go:build !windows

package main

import (
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/locallisten/locallistentest"
)

// Closing the terminal that runs `leapmux solo` delivers SIGHUP. The command
// must drain -- release the Hub runtime lease, stop the worker and the hub --
// and exit 0, not die by the signal with the lease still held.
func TestSoloStopsInOrderOnAHangUp(t *testing.T) {
	locallistentest.SandboxHome(t)
	uniqueListenEnv(t)
	p := startWithStderrPipe(t, "solo")
	p.waitReadiness(t, "leapmux solo listening")

	require.NoError(t, p.cmd.Process.Signal(syscall.SIGHUP))
	select {
	case err := <-p.exited:
		require.NoError(t, err, "solo must exit 0 after the hang-up\noutput:\n%s", p.output.String())
	case <-time.After(childTimeout):
		t.Fatalf("solo did not exit after the hang-up\noutput:\n%s", p.output.String())
	}
	assert.Contains(t, p.output.String(), "released the Hub runtime lease",
		"the hang-up must run the drain that releases the lease")
}
