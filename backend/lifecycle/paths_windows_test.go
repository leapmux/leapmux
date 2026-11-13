//go:build windows

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/leapmux/leapmux/internal/util/atomicfile"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/leapmux/leapmux/util/useronly"
)

func TestControlStartupDoesNotClaimAnotherAccountScratchPath(t *testing.T) {
	parent := t.TempDir()
	t.Setenv("TEMP", parent)
	t.Setenv("TMP", parent)
	t.Setenv("LEAPMUX_RUNTIME_DIR", "")
	unrelated := filepath.Join(parent, "leapmux")
	contents := []byte("an unrelated account resource")
	require.NoError(t, os.WriteFile(unrelated, contents, 0o600))
	runtime, err := StartRuntime(RuntimeConfig{Mode: RoleHub, DataDir: t.TempDir(), Execution: ExecutionForeground})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, runtime.Finish(nil)) })
	directory, err := runtimeDir()
	require.NoError(t, err)
	assert.Equal(t, accountRuntimePath(parent, CurrentAccountID()), directory)
	after, err := os.ReadFile(unrelated)
	require.NoError(t, err)
	assert.Equal(t, contents, after)
}

func TestPrivateResourcesRejectBroadAccessEntries(t *testing.T) {
	for _, isDirectory := range []bool{true, false} {
		path := filepath.Join(t.TempDir(), "resource")
		if isDirectory {
			require.NoError(t, atomicfile.MkdirAllPrivate(path))
		} else {
			require.NoError(t, os.WriteFile(path, []byte("control-token"), 0o600))
		}
		sid, err := useronly.CurrentUserSID()
		require.NoError(t, err)
		sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid + ")(A;;FR;;;WD)")
		require.NoError(t, err)
		acl, _, err := sd.DACL()
		require.NoError(t, err)
		require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
		if isDirectory {
			require.Error(t, useronly.VerifyDirectory(path))
		} else {
			require.Error(t, verifyPrivateFile(path))
		}
	}
}

// Private resources hold the account-only descriptor, and a directory
// or file another account owns is rejected or safely restricted before
// startup modifies anything. Mode bits alone do not restrict inherited
// access control lists, so the check reads the real owner.
func TestPrivateResourcesUseProtectedACLs(t *testing.T) {
	t.Run("a created lifecycle directory admits only this account", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "private")
		require.NoError(t, atomicfile.MkdirAllPrivate(dir))

		owned, err := useronly.OwnedByCurrentUser(dir)
		require.NoError(t, err)
		assert.True(t, owned, "the account-only descriptor owns the directory")
	})

	t.Run("a directory another account owns is refused", func(t *testing.T) {
		foreign, err := windows.GetWindowsDirectory()
		require.NoError(t, err)
		owned, err := useronly.OwnedByCurrentUser(foreign)
		require.NoError(t, err)
		require.False(t, owned, "the Windows directory must provide a foreign-owner fixture")
		err = useronly.VerifyDirectory(foreign)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "another account")
	})

	t.Run("a lifecycle directory is verified on reuse", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "reused")
		require.NoError(t, atomicfile.MkdirAllPrivate(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600))
		require.NoError(t, atomicfile.MkdirAllPrivate(dir))
		assert.FileExists(t, filepath.Join(dir, "marker"), "reuse modifies nothing")
	})
}
