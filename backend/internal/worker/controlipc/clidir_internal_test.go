package controlipc

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/controlenv"
	"github.com/leapmux/leapmux/lifecycle"
)

// cliTarget writes a program that prints marker, and returns its path. Only
// Unix runs it; on Windows it is a file to link.
func cliTarget(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "leapmux-under-test")
	testutil.WriteExecutable(t, path, "#!/bin/sh\necho '"+marker+"'\n")
	return path
}

// runDirOf returns the run directory that PrepareCLIDirectory uses for dataDir.
func runDirOf(t *testing.T, dataDir string) string {
	t.Helper()
	canonical, err := lifecycle.CanonicalDir(dataDir)
	require.NoError(t, err)
	return filepath.Join(canonical, runDirName)
}

// cliDirectories lists the CLI directories in runDir.
func cliDirectories(t *testing.T, runDir string) []string {
	t.Helper()
	entries, err := os.ReadDir(runDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), cliDirectoryPrefix) {
			names = append(names, entry.Name())
		}
	}
	return names
}

func TestPrepareCLIDirectory(t *testing.T) {
	t.Parallel()

	t.Run("links the executable into a private directory of its own", func(t *testing.T) {
		t.Parallel()
		target := cliTarget(t, "worker-cli")
		dataDir := t.TempDir()

		directory, err := PrepareCLIDirectory(dataDir, target)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, directory.Close()) })

		link := directory.Executable()
		require.True(t, filepath.IsAbs(link), "LEAPMUX_CONTROL_CLI must be absolute: %s", link)
		assert.Equal(t, cliExecutableName(runtime.GOOS), filepath.Base(link))
		dir := filepath.Dir(link)
		assert.Equal(t, runDirOf(t, dataDir), filepath.Dir(dir), "the directory lives in the run directory of the data directory")
		assert.True(t, strings.HasPrefix(filepath.Base(dir), cliDirectoryPrefix))

		entries, err := os.ReadDir(dir)
		require.NoError(t, err)
		require.Len(t, entries, 1, "the directory holds the link and nothing else")

		linkInfo, err := os.Stat(link)
		require.NoError(t, err)
		targetInfo, err := os.Stat(target)
		require.NoError(t, err)
		assert.True(t, os.SameFile(targetInfo, linkInfo),
			"on one file system the link is a hard link, the executable itself")

		if runtime.GOOS != "windows" {
			info, err := os.Stat(dir)
			require.NoError(t, err)
			assert.Zero(t, info.Mode().Perm()&0o077, "only this account may enter the directory, mode %s", info.Mode().Perm())
			out, err := exec.Command(link).Output()
			require.NoError(t, err)
			assert.Equal(t, "worker-cli\n", string(out))
		}
	})

	t.Run("links the file that a symbolic link resolves to", func(t *testing.T) {
		t.Parallel()
		target := cliTarget(t, "worker-cli")
		alias := filepath.Join(t.TempDir(), "leapmux")
		if err := os.Symlink(target, alias); err != nil {
			t.Skipf("this account cannot create a symbolic link: %v", err)
		}

		directory, err := PrepareCLIDirectory(t.TempDir(), alias)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, directory.Close()) })

		// An upgrade that points the alias at another file must not change
		// the CLI of a running Worker.
		require.NoError(t, os.Remove(alias))
		require.NoError(t, os.Symlink(cliTarget(t, "upgraded-cli"), alias))
		linkInfo, err := os.Stat(directory.Executable())
		require.NoError(t, err)
		targetInfo, err := os.Stat(target)
		require.NoError(t, err)
		assert.True(t, os.SameFile(targetInfo, linkInfo), "the link stands for the resolved file, not for the alias")
	})

	t.Run("removes the directories that ended processes left", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		runDir := runDirOf(t, dataDir)
		require.NoError(t, os.MkdirAll(filepath.Join(runDir, "cli-old"), 0o700))
		testutil.WriteExecutable(t, filepath.Join(runDir, "cli-old", "leapmux"), "#!/bin/sh\n")
		require.NoError(t, os.WriteFile(filepath.Join(runDir, "cli-file"), []byte("x"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(runDir, "control"), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(runDir, "control", "keep"), []byte("x"), 0o600))
		require.NoError(t, os.MkdirAll(filepath.Join(runDir, "other"), 0o700))
		outside := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(outside, "precious"), []byte("x"), 0o600))
		linkedOutside := os.Symlink(outside, filepath.Join(runDir, "cli-link")) == nil

		directory, err := PrepareCLIDirectory(dataDir, cliTarget(t, "worker-cli"))
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, directory.Close()) })

		assert.Equal(t, []string{filepath.Base(filepath.Dir(directory.Executable()))}, cliDirectories(t, runDir),
			"only the directory of this process remains")
		assert.DirExists(t, filepath.Join(runDir, "control"), "the control sockets stay")
		assert.FileExists(t, filepath.Join(runDir, "control", "keep"))
		assert.DirExists(t, filepath.Join(runDir, "other"), "an entry outside the CLI namespace stays")
		if linkedOutside {
			assert.FileExists(t, filepath.Join(outside, "precious"),
				"a stale entry that is a symbolic link goes without what it points at")
		}
	})

	t.Run("keeps a directory that it cannot remove, and prepares its own", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("this case denies the removal through a directory mode, which Windows ignores")
		}
		if os.Geteuid() == 0 {
			t.Skip("root removes a file whatever the mode of its directory")
		}
		dataDir := t.TempDir()
		runDir := runDirOf(t, dataDir)
		locked := filepath.Join(runDir, "cli-stuck", "locked")
		require.NoError(t, os.MkdirAll(locked, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(locked, "leapmux"), []byte("x"), 0o600))
		require.NoError(t, os.Chmod(locked, 0o500))
		t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

		directory, err := PrepareCLIDirectory(dataDir, cliTarget(t, "worker-cli"))
		require.NoError(t, err, "a leftover that it cannot remove does not cost this process its CLI")
		t.Cleanup(func() { assert.NoError(t, directory.Close()) })

		assert.ElementsMatch(t, []string{"cli-stuck", filepath.Base(filepath.Dir(directory.Executable()))}, cliDirectories(t, runDir),
			"the leftover stays for a later start, beside the directory of this process")
		assert.FileExists(t, directory.Executable())
	})

	t.Run("gives each call a directory of its own", func(t *testing.T) {
		t.Parallel()
		dataDir := t.TempDir()
		target := cliTarget(t, "worker-cli")

		first, err := PrepareCLIDirectory(dataDir, target)
		require.NoError(t, err)
		second, err := PrepareCLIDirectory(dataDir, target)
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, second.Close()) })

		assert.NotEqual(t, filepath.Dir(first.Executable()), filepath.Dir(second.Executable()),
			"a new process never reuses the link of an ended one")
		assert.NoDirExists(t, filepath.Dir(first.Executable()), "the second start removed the first one's directory")
		assert.NoError(t, first.Close(), "closing a directory that a later start removed is not a failure")
	})

	t.Run("accepts a data directory that holds the PATH separator", func(t *testing.T) {
		t.Parallel()
		if runtime.GOOS == "windows" {
			t.Skip("this case names a directory with ':', which Windows forbids in a file name")
		}
		dataDir := filepath.Join(t.TempDir(), "odd:name")
		require.NoError(t, os.MkdirAll(dataDir, 0o700))

		directory, err := PrepareCLIDirectory(dataDir, cliTarget(t, "worker-cli"))
		require.NoError(t, err, "LEAPMUX_CONTROL_CLI still gives the executable")
		t.Cleanup(func() { assert.NoError(t, directory.Close()) })

		assert.False(t, controlenv.PathEntryAllowed(filepath.Dir(directory.Executable())),
			"the directory cannot be a PATH entry, so Apply leaves PATH alone")
	})
}

