package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeExec writes a file with mode in dir, creating dir if needed.
func writeExec(t *testing.T, dir, name string, contents []byte, mode os.FileMode) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, contents, mode))
	return path
}

// writeProgram writes an executable file named name in dir, creating dir.
func writeProgram(t *testing.T, dir, name, contents string) string {
	t.Helper()
	return writeExec(t, dir, name, []byte(contents), 0o755)
}

const testSidecarName = "leapmux-desktop-service"

func TestResolveBundledCli_BesideTheSidecarOnDarwin(t *testing.T) {
	t.Parallel()
	// The macOS .app: Contents/MacOS holds the sidecar and the CLI.
	dir := filepath.Join(t.TempDir(), "LeapMux.app", "Contents", "MacOS")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	cli := writeProgram(t, dir, "leapmux", "cli")

	assert.Equal(t, cli, resolveBundledCliFor("darwin", false, exe))
}

func TestResolveBundledCli_WindowsInstallerCliDirectory(t *testing.T) {
	t.Parallel()
	// The Windows installer ships cli\leapmux.exe in the install directory.
	dir := filepath.Join(t.TempDir(), "Programs", "LeapMux")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	cli := writeProgram(t, filepath.Join(dir, "cli"), "leapmux.exe", "cli")

	assert.Equal(t, cli, resolveBundledCliFor("windows", false, exe))
}

func TestResolveBundledCli_LinuxPackageBinDirectory(t *testing.T) {
	t.Parallel()
	// The .deb installs the sidecar under /usr/lib/<product>/ and the CLI as
	// /usr/bin/leapmux.
	usr := filepath.Join(t.TempDir(), "usr")
	exe := writeProgram(t, filepath.Join(usr, "lib", "leapmux-desktop"), testSidecarName, "svc")
	cli := writeProgram(t, filepath.Join(usr, "bin"), "leapmux", "cli")

	assert.Equal(t, cli, resolveBundledCliFor("linux", false, exe), "the path is clean, with no `..`")
}

func TestResolveBundledCli_AppImageShipsNone(t *testing.T) {
	t.Parallel()
	// An AppImage mounts usr/lib/<product>/ with the sidecar and no CLI.
	usr := filepath.Join(t.TempDir(), "mount", "usr")
	exe := writeProgram(t, filepath.Join(usr, "lib", "leapmux-desktop"), testSidecarName, "svc")
	require.NoError(t, os.MkdirAll(filepath.Join(usr, "bin"), 0o755))

	assert.Empty(t, resolveBundledCliFor("linux", false, exe))
}

func TestResolveBundledCli_DevLayoutClimbsFourLevelsOnlyInDevMode(t *testing.T) {
	t.Parallel()
	// <repo>/desktop/go/bin/<sidecar>, and the CLI at <repo>/leapmux.
	root := t.TempDir()
	exe := writeProgram(t, filepath.Join(root, "desktop", "go", "bin"), testSidecarName, "svc")
	cli := writeProgram(t, root, "leapmux", "cli")

	assert.Empty(t, resolveBundledCliFor("darwin", false, exe), "production must not climb four levels")
	assert.Empty(t, resolveBundledCliFor("linux", false, exe), "production must not climb four levels")
	assert.Equal(t, cli, resolveBundledCliFor("darwin", true, exe))
	assert.Equal(t, cli, resolveBundledCliFor("linux", true, exe))
}

func TestResolveBundledCli_ProductionDoesNotProbeForeignOSLayouts(t *testing.T) {
	t.Parallel()
	usr := filepath.Join(t.TempDir(), "usr")
	dir := filepath.Join(usr, "lib", "leapmux-desktop")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	writeProgram(t, filepath.Join(dir, "cli"), "leapmux", "windows-layout")
	writeProgram(t, filepath.Join(dir, "cli"), "leapmux.exe", "windows-layout")
	writeProgram(t, filepath.Join(usr, "bin"), "leapmux", "linux-layout")

	assert.Empty(t, resolveBundledCliFor("darwin", false, exe),
		"macOS must not probe Windows cli/ or Linux ../../bin layouts")
	assert.Empty(t, resolveBundledCliFor("windows", false, filepath.Join(usr, "bin", testSidecarName)),
		"Windows must not probe sibling or Linux ../../bin layouts")
}

