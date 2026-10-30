//go:build windows

package launch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"
)

func TestIndependentProcessRequestsBreakawayBeforeTheProfile(t *testing.T) {
	t.Parallel()
	for _, independent := range []bool{false, true} {
		cmd, _, _ := Wrap(context.Background(), WrapSpec{Shell: "powershell.exe", Launch: Spec{Program: "helper.exe"}, IndependentProcess: independent})
		require.NotNil(t, cmd.SysProcAttr)
		assert.Equal(t, independent, cmd.SysProcAttr.CreationFlags&windows.CREATE_BREAKAWAY_FROM_JOB != 0)
		assert.Equal(t, independent, cmd.SysProcAttr.CreationFlags&windows.CREATE_NEW_PROCESS_GROUP != 0)
		assert.True(t, cmd.SysProcAttr.HideWindow)
	}
}
