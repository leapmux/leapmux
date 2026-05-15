//go:build darwin

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	desktoppb "github.com/leapmux/leapmux/generated/proto/leapmux/desktop/v1"
)

// cliSymlinkDst is the on-PATH location we install the leapmux symlink at.
// /usr/local/bin is the conventional macOS spot writable by an admin user on
// Intel and a no-op on a clean Apple Silicon machine (no Homebrew).
const cliSymlinkDst = "/usr/local/bin/leapmux"

// cliBinaryName is the basename of the CLI both inside the .app and on PATH.
const cliBinaryName = "leapmux"

func bundledCliPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolveBundledCli(exe)
}

// resolveBundledCli returns the path to the bundled leapmux CLI given the
// running sidecar's executable path. Two layouts are supported:
//
//  1. Production .app: the desktop build's `cp leapmux …/Contents/MacOS/`
//     step puts the CLI as a direct sibling of the sidecar inside the
//     .app bundle. <exe-dir>/leapmux is the production path.
//
//  2. Dev (`task dev-desktop`): the sidecar runs from
//     <repo>/desktop/go/bin/leapmux-desktop-service-<triple>, and the
//     CLI is built into <repo>/leapmux by `task build-backend`. Climb
//     four parents above the exe to reach the repo root and look there.
//
// The production check runs first, so a real .app install never accidentally
// resolves to a stray `leapmux` file four levels up its parent chain.
func resolveBundledCli(exe string) string {
	if p := filepath.Join(filepath.Dir(exe), cliBinaryName); isRegularExecutable(p) {
		return p
	}
	if root := parentN(exe, 4); root != "" {
		if p := filepath.Join(root, cliBinaryName); isRegularExecutable(p) {
			return p
		}
	}
	return ""
}

// parentN returns the directory n levels above path, or "" if the chain
// bottoms out at the filesystem root before n hops complete. filepath.Dir
// is idempotent at "/" / drive roots, so the loop has to detect that
// itself rather than trust a fixed N applications.
func parentN(path string, n int) string {
	for i := 0; i < n; i++ {
		next := filepath.Dir(path)
		if next == path {
			return ""
		}
		path = next
	}
	return path
}

func lookupOnSystemPath() string {
	return findOnPath(os.Getenv("PATH"))
}

// findOnPath walks PATH entries left-to-right and returns the first
// `<dir>/leapmux` that's a regular file with any exec bit set. Symlinks are
// returned as-is — classify() canonicalizes them.
func findOnPath(pathEnv string) string {
	if pathEnv == "" {
		return ""
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, cliBinaryName)
		if isRegularExecutable(candidate) {
			return candidate
		}
	}
	return ""
}

// isRegularExecutable returns true when path is a regular file (after
// following symlinks) with at least one exec bit set. Directories named
// "leapmux" and unreadable entries are rejected.
func isRegularExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	if !info.Mode().IsRegular() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

// classify returns the PATH status comparing the bundled CLI against the
// on-PATH copy. The compare is two-stage: realpath equality first (cheap),
// then sha256 content equality (so a user who copied the bundled binary to
// /usr/local/bin still classifies as STATE_OK rather than STATE_MISMATCH).
//
// target_kind is filled in every response. It describes what currently
// occupies the install destination (cliSymlinkDst) — independent of state —
// so the UI can pick the right button treatment (regular vs. two-click
// danger ConfirmButton) and message upfront, rather than learning about a
// real file at the destination only after the user clicks Install.
func classify(bundled, onPath string) *desktoppb.CliPathStatusResponse {
	if bundled == "" {
		return &desktoppb.CliPathStatusResponse{State: desktoppb.CliPathStatusResponse_STATE_UNAVAILABLE}
	}
	targetKind := inspectTargetKind(cliSymlinkDst)
	if onPath == "" {
		return &desktoppb.CliPathStatusResponse{
			State:      desktoppb.CliPathStatusResponse_STATE_MISSING,
			Bundled:    bundled,
			Target:     cliSymlinkDst,
			TargetKind: targetKind,
		}
	}

	bundledReal, errA := filepath.EvalSymlinks(bundled)
	onPathReal, errB := filepath.EvalSymlinks(onPath)
	if errA == nil && errB == nil && bundledReal == onPathReal {
		return &desktoppb.CliPathStatusResponse{
			State:      desktoppb.CliPathStatusResponse_STATE_OK,
			Bundled:    bundled,
			TargetKind: targetKind,
		}
	}

	same, err := contentsEqual(bundled, onPath)
	if err == nil && same {
		return &desktoppb.CliPathStatusResponse{
			State:      desktoppb.CliPathStatusResponse_STATE_OK,
			Bundled:    bundled,
			TargetKind: targetKind,
		}
	}

	return &desktoppb.CliPathStatusResponse{
		State:      desktoppb.CliPathStatusResponse_STATE_MISMATCH,
		Bundled:    bundled,
		Resolved:   onPath,
		TargetKind: targetKind,
	}
}

