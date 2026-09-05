//go:build windows

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWindowsSpecs_GatesMatchPlatform(t *testing.T) {
	t.Parallel()
	ids := map[string]bool{}
	for _, spec := range defaultExternalAppSpecs() {
		ids[spec.ID] = true
	}
	assert.True(t, ids["notepad-plus-plus"], "Notepad++ must appear on Windows")
	assert.False(t, ids["xcode"], "Xcode must not appear on Windows")
}

// Explorer exits 1 after a SUCCESSFUL open, so its exit code must not be read
// as a verdict -- doing so would report every file-manager launch as failed.
func TestWindowsFileManagerCommand_ExitCodeCarriesNoVerdict(t *testing.T) {
	t.Parallel()
	cmd, exitMeaningful := fileManagerCommand(`C:\repo`)
	assert.Equal(t, []string{"explorer.exe", `C:\repo`}, cmd.Args)
	assert.False(t, exitMeaningful)
}
