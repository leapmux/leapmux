//go:build darwin

package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeExec(t *testing.T, dir, name string, contents []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, contents, mode))
	return path
}

func TestFindOnPath_FindsFirstExecutable(t *testing.T) {
	t.Parallel()
	a := t.TempDir()
	b := t.TempDir()
	writeExec(t, b, "leapmux", []byte("real"), 0o755)
	// Only b's leapmux exists; a is empty.
	got := findOnPath(a + string(filepath.ListSeparator) + b)
	assert.Equal(t, filepath.Join(b, "leapmux"), got)
}

func TestFindOnPath_IgnoresNonExecutable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeExec(t, dir, "leapmux", []byte("readable"), 0o644)
	assert.Empty(t, findOnPath(dir))
}

func TestFindOnPath_IgnoresDirectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(dir, "leapmux"), 0o755))
	assert.Empty(t, findOnPath(dir))
}

func TestFindOnPath_EmptyPath(t *testing.T) {
	t.Parallel()
	assert.Empty(t, findOnPath(""))
}

func TestFindOnPath_SkipsEmptyEntries(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeExec(t, dir, "leapmux", []byte("x"), 0o755)
	// Leading/trailing/double colons must not blow up SplitList.
	got := findOnPath(string(filepath.ListSeparator) + dir + string(filepath.ListSeparator))
	assert.Equal(t, filepath.Join(dir, "leapmux"), got)
}

func TestClassify_UnavailableWhenBundledAbsent(t *testing.T) {
	t.Parallel()
	resp := classify("", "/usr/local/bin/leapmux")
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_UNAVAILABLE, resp.State)
}

func TestClassify_MissingWhenPathEmpty(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("b"), 0o755)
	resp := classify(bundled, "")
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_MISSING, resp.State)
	assert.Equal(t, bundled, resp.Bundled)
	assert.Equal(t, cliSymlinkDst, resp.Target)
}

func TestClassify_OkWhenRealpathEqual(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("b"), 0o755)
	// /usr/local/bin/leapmux scenario: symlink to bundled in a PATH dir.
	pathDir := t.TempDir()
	link := filepath.Join(pathDir, "leapmux")
	require.NoError(t, os.Symlink(bundled, link))

	resp := classify(bundled, link)
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_OK, resp.State)
	assert.Equal(t, bundled, resp.Bundled)
}

func TestClassify_OkWhenContentsEqual(t *testing.T) {
	t.Parallel()
	contents := []byte("leapmux-bytes")
	bundled := writeExec(t, t.TempDir(), "leapmux", contents, 0o755)
	// User-copied identical binary at a different location.
	onPath := writeExec(t, t.TempDir(), "leapmux", contents, 0o755)

	resp := classify(bundled, onPath)
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_OK, resp.State)
}

func TestClassify_MismatchWhenContentsDiffer(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("bundled-bytes"), 0o755)
	onPath := writeExec(t, t.TempDir(), "leapmux", []byte("other-bytes"), 0o755)

	resp := classify(bundled, onPath)
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_MISMATCH, resp.State)
	assert.Equal(t, onPath, resp.Resolved)
	assert.Equal(t, bundled, resp.Bundled)
}

func TestInstallSymlink_HappyPath(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("x"), 0o755)
	dst := filepath.Join(t.TempDir(), "leapmux")

	resp := installSymlink(bundled, dst, false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_OK, resp.Result)

	target, err := os.Readlink(dst)
	require.NoError(t, err)
	assert.Equal(t, bundled, target)
}

func TestInstallSymlink_ReplacesDanglingSymlink(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("x"), 0o755)
	dstDir := t.TempDir()
	dst := filepath.Join(dstDir, "leapmux")
	// Pre-existing dangling symlink should be silently replaced even without force.
	require.NoError(t, os.Symlink("/nonexistent/path/leapmux", dst))

	resp := installSymlink(bundled, dst, false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_OK, resp.Result)
	target, err := os.Readlink(dst)
	require.NoError(t, err)
	assert.Equal(t, bundled, target)
}

