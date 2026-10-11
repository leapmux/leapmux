//go:build unix

package amp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A socket in a directory that other users can reach may belong to another
// user: after a crash of the worker, another user can put a socket there that
// answers "allow". The helper refuses such a socket before it sends the secret.
func TestPermissionHelperRefusesASocketThatIsNotPrivate(t *testing.T) {
	t.Parallel()
	fake := newFakeBridge(t, `{"decision":"allow"}`)
	require.NoError(t, os.Chmod(filepath.Dir(fake.path), 0o755))

	config, err := json.Marshal(helperConfig{Endpoint: fake.path, Secret: "the-secret"})
	require.NoError(t, err)
	run := runHelperWith(t, config, map[string]string{envToolName: "shell_command"}, strings.NewReader(shellInput))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "not private")
	select {
	case received := <-fake.received:
		t.Fatalf("the helper sent the secret %q to a socket that is not private", received.secret)
	default:
	}
}

// The helper refuses a link in place of the socket's directory too: the link
// could point at a directory of another user.
func TestPermissionHelperRefusesALinkedSocketDirectory(t *testing.T) {
	t.Parallel()
	fake := newFakeBridge(t, `{"decision":"allow"}`)
	link := filepath.Join(shortTempDir(t), "link")
	require.NoError(t, os.Symlink(filepath.Dir(fake.path), link))

	config, err := json.Marshal(helperConfig{Endpoint: filepath.Join(link, bridgeSocketName), Secret: "the-secret"})
	require.NoError(t, err)
	run := runHelperWith(t, config, map[string]string{envToolName: "shell_command"}, strings.NewReader(shellInput))
	assert.Equal(t, helperExitReject, run.exitCode(t))
	assert.Contains(t, run.stderr.String(), "not a directory")
}

// The helper sends the secret to a socket alone. A regular file at the socket's
// path, or no file, fails the check before any connection.
func TestCheckPrivateSocketRefusesAPathThatIsNotASocket(t *testing.T) {
	t.Parallel()
	dir := shortTempDir(t)
	file := filepath.Join(dir, bridgeSocketName)
	require.NoError(t, os.WriteFile(file, nil, 0o600))
	assert.ErrorContains(t, checkPrivateSocket(file), "is not a socket")

	assert.ErrorIs(t, checkPrivateSocket(filepath.Join(dir, "absent.sock")), os.ErrNotExist)
	assert.ErrorIs(t, checkPrivateSocket(filepath.Join(dir, "absent", bridgeSocketName)), os.ErrNotExist)
}

// A directory that its group can reach is not private either.
func TestCheckPrivateSocketRefusesADirectoryThatItsGroupCanReach(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	require.NoError(t, os.Chmod(filepath.Dir(bridge.endpoint()), 0o750))
	assert.ErrorContains(t, checkPrivateSocket(bridge.endpoint()), "other users can reach")
}

// The bridge's socket is readable and writable by the owner alone.
func TestPermissionBridgeSocketIsPrivate(t *testing.T) {
	t.Parallel()
	bridge := newTestBridge(t)
	info, err := os.Lstat(bridge.endpoint())
	require.NoError(t, err)
	assert.NotZero(t, info.Mode()&os.ModeSocket)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	assert.NoError(t, checkPrivateSocket(bridge.endpoint()))
}
