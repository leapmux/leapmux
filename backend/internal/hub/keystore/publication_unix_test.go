//go:build unix

package keystore

import (
	"bytes"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestFailedKeyRotationPreservesThePublishedRing(t *testing.T) {
	if path := os.Getenv("LEAPMUX_KEYRING_WRITE_LIMIT_TEST"); path != "" {
		signal.Ignore(syscall.SIGXFSZ)
		var limit unix.Rlimit
		require.NoError(t, unix.Getrlimit(unix.RLIMIT_FSIZE, &limit))
		limit.Cur = 32
		require.NoError(t, unix.Setrlimit(unix.RLIMIT_FSIZE, &limit))
		_, err := RotateKey(path)
		require.Error(t, err)
		return
	}
	path := filepath.Join(t.TempDir(), "encryption.key")
	original, err := LoadOrGenerate(path)
	require.NoError(t, err)
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	child := exec.Command(os.Args[0], "-test.run=^TestFailedKeyRotationPreservesThePublishedRing$")
	child.Env = append(os.Environ(), "LEAPMUX_KEYRING_WRITE_LIMIT_TEST="+path)
	output, err := child.CombinedOutput()
	require.NoError(t, err, string(output))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.True(t, bytes.Equal(before, after), "failed rotation must preserve the exact prior authority")
	reloaded, err := LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, original.keys, reloaded.keys)
	require.Equal(t, original.Pepper(), reloaded.Pepper())
}

func TestLoadOrGenerateReadsImmutableExistingRingsAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encryption.key")
	want, err := LoadOrGenerate(path)
	require.NoError(t, err)
	link := filepath.Join(t.TempDir(), "ring-link")
	require.NoError(t, os.Symlink(path, link))
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.NoError(t, os.Chmod(path, 0o400))
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	for _, candidate := range []string{path, link} {
		got, loadErr := LoadOrGenerate(candidate)
		require.NoError(t, loadErr)
		require.Equal(t, want.keys, got.keys)
		require.Equal(t, want.Pepper(), got.Pepper())
	}
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	afterEntries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Len(t, afterEntries, len(entries), "immutable loads must create no lock or temporary file")
}

func TestKeyRingSymlinkMutationsUseTheTargetLockAndFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ring")
	_, err := LoadOrGenerate(path)
	require.NoError(t, err)
	aliasDir := filepath.Join(t.TempDir(), "directory-alias")
	require.NoError(t, os.Symlink(dir, aliasDir))
	fileAlias := filepath.Join(t.TempDir(), "file-alias")
	require.NoError(t, os.Symlink(path, fileAlias))
	for _, alias := range []string{fileAlias, filepath.Join(aliasDir, "ring")} {
		require.NoError(t, withKeyRingMutation(path, func(string) error {
			_, mutationErr := RotateKey(alias)
			require.ErrorIs(t, mutationErr, errKeyRingMutationBusy)
			return nil
		}))
	}
	version, err := RotateKey(fileAlias)
	require.NoError(t, err)
	require.Equal(t, uint32(2), version)
	target, err := LoadFromFile(path)
	require.NoError(t, err)
	require.Equal(t, uint32(2), target.ActiveVersion())
	info, err := os.Lstat(fileAlias)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	require.NoFileExists(t, fileAlias+".lock")
}

func TestKeyRingRefusesADanglingFileSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "absent")
	alias := filepath.Join(t.TempDir(), "ring")
	require.NoError(t, os.Symlink(target, alias))
	_, err := LoadOrGenerate(alias)
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoFileExists(t, target)
	info, err := os.Lstat(alias)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
	require.NoFileExists(t, alias+".lock")
}

func TestKeyRingReportsUnwritableCreationInitializationAndMutation(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write directories without owner write permission")
	}
	for _, operation := range []string{"create", "initialize pepper", "rotate", "remove", "regenerate pepper"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "ring")
			if operation != "create" {
				_, err := LoadOrGenerate(path)
				require.NoError(t, err)
				_, err = RotateKey(path)
				require.NoError(t, err)
				if operation == "initialize pepper" {
					data, err := os.ReadFile(path)
					require.NoError(t, err)
					cut := bytes.Index(data, []byte(pepperTag+":"))
					require.Positive(t, cut)
					require.NoError(t, os.WriteFile(path, data[:cut], 0o600))
				}
			}
			before, readErr := os.ReadFile(path)
			if operation != "create" {
				require.NoError(t, readErr)
			}
			require.NoError(t, os.Chmod(dir, 0o500))
			t.Cleanup(func() { require.NoError(t, os.Chmod(dir, 0o700)) })
			var err error
			switch operation {
			case "create", "initialize pepper":
				_, err = LoadOrGenerate(path)
			case "rotate":
				_, err = RotateKey(path)
			case "remove":
				err = RemoveKey(path, 1)
			case "regenerate pepper":
				err = RegeneratePepper(path)
			}
			require.ErrorIs(t, err, os.ErrPermission)
			after, readErr := os.ReadFile(path)
			if operation == "create" {
				require.ErrorIs(t, readErr, os.ErrNotExist)
			} else {
				require.NoError(t, readErr)
				require.Equal(t, before, after)
			}
		})
	}
}
