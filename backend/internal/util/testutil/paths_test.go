package testutil_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

// RepoPath finds the repository root from a package directory, whatever its
// depth, and joins the elements under it.
func TestRepoPath_FindsTheRepositoryRoot(t *testing.T) {
	t.Parallel()

	gomod := testutil.RepoPath(t, "backend", "go.mod")
	info, err := os.Stat(gomod)
	require.NoError(t, err)
	assert.False(t, info.IsDir())

	root := testutil.RepoPath(t)
	assert.Equal(t, filepath.Join(root, "contracts"), testutil.RepoPath(t, "contracts"))
	wd, err := os.Getwd()
	require.NoError(t, err)
	rel, err := filepath.Rel(root, wd)
	require.NoError(t, err)
	assert.Equal(t, filepath.Join("backend", "internal", "util", "testutil"), rel,
		"the root is the directory above backend, not the module or the package")
}

// failRecorder is a testing.TB that records a failure and ends the goroutine
// that failed, as testing.T does, but does not fail the test that owns it.
type failRecorder struct {
	testing.TB
	failed bool
}

func (*failRecorder) Helper()                 {}
func (r *failRecorder) Errorf(string, ...any) { r.failed = true }
func (r *failRecorder) FailNow()              { r.failed = true; runtime.Goexit() }

// Outside the repository, RepoPath fails the test. It does not return a path
// under a directory that is not the repository root.
func TestRepoPath_FailsOutsideTheRepository(t *testing.T) {
	wd, err := os.Getwd()
	require.NoError(t, err)
	// The root of the volume holds no backend/go.mod. t.TempDir is not a safe
	// start, because TMPDIR can point inside the repository.
	t.Chdir(filepath.VolumeName(wd) + string(filepath.Separator))

	rec := &failRecorder{TB: t}
	var got string
	done := make(chan struct{})
	go func() {
		defer close(done)
		got = testutil.RepoPath(rec, "contracts")
	}()
	<-done
	assert.True(t, rec.failed, "the walk must stop at the root and fail")
	assert.Empty(t, got, "a failed walk returns no path")
}
