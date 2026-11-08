package servicelaunch

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeLaunchAcceptsFullyQualifiedWindowsPaths(t *testing.T) {
	for _, path := range []string{`\\server\share\worker.yaml`, `\\?\C:\config\worker.yaml`, `\\?\UNC\server\share\worker.yaml`} {
		spec := testSpec()
		spec.ConfigFile = path
		_, err := LaunchSpecFromService(spec)
		require.NoError(t, err)
	}
	for _, path := range []string{`C:worker.yaml`, `\config\worker.yaml`, `/config/worker.yaml`} {
		spec := testSpec()
		spec.ConfigFile = path
		_, err := LaunchSpecFromService(spec)
		require.ErrorContains(t, err, "absolute")
	}
	path := filepath.Join(t.TempDir(), "missing-environment.yaml")
	spec := LaunchSpec{EnvironmentFile: &path}
	_, err := spec.LoadEnvironment()
	require.ErrorContains(t, err, "open the environment file")
}
