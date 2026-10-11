//go:build unix

package cline

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
)

// A profile can point CLINE_PROVIDER_SETTINGS_PATH at a FIFO. An open of a FIFO
// waits for a writer with no deadline, so the read refuses any file that is not
// a regular one before it opens it.
func TestReadProviderSelectionRefusesAFileThatIsNotRegular(t *testing.T) {
	t.Parallel()
	fifo := filepath.Join(t.TempDir(), "providers.json")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	done := make(chan error, 1)
	go func() {
		_, err := readProviderSelection(fifo)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	case <-time.After(30 * time.Second):
		// A writer ends the reader's wait, so the goroutine does not outlive the
		// test.
		if writer, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = writer.Close()
		}
		t.Fatal("the read waits on the FIFO with no deadline")
	}
}

// The read follows a link, as Cline does: a link to a settings file is read,
// and a link to a FIFO is refused before the open, as the FIFO itself is.
func TestReadProviderSelectionFollowsALink(t *testing.T) {
	t.Parallel()
	target := writeProviders(t, `{"version":1,"lastUsedProvider":"deepseek","providers":{}}`)
	link := filepath.Join(t.TempDir(), "providers.json")
	require.NoError(t, os.Symlink(target, link))
	selection, err := readProviderSelection(link)
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: "deepseek"}, selection)

	fifo := filepath.Join(t.TempDir(), "fifo")
	require.NoError(t, syscall.Mkfifo(fifo, 0o600))
	fifoLink := filepath.Join(t.TempDir(), "providers.json")
	require.NoError(t, os.Symlink(fifo, fifoLink))
	done := make(chan error, 1)
	go func() {
		_, err := readProviderSelection(fifoLink)
		done <- err
	}()
	select {
	case err := <-done:
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not a regular file")
	case <-time.After(30 * time.Second):
		if writer, err := os.OpenFile(fifo, os.O_WRONLY, 0); err == nil {
			_ = writer.Close()
		}
		t.Fatal("the read waits on the FIFO behind the link")
	}

	dangling := filepath.Join(t.TempDir(), "providers.json")
	require.NoError(t, os.Symlink(filepath.Join(t.TempDir(), "absent.json"), dangling))
	selection, err = readProviderSelection(dangling)
	require.NoError(t, err, "a link to no file reads as an absent file")
	assert.Equal(t, providerSelection{Provider: defaultClineProvider}, selection)
}

// A profile can export the variables that move Cline's data, and the daemon runs
// inside that profile. So the values come from the user's shell, and the
// worker's own values count for nothing once the shell answers: here the shell
// states no CLINE_DIR, whatever the worker's environment holds.
func TestSettingsEnvironmentReadsTheUsersShell(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv(envClineDataDir, dataDir)
	getenv := agenttest.FixtureEnv(map[string]string{envClineDir: "/worker-only", envClineDataDir: "/worker-only/data"})
	read, home := settingsEnvironment(context.Background(), testutil.TestShell(), false, getenv, "/worker-home")
	assert.Equal(t, isolatedHome, home, "the shell's home wins")
	assert.Equal(t, dataDir, read(envClineDataDir), "the shell's value wins")
	assert.Empty(t, read(envClineDir), "a variable that the shell does not state is empty")
	assert.Equal(t, filepath.Join(dataDir, settingsDirName, providersFileName), providerSettingsPath(read, home))
}
