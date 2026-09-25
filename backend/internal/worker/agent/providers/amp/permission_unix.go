//go:build unix

package amp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// restrictSocket makes the bridge's socket readable and writable by the owner
// alone. The directory is private too, so this is a second check.
func restrictSocket(path string) error {
	return os.Chmod(path, 0o600)
}

// checkPrivateSocket refuses a bridge socket that another user could have put
// in place. The helper runs it before it sends the secret, so the secret and
// the call reach the agent alone.
//
// The socket's directory must be a real directory, not a link, that the
// helper's own user owns and that no other user can read or write. Then only
// that user can create a socket in it. The socket must be a socket of that
// user too.
func checkPrivateSocket(path string) error {
	uid := os.Getuid()
	dir := filepath.Dir(path)
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if owner, ok := agentdir.FileOwner(info); !ok || owner != uid {
		return fmt.Errorf("another user owns %s", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("other users can reach %s (mode %o)", dir, info.Mode().Perm())
	}
	info, err = os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("%s is not a socket", path)
	}
	if owner, ok := agentdir.FileOwner(info); !ok || owner != uid {
		return fmt.Errorf("another user owns %s", path)
	}
	return nil
}