func TestResolveBundledCli_AnInstalledLayoutWinsOverTheDevLayout(t *testing.T) {
	t.Parallel()
	// A stray leapmux four levels up never steals an installed CLI.
	root := t.TempDir()
	binDir := filepath.Join(root, "Contents", "MacOS", "bin")
	exe := writeProgram(t, binDir, testSidecarName, "svc")
	sibling := writeProgram(t, binDir, "leapmux", "canonical")
	writeProgram(t, root, "leapmux", "decoy")

	assert.Equal(t, sibling, resolveBundledCliFor("darwin", true, exe))
}

func TestResolveBundledCli_LinuxLayoutOrder(t *testing.T) {
	t.Parallel()
	usr := filepath.Join(t.TempDir(), "usr")
	dir := filepath.Join(usr, "lib", "leapmux-desktop")
	exe := writeProgram(t, dir, testSidecarName, "svc")
	packaged := writeProgram(t, filepath.Join(usr, "bin"), "leapmux", "deb")
	assert.Equal(t, packaged, resolveBundledCliFor("linux", false, exe))

	sibling := writeProgram(t, dir, "leapmux", "unbundled")
	assert.Equal(t, sibling, resolveBundledCliFor("linux", false, exe), "a sibling comes before ../../bin on Linux")
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
	require.NoError(t, os.Mkdir(filepath.Join(dir, "leapmux"), 0o755))
	assert.Empty(t, resolveBundledCliFor("darwin", false, exe))
}

func TestResolveBundledCli_AFileWithNoExecBitIsNoCli(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no exec bits: any regular file can run")
	}
	dir := t.TempDir()
	exe := writeProgram(t, dir, testSidecarName, "svc")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "leapmux"), []byte("cli"), 0o644))
	assert.Empty(t, resolveBundledCliFor("darwin", false, exe))
}

func TestResolveBundledCli_ShallowExeDoesNotCrash(t *testing.T) {
	t.Parallel()
	// Shallow executable paths bottom out before ../../bin and four hops:
	// neither root /bin/leapmux nor a four-level ancestor is ever probed.
	shallow := string(filepath.Separator) + testSidecarName
	candidates := bundledCliCandidatesFor("linux", true, shallow)
	assert.NotContains(t, candidates, filepath.Join(string(filepath.Separator), "bin", "leapmux"))
	assert.Empty(t, resolveBundledCliFor("linux", true, shallow))

	oneDeep := filepath.Join(string(filepath.Separator), "opt", testSidecarName)
	assert.NotContains(t, bundledCliCandidatesFor("linux", true, oneDeep), filepath.Join(string(filepath.Separator), "bin", "leapmux"))
}

func TestResolveExecutableBundledCli_FollowsSymlinkedSidecar(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require elevated privileges on Windows")
	}
	installDir := filepath.Join(t.TempDir(), "LeapMux.app", "Contents", "MacOS")
	realExe := writeProgram(t, installDir, testSidecarName, "svc")
	cli := writeProgram(t, installDir, cliBinaryName, "cli")

	linkDir := t.TempDir()
	symlinkExe := filepath.Join(linkDir, testSidecarName)
	require.NoError(t, os.Symlink(realExe, symlinkExe))

	resolvedInstallDir, err := filepath.EvalSymlinks(installDir)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join(resolvedInstallDir, cliBinaryName), resolveExecutableBundledCli(symlinkExe, false))
	assert.NotEqual(t, "", cli)
}

func TestCliBinaryNameFollowsTheOS(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "leapmux.exe", cliBinaryNameFor("windows"))
	assert.Equal(t, "leapmux", cliBinaryNameFor("darwin"))
	assert.Equal(t, "leapmux", cliBinaryNameFor("linux"))
}