func TestPrepareCLIDirectoryRefusesAnUnusableExecutable(t *testing.T) {
	t.Parallel()
	nonExecutable := filepath.Join(t.TempDir(), "leapmux")
	require.NoError(t, os.WriteFile(nonExecutable, []byte("#!/bin/sh\n"), 0o600))

	cases := []struct {
		name       string
		executable string
		unixOnly   bool
	}{
		{name: "a relative path", executable: "leapmux"},
		{name: "an empty path", executable: ""},
		{name: "a missing file", executable: filepath.Join(t.TempDir(), "missing")},
		{name: "a directory", executable: t.TempDir()},
		{name: "a file without an execute bit", executable: nonExecutable, unixOnly: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.unixOnly && runtime.GOOS == "windows" {
				t.Skip("Windows has no execute permission bit")
			}
			dataDir := t.TempDir()

			directory, err := PrepareCLIDirectory(dataDir, tc.executable)

			require.Error(t, err)
			assert.Nil(t, directory)
			assert.Empty(t, cliDirectories(t, runDirOf(t, dataDir)), "a refusal leaves no directory behind")
		})
	}
}

// A link that no method can create fails the preparation, and the directory
// that the preparation created goes with it: no empty directory stays to put
// first on PATH.
//
// Not parallel: the test replaces linkMethods, which every preparation reads.
// The parallel tests of this package wait until the sequential ones finish, so
// none of them reads the replacement.
func TestPrepareCLIDirectoryRemovesItsDirectoryWhenNoLinkWorks(t *testing.T) {
	original := linkMethods
	t.Cleanup(func() { linkMethods = original })
	linkMethods = []linkMethod{
		{name: "partial", create: func(_, link string) error {
			require.NoError(t, os.WriteFile(link, []byte("half"), 0o600))
			return errors.New("interrupted")
		}},
		{name: "refused", create: func(string, string) error { return errors.New("not permitted") }},
	}
	dataDir := t.TempDir()

	directory, err := PrepareCLIDirectory(dataDir, cliTarget(t, "worker-cli"))

	require.Error(t, err)
	assert.Nil(t, directory)
	assert.ErrorContains(t, err, "partial: interrupted")
	assert.ErrorContains(t, err, "refused: not permitted")
	assert.Empty(t, cliDirectories(t, runDirOf(t, dataDir)), "the failed preparation leaves no directory behind")
}

