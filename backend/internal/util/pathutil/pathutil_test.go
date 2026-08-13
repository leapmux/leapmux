package pathutil

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSamePath_Identical(t *testing.T) {
	assert.True(t, SamePath("/home/user", "/home/user"))
}

func TestSamePath_CleanNormalization(t *testing.T) {
	assert.True(t, SamePath("/home/user/", "/home/user"))
	assert.True(t, SamePath("/home//user", "/home/user"))
	assert.True(t, SamePath("/home/./user", "/home/user"))
}

func TestSamePath_Different(t *testing.T) {
	assert.False(t, SamePath("/home/alice", "/home/bob"))
}

func TestSamePath_CaseSensitivity(t *testing.T) {
	// Case-insensitive on Windows, case-sensitive on POSIX.
	got := SamePath("/Home/User", "/home/user")
	if runtime.GOOS == "windows" {
		assert.True(t, got, "Windows should compare paths case-insensitively")
	} else {
		assert.False(t, got, "POSIX should compare paths case-sensitively")
	}
}

func TestHasPathPrefix(t *testing.T) {
	// Nested and equal paths match.
	assert.True(t, HasPathPrefix("/home/user/plans/a.md", "/home/user/plans"))
	assert.True(t, HasPathPrefix("/home/user/plans", "/home/user/plans"))
	// Sibling with shared prefix does not match.
	assert.False(t, HasPathPrefix("/home/user/plansx", "/home/user/plans"))
	// Outside the prefix.
	assert.False(t, HasPathPrefix("/home/user/other", "/home/user/plans"))

	// Case sensitivity matches platform filesystem semantics.
	got := HasPathPrefix("/Home/User/Plans/a.md", "/home/user/plans")
	if runtime.GOOS == "windows" {
		assert.True(t, got)
	} else {
		assert.False(t, got)
	}
}

func TestNormalizeNative_WindowsForwardSlashes(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific separator normalization")
	}
	assert.Equal(t, `C:\Users\foo`, NormalizeNative("C:/Users/foo"))
	assert.Equal(t, `C:\Users\foo\bar`, NormalizeNative("C:/Users/foo/bar"))
}

func TestNormalizeNative_WindowsMsysDriveLetter(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("MSYS path translation only applies on Windows")
	}
	// Git-Bash / MSYS style → native.
	assert.Equal(t, `C:\Users\foo`, NormalizeNative("/c/Users/foo"))
	assert.Equal(t, `C:\Users\foo`, NormalizeNative("/C/Users/foo"))
	assert.Equal(t, `C:\`, NormalizeNative("/c/"))
	// Bare drive (no trailing slash) still normalizes.
	assert.Equal(t, `C:\`, NormalizeNative("/c"))
}

func TestNormalizeNative_PosixUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX paths on POSIX hosts")
	}
	// On POSIX, /c/foo is a legitimate path — do NOT rewrite it.
	assert.Equal(t, "/c/foo", NormalizeNative("/c/foo"))
	assert.Equal(t, "/home/user", NormalizeNative("/home/user/"))
}

func TestNormalizeNative_Empty(t *testing.T) {
	assert.Equal(t, "", NormalizeNative(""))
}

// TestCanonicalize_UnresolvableFallbackIsCleaned pins the fallback, which is
// what makes Canonicalize safe to use as a map key.
//
// EvalSymlinks cleans on its success path, so returning the raw string on
// failure made the same directory produce two different keys depending on
// whether it happened to resolve. gitIndexLock keys its mutex map on exactly
// this value: two keys there means two mutexes for one repository, and two
// concurrent `git worktree add`s -- the failure the lock exists to prevent.
func TestCanonicalize_UnresolvableFallbackIsCleaned(t *testing.T) {
	t.Parallel()

	// A path that cannot resolve, spelled two ways that Clean unifies.
	base := filepath.Join(os.TempDir(), "leapmux-does-not-exist-"+t.Name())
	messy := filepath.Join(base, "sub", "..", ".", "repo")
	tidy := filepath.Join(base, "repo")

	_, err := filepath.EvalSymlinks(messy)
	require.Error(t, err, "the test needs a path EvalSymlinks cannot resolve")

	assert.Equal(t, Canonicalize(tidy), Canonicalize(messy),
		"two spellings of one unresolvable path must canonicalize to one key")
	assert.Equal(t, filepath.Clean(messy), Canonicalize(messy))
}

// TestCanonicalizeAbsent_ResolvesThroughTheDeepestExistingAncestor covers the
// case Canonicalize cannot: a leaf that no longer exists.
//
// A worktree row stores the path canonicalized while the directory was there.
// Once the user deletes the directory, EvalSymlinks fails on the whole path and
// Canonicalize falls back to Clean -- which on macOS answers /var/... where the
// row holds /private/var/..., so the lookup misses its own row. The worktree
// removal preflight reads that row to keep its lock check alive for a directory
// that is gone, so a miss there reported a locked worktree as removable.
func TestCanonicalizeAbsent_ResolvesThroughTheDeepestExistingAncestor(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	absent := filepath.Join(parent, "gone", "deeper")

	_, err := filepath.EvalSymlinks(absent)
	require.Error(t, err, "the test needs a path EvalSymlinks cannot resolve")

	assert.Equal(t, filepath.Join(Canonicalize(parent), "gone", "deeper"), CanonicalizeAbsent(absent),
		"the existing ancestor supplies the spelling; the absent tail rides along")

	// The point of the helper, stated as the property that matters: the path a
	// caller canonicalized while it existed still matches after it is deleted.
	child := filepath.Join(parent, "child")
	require.NoError(t, os.Mkdir(child, 0o755))
	before := Canonicalize(child)
	require.NoError(t, os.Remove(child))
	assert.Equal(t, before, CanonicalizeAbsent(child))
	if runtime.GOOS == "darwin" {
		assert.NotEqual(t, before, Canonicalize(child),
			"this is the miss the helper exists to fix; if it stops happening the helper is dead code")
	}
}

// For a path that exists the helper must agree with Canonicalize exactly --
// otherwise the two spellings drift apart and a caller has to know which one
// produced a stored value.
func TestCanonicalizeAbsent_MatchesCanonicalizeForAnExistingPath(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	assert.Equal(t, Canonicalize(dir), CanonicalizeAbsent(dir))
	assert.Equal(t, Canonicalize(filepath.Join(dir, "sub", "..", ".")), CanonicalizeAbsent(filepath.Join(dir, "sub", "..", ".")))
}
