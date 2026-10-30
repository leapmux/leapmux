package procutil

import (
	"os/exec"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProcessOwnerSignalGroupAcceptsAbsentOrUnstartedOwners(t *testing.T) {
	t.Parallel()
	for _, owner := range []*ProcessOwner{nil, PrepareProcess(nil), PrepareProcess(exec.Command("unused-command"))} {
		require.NoError(t, owner.SignalGroup(syscall.SIGTERM))
	}
}
