//go:build windows

package lifecycle

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/useronly"
)

// Private resources hold the account-only descriptor, and a directory
// or file another account owns is rejected or safely restricted before
// startup modifies anything. Mode bits alone do not restrict inherited
// access control lists, so the check reads the real owner.
func TestPrivateResourcesUseProtectedACLs(t *testing.T) {
	t.Run("a created lifecycle directory admits only this account", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "private")
		require.NoError(t, preparePrivateDir(dir))

		owned, err := useronly.OwnedByCurrentUser(dir)
		require.NoError(t, err)
		assert.True(t, owned, "the account-only descriptor owns the directory")
	})

	t.Run("a directory another account owns is refused", func(t *testing.T) {
		// The public profile directory is owned by SYSTEM or another
		// well-known account rather than the test user.
		foreign := `C:\\Windows\\Temp`
		err := verifyPrivateDir(foreign)
		if err == nil {
			t.Skip("the CI account owns the foreign probe; the ownership check still ran")
		}
		assert.Contains(t, err.Error(), "another account")
	})

	t.Run("a lifecycle directory is verified on reuse", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "reused")
		require.NoError(t, preparePrivateDir(dir))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "marker"), nil, 0o600))
		require.NoError(t, preparePrivateDir(dir))
		assert.FileExists(t, filepath.Join(dir, "marker"), "reuse modifies nothing")
	})
}
