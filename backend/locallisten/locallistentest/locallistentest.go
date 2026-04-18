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
//
// The prefix is kept short ("lm") so that downstream Unix socket paths built
// from this home (e.g. <home>/.config/leapmux/solo/hub/hub.sock) stay within
// the 104-byte AF_UNIX sun_path limit on macOS.
func SandboxHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("", "lm")
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
// with concurrent tests in the same process.
//
// On Unix it returns unix:<short-temp-dir>/s.sock. We avoid t.TempDir()
// because its path embeds the full test name plus a sequence counter; on
// macOS that routinely exceeds the 104-byte AF_UNIX sun_path limit once
// the caller's prefix and ".sock" are appended, and net.Listen fails with
// "bind: invalid argument". Cleanup is wired through t.Cleanup.
//
// On Windows it returns npipe:<prefix>-<pid>-<nanos>-<counter>; named
// pipe names have no equivalent length constraint, so the caller-supplied
// prefix is preserved to make failures easier to attribute.
func UniqueListenURL(t *testing.T, prefix string) string {
	t.Helper()
	n := counter.Add(1)
	if runtime.GOOS == "windows" {
		return fmt.Sprintf("npipe:%s-%d-%d-%d", prefix, os.Getpid(), time.Now().UnixNano(), n)
	}
	dir, err := os.MkdirTemp("", "lm")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return "unix:" + filepath.Join(dir, "s.sock")
}
