package testutil_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/internal/util/testutil"
)

// TestNativeAbsPath_IsAbsoluteOnEveryHost pins the one property every caller
// depends on, against the SAME filepath.IsAbs the guards under test apply.
//
// Without it the platform assumption is only implied by the fixtures. A host
// where it does not hold then reports many unrelated failures about ownership,
// reaping and scope -- which is exactly how the POSIX-literal version surfaced
// on Windows.
func TestNativeAbsPath_IsAbsoluteOnEveryHost(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/r", "/mine-a", "/r/a.go", "/repo/pkg/README.md"} {
		got := testutil.NativeAbsPath(p)
		assert.True(t, filepath.IsAbs(got),
			"NativeAbsPath(%q) = %q must be absolute on %s", p, got, runtime.GOOS)
	}
}

// A caller that stores a fixture path and compares it later needs the path to
// be in cleaned form already, or filepath.Clean rewrites the separators and the
// two spellings stop matching.
func TestNativeAbsPath_IsAlreadyCleaned(t *testing.T) {
	t.Parallel()

	for _, p := range []string{"/r", "/r/a.go", "/repo/pkg/README.md"} {
		got := testutil.NativeAbsPath(p)
		assert.Equal(t, got, filepath.Clean(got),
			"NativeAbsPath(%q) = %q must already be cleaned on %s", p, got, runtime.GOOS)
	}
}

// The volume is what a POSIX literal lacks, and Windows is the only host that
// requires one. This case states why NativeAbsPath exists at all.
func TestNativeAbsPath_CarriesAVolumeOnWindows(t *testing.T) {
	t.Parallel()

	if runtime.GOOS != "windows" {
		assert.Equal(t, "/r/a.go", testutil.NativeAbsPath("/r/a.go"),
			"a POSIX host needs no rewrite, so the literal passes through unchanged")
		return
	}
	assert.NotEmpty(t, filepath.VolumeName(testutil.NativeAbsPath("/r/a.go")),
		"an absolute path on Windows carries a volume")
}

// Distinct literals must stay distinct, or a test that seeds two rows and
// expects two answers gets one.
func TestNativeAbsPath_KeepsDistinctLiteralsDistinct(t *testing.T) {
	t.Parallel()

	assert.NotEqual(t, testutil.NativeAbsPath("/r/a.go"), testutil.NativeAbsPath("/r/b.go"))
}
