//go:build linux

// The docker s6-overlay run script is only used inside the Linux container
// image, so its tests run only on Linux. They invoke /bin/sh directly, which
// isn't available on Windows runners.

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDockerRunScriptSoloMode(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")
	capturePath := filepath.Join(tmp, "args.txt")
	fakeLeapMux := filepath.Join(tmp, "leapmux")
	writeExecutable(t, fakeLeapMux, `#!/bin/sh
printf '%s\n' "$@" > "$LEAPMUX_CAPTURE"
`)

	cmd := exec.Command("/bin/sh", dockerRunScriptPath(t))
	cmd.Env = append(os.Environ(),
		"LEAPMUX_MODE=solo",
		"LEAPMUX_BIN="+fakeLeapMux,
		"LEAPMUX_DATA_DIR="+dataDir,
		"LEAPMUX_CAPTURE="+capturePath,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))

	args, err := os.ReadFile(capturePath)
	require.NoError(t, err)
	assert.Equal(t, "solo\n-config\n"+filepath.Join(dataDir, "solo", "solo.yaml")+"\n", string(args))
	assert.FileExists(t, filepath.Join(dataDir, "solo", "solo.yaml"))
}

func TestDockerRunScriptRejectsInvalidModeBeforeCreatingConfig(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "data")

	cmd := exec.Command("/bin/sh", dockerRunScriptPath(t))
	cmd.Env = append(os.Environ(),
		"LEAPMUX_MODE=bogus",
		"LEAPMUX_DATA_DIR="+dataDir,
	)
	output, err := cmd.CombinedOutput()
	require.Error(t, err)
	assert.Contains(t, string(output), "LEAPMUX_MODE must be one of: hub, worker, dev, solo")
	assert.NoDirExists(t, filepath.Join(dataDir, "bogus"))
}

func dockerRunScriptPath(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Join(filepath.Dir(file), "..", "..", "..", "docker", "rootfs", "etc", "s6-overlay", "s6-rc.d", "leapmux", "run")
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	require.NoError(t, os.WriteFile(path, []byte(content), 0o755))
}
