package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeProgram writes an executable file named name in dir, creating dir.
func writeProgram(t *testing.T, dir, name, contents string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o755))
	return path
}

const testSidecarName = "leapmux-desktop-service"

func TestResolveBundledCli_BesideTheSidecar(t *testing.T) {
	t.Parallel()
	// The macOS .app: Contents/MacOS holds the sidecar and the CLI.
	dir := filepath.Join(t.TempDir(), "LeapMux.app", "Contents", "MacOS")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	cli := writeProgram(t, dir, cliBinaryName, "cli")

	assert.Equal(t, cli, resolveBundledCli(exe))
}

func TestResolveBundledCli_WindowsInstallerCliDirectory(t *testing.T) {
	t.Parallel()
	// The Windows installer ships cli\leapmux.exe in the install directory.
	dir := filepath.Join(t.TempDir(), "Programs", "LeapMux")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	cli := writeProgram(t, filepath.Join(dir, "cli"), cliBinaryName, "cli")

	assert.Equal(t, cli, resolveBundledCli(exe))
}

func TestResolveBundledCli_LinuxPackageBinDirectory(t *testing.T) {
	t.Parallel()
	// The .deb installs the sidecar under /usr/lib/<product>/ and the CLI as
	// /usr/bin/leapmux.
	usr := filepath.Join(t.TempDir(), "usr")
	exe := writeProgram(t, filepath.Join(usr, "lib", "leapmux-desktop"), testSidecarName, "svc")
	cli := writeProgram(t, filepath.Join(usr, "bin"), cliBinaryName, "cli")

	assert.Equal(t, cli, resolveBundledCli(exe), "the path is clean, with no `..`")
}

func TestResolveBundledCli_AppImageShipsNone(t *testing.T) {
	t.Parallel()
	// An AppImage mounts usr/lib/<product>/ with the sidecar and no CLI.
	usr := filepath.Join(t.TempDir(), "mount", "usr")
	exe := writeProgram(t, filepath.Join(usr, "lib", "leapmux-desktop"), testSidecarName, "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(usr, "bin"), 0o755))

	assert.Empty(t, resolveBundledCli(exe))
}

func TestResolveBundledCli_DevLayoutClimbsFourLevels(t *testing.T) {
	t.Parallel()
	// <repo>/desktop/go/bin/<sidecar>, and the CLI at <repo>/leapmux.
	root := t.TempDir()
	exe := writeProgram(t, filepath.Join(root, "desktop", "go", "bin"), testSidecarName, "svc")
	cli := writeProgram(t, root, cliBinaryName, "cli")

	assert.Equal(t, cli, resolveBundledCli(exe))
}

func TestResolveBundledCli_AnInstalledLayoutWinsOverTheDevLayout(t *testing.T) {
	t.Parallel()
	// A stray leapmux four levels up never steals an installed CLI.
	root := t.TempDir()
	binDir := filepath.Join(root, "Contents", "MacOS", "bin")
	exe := writeProgram(t, binDir, testSidecarName, "svc")
	sibling := writeProgram(t, binDir, cliBinaryName, "canonical")
	writeProgram(t, root, cliBinaryName, "decoy")

	assert.Equal(t, sibling, resolveBundledCli(exe))
}

func TestResolveBundledCli_TheOrderOfTheInstalledLayouts(t *testing.T) {
	t.Parallel()
	usr := filepath.Join(t.TempDir(), "usr")
	dir := filepath.Join(usr, "lib", "leapmux-desktop")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	packaged := writeProgram(t, filepath.Join(usr, "bin"), cliBinaryName, "deb")
	assert.Equal(t, packaged, resolveBundledCli(exe))

	installer := writeProgram(t, filepath.Join(dir, "cli"), cliBinaryName, "msi")
	assert.Equal(t, installer, resolveBundledCli(exe), "the cli directory comes before ../../bin")

	sibling := writeProgram(t, dir, cliBinaryName, "app")
	assert.Equal(t, sibling, resolveBundledCli(exe), "a sibling comes first")
}

func TestResolveBundledCli_NoneFound(t *testing.T) {
	t.Parallel()
	exe := writeProgram(t, t.TempDir(), testSidecarName, "svc")
	assert.Empty(t, resolveBundledCli(exe))
}

func TestResolveBundledCli_ADirectoryNamedLikeTheCliIsNoCli(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeProgram(t, dir, testSidecarName, "svc")
	require.NoError(t, os.Mkdir(filepath.Join(dir, cliBinaryName), 0o755))
	assert.Empty(t, resolveBundledCli(exe))
}

func TestResolveBundledCli_AFileWithNoExecBitIsNoCli(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no exec bits: any regular file can run")
	}
	dir := t.TempDir()
	exe := writeProgram(t, dir, testSidecarName, "svc")
	require.NoError(t, os.WriteFile(filepath.Join(dir, cliBinaryName), []byte("cli"), 0o644))
	assert.Empty(t, resolveBundledCli(exe))
}

func TestResolveBundledCli_ShallowExeDoesNotCrash(t *testing.T) {
	t.Parallel()
	// The parent chain bottoms out before four hops: parentN returns "".
	assert.Empty(t, resolveBundledCli(string(filepath.Separator)+testSidecarName))
}

func TestCliBinaryNameFollowsTheOS(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		assert.Equal(t, "leapmux.exe", cliBinaryName)
	} else {
		assert.Equal(t, "leapmux", cliBinaryName)
	}
}
