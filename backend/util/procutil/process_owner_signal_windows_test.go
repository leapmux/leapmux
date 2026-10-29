//go:build windows

package procutil

import (
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessOwnerSignalGroupRetainsUnsupportedWindowsDelivery(t *testing.T) {
	owner := startObservedOwner(t, nil)
	require.ErrorIs(t, owner.SignalGroup(syscall.SIGINT), syscall.Errno(syscall.EWINDOWS))
	require.True(t, owner.root.Runs())
	require.NoError(t, owner.Err(), "normal shutdown can still finish after an unsupported signal")
}
