package codewhale

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"github.com/shirou/gopsutil/v4/process"
)

// Codewhale 0.10.1 tries to attach whenever its owner receipt exists.
// A signal exit can leave the receipt after the owner releases its socket.
// Retire only a receipt for this store whose process no longer exists.
// The native socket binder then removes its stale socket itself.
func retireDeadNativeReceipt(ctx context.Context, store codewhaleStore) error {
	root, err := os.OpenRoot(store.runtimeHome())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	directory, err := root.Lstat("run")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !directory.IsDir() {
		return fmt.Errorf("the Codewhale owner directory is not a directory")
	}
	name := filepath.Join("run", "daemon.sock.owner.json")
	if runtime.GOOS == "windows" {
		name = filepath.Join("run", "daemon.owner.json")
	}
	info, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("the Codewhale owner receipt is not a regular file")
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	held, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(info, held) {
		return fmt.Errorf("the Codewhale owner receipt changed")
	}
	data, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return err
	}
	var receipt struct {
		Version int    `json:"version"`
		PID     int32  `json:"pid"`
		DataDir string `json:"data_dir"`
	}
	if len(data) > 16384 || json.Unmarshal(data, &receipt) != nil || receipt.Version != 1 || receipt.PID <= 0 || !filepath.IsAbs(receipt.DataDir) {
		return fmt.Errorf("the Codewhale owner receipt is invalid")
	}
	actual, err := filepath.EvalSymlinks(receipt.DataDir)
	if err != nil {
		return fmt.Errorf("read the Codewhale owner store: %w", err)
	}
	expected, err := filepath.EvalSymlinks(store.runtimeDir())
	if err != nil {
		return err
	}
	actualInfo, err := os.Stat(actual)
	if err != nil {
		return err
	}
	expectedInfo, err := os.Stat(expected)
	if err != nil {
		return err
	}
	if !os.SameFile(actualInfo, expectedInfo) {
		return fmt.Errorf("the Codewhale owner receipt identifies another store")
	}
	alive, err := process.PidExistsWithContext(ctx, receipt.PID)
	if err != nil {
		return fmt.Errorf("inspect the Codewhale owner process: %w", err)
	}
	if alive {
		return fmt.Errorf("the process ID in the Codewhale owner receipt still exists")
	}
	current, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	latest, err := io.ReadAll(io.LimitReader(file, 16385))
	if err != nil {
		return err
	}
	if !os.SameFile(held, current) || !bytes.Equal(data, latest) {
		return fmt.Errorf("the Codewhale owner receipt changed")
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Remove(name)
}