// inspectTargetKind classifies what currently sits at cliSymlinkDst, so
// the UI can decide whether the Install button needs a two-click "danger"
// arming (only for a real file — never for symlinks, which we always
// replace silently). Permission errors fall through to UNSPECIFIED; the
// UI plays it safe and treats UNSPECIFIED the same as REGULAR_FILE.
func inspectTargetKind(path string) desktoppb.CliPathStatusResponse_TargetKind {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return desktoppb.CliPathStatusResponse_TARGET_KIND_ABSENT
		}
		return desktoppb.CliPathStatusResponse_TARGET_KIND_UNSPECIFIED
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return desktoppb.CliPathStatusResponse_TARGET_KIND_SYMLINK
	}
	if info.Mode().IsRegular() {
		return desktoppb.CliPathStatusResponse_TARGET_KIND_REGULAR_FILE
	}
	return desktoppb.CliPathStatusResponse_TARGET_KIND_UNSPECIFIED
}

// contentsEqual compares two files by SHA-256 digest. A nil error with a true
// return means the files have identical bytes; a non-nil error means one
// could not be read (treat as "not equal" at the caller — classify falls
// through to MISMATCH so the user sees a warning instead of a silent OK).
func contentsEqual(a, b string) (bool, error) {
	ha, err := fileSha256(a)
	if err != nil {
		return false, err
	}
	hb, err := fileSha256(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

func fileSha256(path string) ([32]byte, error) {
	var zero [32]byte
	f, err := os.Open(path)
	if err != nil {
		return zero, err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return zero, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

// installSymlink creates dst → bundled. Existing dangling/wrong symlinks at
// dst are always removed and replaced — the goal is just to make the link
// point at the bundled binary, and an outdated symlink isn't user content.
// A non-symlink regular file at dst is treated as user-installed: when
// `force` is false we refuse with RESULT_ALREADY_EXISTS_REAL_FILE so the UI
// can confirm before clobbering, and when true we remove it first. On
// EACCES/EROFS/EPERM the install returns RESULT_NEEDS_SUDO with the exact
// sudo command (including `-f` so the user's manual retry also overwrites).
func installSymlink(bundled, dst string, force bool) *desktoppb.CliInstallSymlinkResponse {
	if bundled == "" {
		return &desktoppb.CliInstallSymlinkResponse{
			Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
			Message: "bundled leapmux binary not found inside the app",
		}
	}

	parent := filepath.Dir(dst)
	if _, err := os.Stat(parent); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &desktoppb.CliInstallSymlinkResponse{
				Result:  desktoppb.CliInstallSymlinkResponse_RESULT_PARENT_MISSING,
				Path:    parent,
				Command: fmt.Sprintf("sudo mkdir -p %q && sudo ln -sf %q %q", parent, bundled, dst),
			}
		}
		return &desktoppb.CliInstallSymlinkResponse{
			Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
			Message: err.Error(),
		}
	}

	if info, err := os.Lstat(dst); err == nil {
		isSymlink := info.Mode()&os.ModeSymlink != 0
		if !isSymlink && !force {
			return &desktoppb.CliInstallSymlinkResponse{
				Result: desktoppb.CliInstallSymlinkResponse_RESULT_ALREADY_EXISTS_REAL_FILE,
				Path:   dst,
			}
		}
		if err := os.Remove(dst); err != nil {
			return &desktoppb.CliInstallSymlinkResponse{
				Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
				Message: fmt.Sprintf("remove existing entry: %s", err),
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return &desktoppb.CliInstallSymlinkResponse{
			Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
			Message: err.Error(),
		}
	}

	if err := os.Symlink(bundled, dst); err != nil {
		if isPermissionDenied(err) {
			return &desktoppb.CliInstallSymlinkResponse{
				Result:  desktoppb.CliInstallSymlinkResponse_RESULT_NEEDS_SUDO,
				Command: fmt.Sprintf("sudo ln -sf %q %q", bundled, dst),
			}
		}
		return &desktoppb.CliInstallSymlinkResponse{
			Result:  desktoppb.CliInstallSymlinkResponse_RESULT_IO_ERROR,
			Message: err.Error(),
		}
	}

	return &desktoppb.CliInstallSymlinkResponse{
		Result: desktoppb.CliInstallSymlinkResponse_RESULT_OK,
	}
}

// isPermissionDenied catches the three POSIX errnos macOS surfaces when the
// CLI install target sits in a directory the current user can't write to
// (typical for /usr/local/bin on a fresh, unmodified machine). We use
// errors.Is against the standard fs.ErrPermission sentinel plus explicit
// syscall checks to cover ReadOnlyFilesystem and EPERM, which macOS returns
// for SIP-protected directories.
func isPermissionDenied(err error) bool {
	if errors.Is(err, fs.ErrPermission) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		switch errno {
		case syscall.EACCES, syscall.EROFS, syscall.EPERM:
			return true
		}
	}
	return false
}

func cliPathStatusFromSidecar() *desktoppb.CliPathStatusResponse {
	return classify(bundledCliPath(), lookupOnSystemPath())
}

func cliInstallSymlinkFromSidecar(force bool) *desktoppb.CliInstallSymlinkResponse {
	return installSymlink(bundledCliPath(), cliSymlinkDst, force)
}