func TestInstallSymlink_ReplacesOutdatedSymlink(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("new"), 0o755)
	stale := writeExec(t, t.TempDir(), "leapmux-old", []byte("old"), 0o755)
	dst := filepath.Join(t.TempDir(), "leapmux")
	// Symlink pointing at a different valid binary (e.g. an older app's
	// embedded CLI) — must be replaced without requiring force.
	require.NoError(t, os.Symlink(stale, dst))

	resp := installSymlink(bundled, dst, false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_OK, resp.Result)
	target, err := os.Readlink(dst)
	require.NoError(t, err)
	assert.Equal(t, bundled, target)
}

func TestInstallSymlink_RefusesRealFileWithoutForce(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("x"), 0o755)
	dst := writeExec(t, t.TempDir(), "leapmux", []byte("user-installed"), 0o755)

	resp := installSymlink(bundled, dst, false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_ALREADY_EXISTS_REAL_FILE, resp.Result)
	assert.Equal(t, dst, resp.Path)

	// The user-installed binary must remain untouched.
	got, err := os.ReadFile(dst)
	require.NoError(t, err)
	assert.Equal(t, "user-installed", string(got))
}

func TestInstallSymlink_ForceReplacesRealFile(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("new"), 0o755)
	dst := writeExec(t, t.TempDir(), "leapmux", []byte("user-installed"), 0o755)

	resp := installSymlink(bundled, dst, true)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_OK, resp.Result)

	// dst should now be a symlink to the new bundled binary, not the
	// regular file we wrote earlier.
	info, err := os.Lstat(dst)
	require.NoError(t, err)
	assert.NotEqual(t, 0, info.Mode()&os.ModeSymlink, "dst should be a symlink after force-overwrite")
	target, err := os.Readlink(dst)
	require.NoError(t, err)
	assert.Equal(t, bundled, target)
}

func TestInstallSymlink_ParentMissing(t *testing.T) {
	t.Parallel()
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("x"), 0o755)
	// Synthesize a non-existent parent directory.
	dst := filepath.Join(t.TempDir(), "does-not-exist", "leapmux")

	resp := installSymlink(bundled, dst, false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_PARENT_MISSING, resp.Result)
	assert.Equal(t, filepath.Dir(dst), resp.Path)
	assert.Contains(t, resp.Command, "sudo mkdir -p")
	assert.Contains(t, resp.Command, "sudo ln -sf")
}

func TestInstallSymlink_BundledMissing(t *testing.T) {
	t.Parallel()
	resp := installSymlink("", filepath.Join(t.TempDir(), "leapmux"), false)
	assert.Equal(t, desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR, resp.Result)
}

func TestResolveBundledCli_ProductionLayout(t *testing.T) {
	t.Parallel()
	// Simulate Contents/MacOS/ — sidecar and leapmux as direct siblings.
	dir := t.TempDir()
	exe := writeExec(t, dir, "leapmux-desktop-service-aarch64-apple-darwin", []byte("svc"), 0o755)
	cli := writeExec(t, dir, "leapmux", []byte("cli"), 0o755)

	assert.Equal(t, cli, resolveBundledCli(exe))
}