func TestCLIDirectoryClose(t *testing.T) {
	t.Parallel()
	target := cliTarget(t, "worker-cli")
	directory, err := PrepareCLIDirectory(t.TempDir(), target)
	require.NoError(t, err)
	dir := filepath.Dir(directory.Executable())

	require.NoError(t, directory.Close())
	assert.NoDirExists(t, dir)
	assert.FileExists(t, target, "the link goes, the executable stays")
	assert.NoError(t, directory.Close(), "Close is idempotent")

	var none *CLIDirectory
	assert.NoError(t, none.Close())
	assert.Empty(t, none.Executable(), "a factory without a CLI advertises none")
}

func TestLinkExecutable(t *testing.T) {
	t.Parallel()

	t.Run("falls back in order and frees the name for each method", func(t *testing.T) {
		t.Parallel()
		target := cliTarget(t, "worker-cli")
		link := filepath.Join(t.TempDir(), "leapmux")
		var calls []string
		methods := []linkMethod{
			{name: "partial", create: func(_, link string) error {
				calls = append(calls, "partial")
				require.NoError(t, os.WriteFile(link, []byte("half"), 0o600))
				return errors.New("interrupted")
			}},
			{name: "refused", create: func(_, link string) error {
				calls = append(calls, "refused")
				assert.NoFileExists(t, link, "the partial entry of the earlier method is gone")
				return errors.New("not permitted")
			}},
			{name: "works", create: func(target, link string) error {
				calls = append(calls, "works")
				return copyExecutable(target, link)
			}},
		}

		method, err := linkExecutable(target, link, methods)

		require.NoError(t, err)
		assert.Equal(t, "works", method)
		assert.Equal(t, []string{"partial", "refused", "works"}, calls)
	})

	t.Run("reports the failure of every method", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")
		methods := []linkMethod{
			{name: "first", create: func(string, string) error { return errors.New("first cause") }},
			{name: "second", create: func(string, string) error { return errors.New("second cause") }},
		}

		method, err := linkExecutable(cliTarget(t, "worker-cli"), link, methods)

		require.Error(t, err)
		assert.Empty(t, method)
		assert.ErrorContains(t, err, "first: first cause")
		assert.ErrorContains(t, err, "second: second cause")
		assert.NoFileExists(t, link)
	})

	t.Run("stops when it cannot free the name for the next method", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")
		nextRan := false
		methods := []linkMethod{
			{name: "blocking", create: func(_, link string) error {
				// A directory that is not empty refuses os.Remove.
				require.NoError(t, os.MkdirAll(filepath.Join(link, "inside"), 0o700))
				return errors.New("interrupted")
			}},
			{name: "next", create: func(string, string) error {
				nextRan = true
				return nil
			}},
		}

		method, err := linkExecutable(cliTarget(t, "worker-cli"), link, methods)

		require.Error(t, err)
		assert.Empty(t, method)
		assert.False(t, nextRan, "a method never runs over an entry that the earlier one left")
		assert.ErrorContains(t, err, "blocking: interrupted")
		var pathErr *os.PathError
		assert.ErrorAs(t, err, &pathErr, "the failure to free the name is part of the error")
	})

	t.Run("creates each kind of link on this host", func(t *testing.T) {
		t.Parallel()
		target := cliTarget(t, "worker-cli")
		for _, method := range linkMethods {
			link := filepath.Join(t.TempDir(), "leapmux")
			name, err := linkExecutable(target, link, []linkMethod{method})
			if err != nil && method.name == "symbolic link" && runtime.GOOS == "windows" {
				// Windows refuses a symbolic link without a privilege or
				// Developer Mode, which is why a copy follows it.
				continue
			}
			require.NoError(t, err, method.name)
			assert.Equal(t, method.name, name)
			content, err := os.ReadFile(link)
			require.NoError(t, err)
			assert.Equal(t, "#!/bin/sh\necho 'worker-cli'\n", string(content), "a %s reads as the executable", method.name)
		}
	})
}

