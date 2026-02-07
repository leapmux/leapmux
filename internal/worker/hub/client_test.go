package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveWorkingDir_HomeDir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	got, err := resolveWorkingDir("~")
	require.NoError(t, err, "resolveWorkingDir(~)")
	assert.Equal(t, home, got)
}

func TestResolveWorkingDir_HomeSubdir(t *testing.T) {
	home, err := os.UserHomeDir()
	require.NoError(t, err, "UserHomeDir")

	// Use a subdirectory that exists under home. On macOS/Linux, home itself
	// always exists, so we create a temp dir under it for a reliable test.
	sub := filepath.Join(home, "Documents")
	if _, statErr := os.Stat(sub); statErr != nil {
		t.Skipf("~/Documents does not exist, skipping")
	}

	got, err := resolveWorkingDir("~/Documents")
	require.NoError(t, err, "resolveWorkingDir(~/Documents)")
	assert.Equal(t, sub, got)
}

func TestResolveWorkingDir_TildeInMiddle(t *testing.T) {
	// /foo/~/bar is NOT a tilde prefix — should be treated literally.
	// This path likely doesn't exist, so we expect an error.
	_, err := resolveWorkingDir("/foo/~/bar")
	assert.Error(t, err, "expected error for /foo/~/bar (path should not exist)")
}

func TestResolveWorkingDir_DoubleTilde(t *testing.T) {
	// ~~ is NOT a tilde prefix — resolves relative to cwd as ./~~
	_, err := resolveWorkingDir("~~")
	assert.Error(t, err, "expected error for ~~ (no such directory)")
}

func TestResolveWorkingDir_DoubleTildeSubpath(t *testing.T) {
	_, err := resolveWorkingDir("~~/foo")
	assert.Error(t, err, "expected error for ~~/foo (no such directory)")
}

func TestResolveWorkingDir_ExistingDir(t *testing.T) {
	// Use a temp directory to avoid symlink issues (/tmp -> /private/tmp on macOS).
	dir := t.TempDir()

	got, err := resolveWorkingDir(dir)
	require.NoError(t, err, "resolveWorkingDir(%s)", dir)
	expected := filepath.Clean(dir)
	assert.Equal(t, expected, got)
}

func TestResolveWorkingDir_NonexistentPath(t *testing.T) {
	_, err := resolveWorkingDir("/nonexistent/path/that/does/not/exist")
	assert.Error(t, err, "expected error for nonexistent path")
}

func TestResolveWorkingDir_FileNotDir(t *testing.T) {
	// Create a temporary file (not a directory).
	f, err := os.CreateTemp("", "resolveWorkingDir-test-*")
	require.NoError(t, err)
	_ = f.Close()
	defer func() { _ = os.Remove(f.Name()) }()

	_, err = resolveWorkingDir(f.Name())
	assert.Error(t, err, "expected error for a file path (not a directory)")
}

func TestResolveWorkingDir_Empty(t *testing.T) {
	// Empty string resolves to cwd.
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveWorkingDir("")
	require.NoError(t, err, "resolveWorkingDir('')")
	assert.Equal(t, cwd, got)
}

func TestResolveWorkingDir_RelativePath(t *testing.T) {
	// "." should resolve to cwd.
	cwd, err := os.Getwd()
	require.NoError(t, err)

	got, err := resolveWorkingDir(".")
	require.NoError(t, err, "resolveWorkingDir('.')")
	assert.Equal(t, cwd, got)
}

func TestConnectWithReconnect_ReconnectsOnFailure(t *testing.T) {
	var attempts atomic.Int32
	targetAttempts := int32(4)

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		if n >= targetAttempts {
			cancel() // Stop after enough attempts.
		}
		return fmt.Errorf("connection lost")
	}

	client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)

	assert.GreaterOrEqual(t, attempts.Load(), targetAttempts, "connect call count")
}

func TestConnectWithReconnect_StopsOnContextCancel(t *testing.T) {
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	mockConnect := func(_ context.Context, _ string) error {
		attempts.Add(1)
		return fmt.Errorf("connection lost")
	}

	// Cancel after a short delay.
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	client.connectWithReconnect(ctx, "token", mockConnect, newFastBackoff(), 5*time.Millisecond)

	assert.GreaterOrEqual(t, attempts.Load(), int32(1), "expected at least 1 attempt")
}

func TestConnectWithReconnect_ResetsBackoffAfterLongConnection(t *testing.T) {
	// Track when each connect call happens.
	var timestamps []time.Time
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 10 * time.Millisecond
	bo.MaxInterval = 500 * time.Millisecond
	bo.Multiplier = 4.0
	bo.RandomizationFactor = 0
	bo.Reset()

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		switch n {
		case 1:
			// First call: fail immediately → backoff=10ms.
			return fmt.Errorf("fail 1")
		case 2:
			// Second call: fail immediately → backoff=40ms.
			return fmt.Errorf("fail 2")
		case 3:
			// Third call: fail immediately → backoff=160ms.
			return fmt.Errorf("fail 3")
		case 4:
			// Fourth call: succeed for longer than threshold → should reset backoff.
			time.Sleep(80 * time.Millisecond)
			return fmt.Errorf("disconnect after long session")
		case 5:
			// Fifth call: fail → backoff should have been reset to 10ms (InitialInterval).
			return fmt.Errorf("fail 5")
		default:
			cancel()
			return fmt.Errorf("done")
		}
	}

	client.connectWithReconnect(ctx, "token", mockConnect, bo, 50*time.Millisecond)

	require.GreaterOrEqual(t, len(timestamps), 6, "expected at least 6 timestamps")

	// Gap between call 3 and 4 should be large (160ms backoff).
	// Gap between call 5 and 6 should be small (10ms, reset to InitialInterval).
	gap34 := timestamps[3].Sub(timestamps[2])
	gap56 := timestamps[5].Sub(timestamps[4])

	assert.Less(t, gap56, gap34, "gap after reset should be shorter than gap before long connection")
}

func TestConnectWithReconnect_BackoffCapsAtMax(t *testing.T) {
	var timestamps []time.Time
	targetAttempts := int32(8)
	var attempts atomic.Int32

	client := &Client{}
	ctx, cancel := context.WithCancel(context.Background())

	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 2 * time.Millisecond
	bo.MaxInterval = 10 * time.Millisecond
	bo.Multiplier = 2.0
	bo.RandomizationFactor = 0
	bo.Reset()

	mockConnect := func(_ context.Context, _ string) error {
		n := attempts.Add(1)
		timestamps = append(timestamps, time.Now())
		if n >= targetAttempts {
			cancel()
		}
		return fmt.Errorf("fail")
	}

	client.connectWithReconnect(ctx, "token", mockConnect, bo, 1*time.Hour)

	// Verify that later gaps don't exceed MaxInterval + tolerance.
	tolerance := 5 * time.Millisecond
	for i := 1; i < len(timestamps); i++ {
		gap := timestamps[i].Sub(timestamps[i-1])
		assert.LessOrEqual(t, gap, bo.MaxInterval+tolerance, "gap[%d]=%v exceeds MaxInterval=%v", i, gap, bo.MaxInterval)
	}
}
