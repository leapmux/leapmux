package procutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOwnStartedProcessRejectsTheWorkerIdentity(t *testing.T) {
	identity, exists := IdentifyProcess(os.Getpid())
	require.True(t, exists)
	owner, err := OwnStartedProcess(identity)
	require.ErrorContains(t, err, "worker", "an adopted owner must not signal the worker or its group")
	require.Nil(t, owner)
}
