//go:build unix

package procutil

import (
	"context"
	"errors"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessOwnerCaptureRejectsAnAbsentContextWithoutPanic(t *testing.T) {
	fixture := startRootExitFixture(t, t.TempDir())
	var absentContext context.Context
	var captureErr error
	require.NotPanics(t, func() { captureErr = fixture.owner.Capture(absentContext) })
	require.ErrorContains(t, captureErr, "context is absent")
	fixture.requirePong(t)
}

type unidentifiedProcessCommand struct {
	child     *os.Process
	waitCount int
	waitErr   error
}

func (*unidentifiedProcessCommand) start() error { return nil }
func (c *unidentifiedProcessCommand) wait() error {
	c.waitCount++
	return c.waitErr
}
func (c *unidentifiedProcessCommand) process() *os.Process  { return c.child }
func (*unidentifiedProcessCommand) state() *os.ProcessState { return nil }

func TestProcessOwnerWaitDoesNotRepeatStartupFailureCleanup(t *testing.T) {
	child, err := os.FindProcess(math.MaxInt32)
	require.NoError(t, err)
	t.Cleanup(func() { _ = child.Release() })
	command := &unidentifiedProcessCommand{child: child, waitErr: errors.New("native startup cleanup wait")}
	owner := &ProcessOwner{cmd: command, children: make(map[ProcessIdentity]struct{})}
	startErr := owner.Start()
	require.ErrorContains(t, startErr, "supplied no creation identity")
	require.ErrorIs(t, startErr, command.waitErr)
	require.Equal(t, 1, command.waitCount, "failed startup reaps its native command once")
	require.ErrorIs(t, owner.Wait(), command.waitErr)
	require.ErrorIs(t, owner.Wait(), command.waitErr)
	require.Equal(t, 1, command.waitCount, "later callers must receive the existing native wait result")
}