func TestCopyExecutable(t *testing.T) {
	t.Parallel()

	t.Run("copies an executable that only this account can run", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")

		require.NoError(t, copyExecutable(cliTarget(t, "copied-cli"), link))

		if runtime.GOOS != "windows" {
			info, err := os.Stat(link)
			require.NoError(t, err)
			assert.Equal(t, os.FileMode(0o700), info.Mode().Perm())
			out, err := exec.Command(link).Output()
			require.NoError(t, err)
			assert.Equal(t, "copied-cli\n", string(out))
		}
	})

	t.Run("refuses to replace an existing file", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")
		require.NoError(t, os.WriteFile(link, []byte("existing"), 0o600))

		require.Error(t, copyExecutable(cliTarget(t, "worker-cli"), link))

		content, err := os.ReadFile(link)
		require.NoError(t, err)
		assert.Equal(t, "existing", string(content), "the refusal leaves the existing file alone")
	})

	t.Run("removes a partial copy", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")

		// Reading a directory fails after the copy is already open.
		require.Error(t, copyExecutable(t.TempDir(), link))

		assert.NoFileExists(t, link)
	})

	t.Run("fails for a missing source without creating the copy", func(t *testing.T) {
		t.Parallel()
		link := filepath.Join(t.TempDir(), "leapmux")

		require.Error(t, copyExecutable(filepath.Join(t.TempDir(), "missing"), link))

		assert.NoFileExists(t, link)
	})
}

func TestCLIExecutableName(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "leapmux.exe", cliExecutableName("windows"), "a Windows command search needs the extension")
	assert.Equal(t, "leapmux", cliExecutableName("linux"))
	assert.Equal(t, "leapmux", cliExecutableName("darwin"))
}
