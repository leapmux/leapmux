//go:build windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Explorer exits 1 after a SUCCESSFUL open, so its exit code must not be read
// as a verdict -- doing so would report every file-manager launch as failed.
func TestWindowsFileManagerCommand_ExitCodeCarriesNoVerdict(t *testing.T) {
	t.Parallel()
	plan := fileManagerCommand(`C:\repo`)
	assert.Equal(t, []string{"explorer.exe", `C:\repo`}, plan.cmd.Args)
	assert.False(t, plan.exitMeaningful)
}