func TestResolveBundledCli_DevLayoutClimbsFourLevels(t *testing.T) {
	t.Parallel()
	// Simulate <repo>/desktop/go/bin/leapmux-desktop-service-...; the CLI is
	// at <repo>/leapmux. The sidecar dir has no sibling leapmux, so the
	// dev fallback should kick in and find the repo-root binary.
	root := t.TempDir()
	binDir := filepath.Join(root, "desktop", "go", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	exe := writeExec(t, binDir, "leapmux-desktop-service-aarch64-apple-darwin", []byte("svc"), 0o755)
	cli := writeExec(t, root, "leapmux", []byte("cli"), 0o755)

	assert.Equal(t, cli, resolveBundledCli(exe))
}

func TestResolveBundledCli_DevLayoutBundledTakesPriority(t *testing.T) {
	t.Parallel()
	// If both the sibling and a 4-up candidate happen to exist (paranoid
	// test for someone with a stray ~/leapmux file installing the app at
	// ~/Apps/X.app), the sibling wins — that's the canonical production
	// path and the dev fallback must not steal it.
	root := t.TempDir()
	binDir := filepath.Join(root, "Contents", "MacOS", "bin")
	require.NoError(t, os.MkdirAll(binDir, 0o755))
	exe := writeExec(t, binDir, "leapmux-desktop-service-aarch64-apple-darwin", []byte("svc"), 0o755)
	sibling := writeExec(t, binDir, "leapmux", []byte("canonical"), 0o755)
	_ = writeExec(t, root, "leapmux", []byte("decoy"), 0o755)

	assert.Equal(t, sibling, resolveBundledCli(exe))
}

func TestResolveBundledCli_NoneFound(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	exe := writeExec(t, dir, "leapmux-desktop-service-aarch64-apple-darwin", []byte("svc"), 0o755)
	assert.Empty(t, resolveBundledCli(exe))
}

func TestResolveBundledCli_ShallowExeDoesNotCrash(t *testing.T) {
	t.Parallel()
	// Synthesize an exe path whose parent chain bottoms out before 4 hops
	// — parentN must return "" without filepath.Dir spinning at the root.
	assert.Empty(t, resolveBundledCli("/leapmux-desktop-service"))
}

func TestInspectTargetKind(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()

	// Absent
	assert.Equal(t,
		desktoppb.CliPathStatusResponse_TARGET_KIND_ABSENT,
		inspectTargetKind(filepath.Join(dir, "nope")),
	)

	// Symlink (even when pointing nowhere — Lstat doesn't follow).
	dangling := filepath.Join(dir, "dangling")
	require.NoError(t, os.Symlink("/nonexistent", dangling))
	assert.Equal(t,
		desktoppb.CliPathStatusResponse_TARGET_KIND_SYMLINK,
		inspectTargetKind(dangling),
	)

	// Regular file
	regular := writeExec(t, dir, "real", []byte("x"), 0o755)
	assert.Equal(t,
		desktoppb.CliPathStatusResponse_TARGET_KIND_REGULAR_FILE,
		inspectTargetKind(regular),
	)
}

func TestClassify_PopulatesTargetKindForActiveStates(t *testing.T) {
	t.Parallel()
	// Smoke test: classify must set target_kind on missing/mismatch/ok so
	// the dialog can decide button treatment without round-tripping again.
	// We can't easily override cliSymlinkDst here, but we at least confirm
	// the field is non-UNSPECIFIED on the missing path (where the production
	// /usr/local/bin/leapmux is inspected).
	bundled := writeExec(t, t.TempDir(), "leapmux", []byte("b"), 0o755)
	resp := classify(bundled, "")
	assert.Equal(t, desktoppb.CliPathStatusResponse_STATE_MISSING, resp.State)
	// The actual TargetKind depends on the host machine's /usr/local/bin/leapmux —
	// could be ABSENT, SYMLINK, or REGULAR_FILE — but never UNSPECIFIED
	// unless lstat actually fails with a non-NotExist error.
	assert.NotEqual(t, desktoppb.CliPathStatusResponse_TARGET_KIND_UNSPECIFIED, resp.TargetKind,
		"classify should populate target_kind for MISSING; got UNSPECIFIED")
}

func TestIsPermissionDenied(t *testing.T) {
	t.Parallel()
	assert.True(t, isPermissionDenied(syscall.EACCES))
	assert.True(t, isPermissionDenied(syscall.EROFS))
	assert.True(t, isPermissionDenied(syscall.EPERM))
	assert.True(t, isPermissionDenied(fs.ErrPermission))
	assert.False(t, isPermissionDenied(errors.New("random")))
}
