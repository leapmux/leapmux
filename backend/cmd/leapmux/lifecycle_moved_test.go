package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/lifecycle"
)

// A data directory that moved with its instance works at its new path. The
// service settings of the old path describe no service there, so they neither
// block a command nor survive the adoption that enable performs.
func TestEnableAdoptsAMovedDataDirectory(t *testing.T) {
	parent := mustCanonicalDir(t, t.TempDir())
	from := filepath.Join(parent, "before")
	manager := &nativeStartRecorder{t: t}
	enable := func(dataDir string) error {
		_, err := captureStdout(t, func() error {
			return runModeEnableWithController("hub", nativeRecorderController(manager))(cmdCtx{}, []string{"--data-dir", dataDir, "--startup=login", "--yes"})
		})
		return err
	}
	require.NoError(t, enable(from))
	require.FileExists(t, lifecycle.PathsOf(from).ServiceSpec)

	to := filepath.Join(parent, "after")
	require.NoError(t, os.Rename(from, to))

	configuration, _, err := loadLifecycleCommand("hub", "status", []string{"--data-dir", to})
	require.NoError(t, err)
	require.NoError(t, validateLifecycleSelection(configuration), "status must accept the moved directory")
	controller, err := nativeRecorderController(manager)("hub", to)
	require.NoError(t, err)
	_, err = controller.ReadSpec()
	require.ErrorIs(t, err, os.ErrNotExist, "the settings of the old path describe no service at the new path")

	require.NoError(t, enable(to))
	spec, err := lifecycle.ReadServiceSpec(lifecycle.PathsOf(to).ServiceSpec)
	require.NoError(t, err)
	assert.Equal(t, to, spec.DataDirectory)
	require.Len(t, manager.installs, 2)
	assert.Equal(t, to, manager.installs[1].DataDirectory)

	require.NoError(t, enable(to), "an adopted directory needs no second adoption")
}
