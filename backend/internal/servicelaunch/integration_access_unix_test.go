//go:build darwin || linux

package servicelaunch

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/locallisten"
	"github.com/stretchr/testify/require"
)

func TestNativeControlAccessProbe(t *testing.T) {
	url := os.Getenv("LEAPMUX_NATIVE_CONTROL_PROBE")
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dial, err := locallisten.Dialer(url)
	if err != nil {
		os.Exit(4)
	}
	connection, err := dial(ctx)
	if connection != nil {
		_ = connection.Close()
		os.Exit(3)
	}
	if errors.Is(err, os.ErrPermission) {
		os.Exit(0)
	}
	os.Exit(4)
}

func assertForeignNativeControlDenied(t *testing.T, url string) {
	t.Helper()
	// A shared temporary parent is required: macOS's account TMPDIR cannot
	// be traversed by the foreign account, even when the child is executable.
	directory, err := os.MkdirTemp("/tmp", "leapmux-native-access-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(directory)) })
	require.NoError(t, os.Chmod(directory, 0o755))
	executable := filepath.Join(directory, "control-probe")
	data, err := os.ReadFile(os.Args[0])
	require.NoError(t, err)
	testutil.WriteExecutable(t, executable, string(data))
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/usr/bin/sudo", "-u", "nobody", "--", "/usr/bin/env", "LEAPMUX_NATIVE_CONTROL_PROBE="+url, executable, "-test.run=^TestNativeControlAccessProbe$")
	command.Stdin = os.Stdin
	output, err := command.CombinedOutput()
	require.NoError(t, err, "the executable probe must reach a permission refusal at the actual control socket: %s", output)
}
