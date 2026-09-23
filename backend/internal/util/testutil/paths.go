package testutil

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// NativeAbsPath renders a POSIX-style path literal as a NATIVE absolute path.
//
// A POSIX literal cannot stand in for one. An absolute path on Windows carries
// a volume, so filepath.IsAbs("/repo/a.go") is FALSE there. A guard that asks
// filepath.IsAbs then refuses the fixture, for a reason the test never meant to
// assert.
//
// That failure lands far from the literal and describes the wrong thing. The
// code under test answers "not absolute" correctly, and the assertion that
// fails reports the behavior the test exists to pin.
//
// The paths this builds are fictional and never reach the filesystem. They only
// have to be absolute, and to survive a round trip through storage unchanged.
//
// Use this for a fixture path that production code checks for absoluteness.
// Prefer t.TempDir for a path that a test opens.
func NativeAbsPath(posixPath string) string {
	return filepath.FromSlash(nativePathVolume + posixPath)
}

// nativePathVolume is the volume component of the test binary's working
// directory: "" on POSIX (where the literals are already absolute), "C:"/"D:"/...
// on Windows. Derived rather than hardcoded so NativeAbsPath states a volume
// that exists on whatever host runs the suite.
var nativePathVolume = func() string {
	wd, err := os.Getwd()
	if err != nil {
		// Only reachable if the cwd was unlinked mid-run. Returning "" keeps
		// NativeAbsPath total; on Windows the result is then not absolute, so
		// the guard under test refuses it and the test fails loudly rather
		// than quietly exercising some other path.
		return ""
	}
	return filepath.VolumeName(wd)
}()

// RepoPath returns elem joined under the repository root: the directory that
// holds backend/go.mod.
//
// It walks up from the test's working directory, which is the test's package
// directory, so a test states a repository path the same way at every depth. A
// relative "../../../../testdata" breaks the day its package moves one directory
// deeper, and the failure then reads as a missing fixture.
func RepoPath(t testing.TB, elem ...string) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return filepath.Join(append([]string{dir}, elem...)...)
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "no directory above the test holds backend/go.mod")
		dir = parent
	}
}
