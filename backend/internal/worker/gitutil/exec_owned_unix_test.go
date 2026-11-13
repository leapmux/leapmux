//go:build unix

package gitutil

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutorReclaimsOnlyVerifiedLiveHelpers(t *testing.T) {
	first := startGitPipeFixture(t)
	firstFilter := first.acceptHelper(t, "filter")
	other := startGitPipeFixture(t)
	otherFilter := other.acceptHelper(t, "filter")
	first.cancel()
	require.Error(t, first.wait(t))
	_, writeErr := fmt.Fprintln(firstFilter.connection, "ping")
	if writeErr != nil {
		require.True(t, errors.Is(writeErr, syscall.EPIPE) || errors.Is(writeErr, syscall.ECONNRESET), "observe helper exit, not a control timeout: %v", writeErr)
	} else {
		reply, readErr := firstFilter.reader.ReadString('\n')
		require.Error(t, readErr, "owned cancellation must close the live helper control; got %q", reply)
		require.True(t, errors.Is(readErr, io.EOF) || errors.Is(readErr, syscall.ECONNRESET), "observe helper exit, not a control timeout: %v", readErr)
	}
	// A ping proves the other helper still executes, including on systems
	// where an unreaped zombie would also satisfy ProcessIdentity.Runs.
	otherFilter.command(t, "ping")
	reply, err := otherFilter.reader.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "pong\n", reply)
	otherFilter.command(t, "finish")
	require.NoError(t, other.wait(t))
}
