// Package locallistentest supplies test helpers for packages that exercise
// the locallisten transport. Kept in its own package so production code
// doesn't pull in testing.T dependencies.
package locallistentest

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// shortTempDir mints a fresh temp dir with the minimal prefix "lm" and
// registers cleanup. The short prefix matters because downstream Unix
// socket paths built under this dir (e.g. <dir>/.../hub.sock) must stay
// within the 104-byte AF_UNIX sun_path limit on macOS; t.TempDir() embeds
// the full test name and routinely exceeds that limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lm")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// SandboxHome redirects home-directory lookups to a fresh temp directory for
// the duration of the test. Sets both HOME and USERPROFILE so os.UserHomeDir
// returns the sandbox on every platform. Returns the sandbox path.
func SandboxHome(t *testing.T) string {
	t.Helper()
	home := filepath.Clean(shortTempDir(t))
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

var counter atomic.Uint64

// UniqueListenURL returns a per-test local-listen URL that won't collide
// with concurrent tests in the same process.
//
// On Unix it returns unix:<short-temp-dir>/s.sock. On Windows it returns
// npipe:<prefix>-<pid>-<nanos>-<counter>; named pipe names have no length
// constraint, so the caller-supplied prefix is preserved to make failures
// easier to attribute.
func UniqueListenURL(t *testing.T, prefix string) string {
	t.Helper()
	n := counter.Add(1)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("npipe:%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), n)
	}
	return "unix:" + filepath.Join(shortTempDir(t), "s.sock")
}
