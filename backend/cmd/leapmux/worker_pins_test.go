package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The --data-dir help states the variable that the worker configuration
// reads. LEAPMUX_DATA_DIR belongs to the Docker start script only, so the
// binary never reads it.
func TestCrossWorkerPinsDataDirHelpStatesTheWorkerConfiguration(t *testing.T) {
	result := invokeLifecycleCLI(t, t.TempDir(), "worker", "cross-worker-pins", "list", "--help")
	require.Equal(t, 0, result.exitCode, result.stderr)
	help := result.stdout + result.stderr
	assert.Contains(t, help, "LEAPMUX_WORKER_DATA_DIR")
	assert.NotContains(t, help, "LEAPMUX_DATA_DIR")
}
