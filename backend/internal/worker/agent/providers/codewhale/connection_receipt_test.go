package codewhale

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/coder/quartz"
	"github.com/stretchr/testify/require"
)

type nativeReceiptFixture struct {
	store  codewhaleStore
	path   string
	fields map[string]any
}

func newNativeReceiptFixture(t *testing.T) nativeReceiptFixture {
	t.Helper()
	store := codewhaleStore{dir: filepath.Join(t.TempDir(), "store-test")}
	require.NoError(t, os.MkdirAll(store.runtimeDir(), 0o700))
	run := filepath.Join(store.runtimeHome(), "run")
	require.NoError(t, os.MkdirAll(run, 0o700))
	name := "daemon.sock.owner.json"
	if runtime.GOOS == "windows" {
		name = "daemon.owner.json"
	}
	return nativeReceiptFixture{store, filepath.Join(run, name), map[string]any{
		"version": 1, "pid": 2147483647, "data_dir": store.runtimeDir(),
		"socket_path": filepath.Join(run, "daemon.sock"),
	}}
}

func TestReclaimStoreRetiresDeadNativeReceipt(t *testing.T) {
	t.Parallel()
	for _, size := range []struct {
		name  string
		bytes int
	}{{"short", 0}, {"maximum", 16384}} {
		t.Run(size.name, func(t *testing.T) {
			t.Parallel()
			fixture := newNativeReceiptFixture(t)
			data, err := json.Marshal(fixture.fields)
			require.NoError(t, err)
			if size.bytes != 0 {
				require.Less(t, len(data), size.bytes)
				data = append(data, bytes.Repeat([]byte(" "), size.bytes-len(data))...)
				require.Len(t, data, size.bytes)
			}
			require.True(t, json.Valid(data))
			require.NoError(t, os.WriteFile(fixture.path, data, 0o600))
			require.NoError(t, reclaimStore(context.Background(), fixture.store, quartz.NewReal()))
			_, err = os.Stat(fixture.path)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestReclaimStorePreservesUnverifiedNativeReceipts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"live", "another-store", "relative-store", "missing-store", "invalid-json", "zero-pid", "negative-pid", "large-pid", "unknown-version", "oversized", "symlink", "directory", "parent-symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			fixture := newNativeReceiptFixture(t)
			store, path, receipt := fixture.store, fixture.path, fixture.fields
			run := filepath.Dir(path)
			expectedError := "the Codewhale owner receipt is invalid"
			switch kind {
			case "live":
				expectedError = "the process ID in the Codewhale owner receipt still exists"
				receipt["pid"] = os.Getpid()
			case "another-store":
				expectedError = "the Codewhale owner receipt identifies another store"
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
				require.Less(t, len(data), 16385)
				data = append(data, bytes.Repeat([]byte(" "), 16385-len(data))...)
				require.Len(t, data, 16385)
				require.True(t, json.Valid(data))
			}
			switch kind {
			case "directory":
				expectedError = "the Codewhale owner receipt is not a regular file"
				require.NoError(t, os.Mkdir(path, 0o700))
			case "symlink":
				expectedError = "the Codewhale owner receipt is not a regular file"
				target := filepath.Join(t.TempDir(), "receipt")
				require.NoError(t, os.WriteFile(target, data, 0o600))
				require.NoError(t, os.Symlink(target, path))
			case "parent-symlink":
				expectedError = "the Codewhale owner directory is not a directory"
				require.NoError(t, os.Remove(run))
				target := t.TempDir()
				require.NoError(t, os.Symlink(target, run))
				require.NoError(t, os.WriteFile(path, data, 0o600))
			default:
				require.NoError(t, os.WriteFile(path, data, 0o600))
			}
			require.EqualError(t, reclaimStore(context.Background(), store, quartz.NewReal()), expectedError)
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
