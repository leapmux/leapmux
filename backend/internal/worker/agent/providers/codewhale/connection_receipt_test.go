package codewhale

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"
)

func TestReclaimStoreRetiresDeadNativeReceipt(t *testing.T) {
	t.Parallel()
	store := codewhaleStore{dir: filepath.Join(t.TempDir(), "store-test")}
	require.NoError(t, os.MkdirAll(store.runtimeDir(), 0o700))
	run := filepath.Join(store.runtimeHome(), "run")
	require.NoError(t, os.MkdirAll(run, 0o700))
	name := "daemon.sock.owner.json"
	if runtime.GOOS == "windows" {
		name = "daemon.owner.json"
	}
	receipt := filepath.Join(run, name)
	data, err := json.Marshal(map[string]any{
		"version": 1, "pid": 2147483647, "data_dir": store.runtimeDir(),
		"socket_path": filepath.Join(run, "daemon.sock"),
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(receipt, data, 0o600))
	require.NoError(t, reclaimStore(context.Background(), store, quartz.NewReal()))
	_, err = os.Stat(receipt)
	require.ErrorIs(t, err, os.ErrNotExist)
}

func TestReclaimStorePreservesUnverifiedNativeReceipts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"live", "another-store", "relative-store", "missing-store", "invalid-json", "zero-pid", "negative-pid", "large-pid", "unknown-version", "oversized", "symlink", "directory", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			store := codewhaleStore{dir: filepath.Join(t.TempDir(), "store-test")}
			require.NoError(t, os.MkdirAll(store.runtimeDir(), 0o700))
			run := filepath.Join(store.runtimeHome(), "run")
			require.NoError(t, os.MkdirAll(run, 0o700))
			name := "daemon.sock.owner.json"
			if runtime.GOOS == "windows" {
				name = "daemon.owner.json"
			}
			path := filepath.Join(run, name)
			receipt := map[string]any{"version": 1, "pid": 2147483647, "data_dir": store.runtimeDir()}
			switch kind {
			case "live":
				receipt["pid"] = os.Getpid()
			case "another-store":
				receipt["data_dir"] = t.TempDir()
			case "relative-store":
				receipt["data_dir"] = "runtime"
			case "missing-store":
				delete(receipt, "data_dir")
			case "zero-pid":
				receipt["pid"] = 0
			case "negative-pid":
				receipt["pid"] = -1
			case "large-pid":
				receipt["pid"] = uint64(1) << 40
			case "unknown-version":
				receipt["version"] = 2
			}
			data, err := json.Marshal(receipt)
			require.NoError(t, err)
			if kind == "invalid-json" {
				data = []byte("{")
			}
			if kind == "oversized" {
				data = append(data, make([]byte, 16385)...)
			}
			switch kind {
			case "directory":
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink":
				target := filepath.Join(t.TempDir(), "receipt")
				require.NoError(t, os.WriteFile(target, data, 0o600))
				require.NoError(t, os.Symlink(target, path))
			case "parent-symlink":
				require.NoError(t, os.Remove(run))
				target := t.TempDir()
				require.NoError(t, os.Symlink(target, run))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			default:
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			require.Error(t, reclaimStore(context.Background(), store, quartz.NewReal()))
			_, err = os.Lstat(path)
			require.NoError(t, err)
			if kind != "directory" {
				preserved, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, data, preserved)
			}
		})
	}
}
