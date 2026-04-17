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

// SandboxHome redirects home-directory lookups to a fresh temp directory for
// the duration of the test. Sets both HOME and USERPROFILE so os.UserHomeDir
// returns the sandbox on every platform. Returns the sandbox path.
func SandboxHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "leapmux-sandbox-home-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	home = filepath.Clean(home)
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Setenv("USERPROFILE", home)
	}
	return home
}

var counter atomic.Uint64

// UniqueListenURL returns a per-test local-listen URL that won't collide
// with concurrent tests in the same process. Prefix is embedded in the
// name so failures point at the test package.
//
// On Unix returns unix:<t.TempDir>/<prefix>.sock (t.TempDir handles
// cleanup); on Windows returns npipe:<prefix>-<pid>-<nanos>-<counter>.
func UniqueListenURL(t *testing.T, prefix string) string {
	t.Helper()
	n := counter.Add(1)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("npipe:%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), n)
	}
	return "unix:" + filepath.Join(t.TempDir(), prefix+".sock")
}
