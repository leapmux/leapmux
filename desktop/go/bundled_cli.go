package main

import (
	"os"
	"path/filepath"
	"runtime"

	"github.com/leapmux/leapmux/generated/contracts"
)

// cliBinaryName is the basename of the leapmux CLI that the desktop app
// ships, and of the CLI on PATH.
var cliBinaryName = cliBinaryNameFor(runtime.GOOS)

func cliBinaryNameFor(goos string) string {
	if goos == "windows" {
		return "leapmux.exe"
	}
	return "leapmux"
}

// bundledCliPath returns the leapmux CLI that this desktop install ships, or
// "" when it ships none. The Worker that the sidecar runs links it into the
// PATH of each agent and terminal (solo.Config.ControlCLI), and the macOS
// install button links it into /usr/local/bin.
func bundledCliPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	allowDevLayout := os.Getenv(contracts.EnvDevEndpoint) != "" || os.Getenv(contracts.EnvDevFrontend) != ""
	return resolveExecutableBundledCli(exe, allowDevLayout)
}

// resolveExecutableBundledCli resolves symlinks on exe before probing the
// bundled CLI layouts, so a symlinked sidecar resolves relative paths against
// its real installation directory rather than the symlink directory.
func resolveExecutableBundledCli(exe string, allowDevLayout bool) string {
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return resolveBundledCliFor(runtime.GOOS, allowDevLayout, exe)
}

// bundledCliCandidatesFor returns the candidate CLI paths for exe in priority
// order for goos, skipping parent-relative candidates whose traversal would
// bottom out at the filesystem root. The four-level repository-root fallback
// is enabled only when allowDevLayout is true so production bundles never
// climb into unrelated ancestor directories (~, AppData, or /tmp).
func bundledCliCandidatesFor(goos string, allowDevLayout bool, exe string) []string {
	dir := filepath.Dir(exe)
	bin := cliBinaryNameFor(goos)
	var candidates []string
	switch goos {
	case "darwin":
		candidates = append(candidates, filepath.Join(dir, bin))
	case "windows":
		candidates = append(candidates, filepath.Join(dir, "cli", bin))
	case "linux":
		candidates = append(candidates, filepath.Join(dir, bin))
		if prefix := parentN(dir, 2); prefix != "" {
			candidates = append(candidates, filepath.Join(prefix, "bin", bin))
		}
	default:
		candidates = append(candidates, filepath.Join(dir, bin))
	}
	if allowDevLayout {
		if root := parentN(exe, 4); root != "" {
			candidates = append(candidates, filepath.Join(root, bin))
		}
	}
	return candidates
}

// resolveBundledCli returns the leapmux CLI that ships beside the sidecar on
// the current host OS in production layout mode, or "".
func resolveBundledCli(exe string) string {
	return resolveBundledCliFor(runtime.GOOS, false, exe)
}

// resolveBundledCliFor returns the leapmux CLI that ships beside the sidecar
// whose executable is exe for goos, or "". It tries the layouts for that OS in
// order, and the first executable CLI that exists wins:
//
//   - darwin: beside the sidecar (Contents/MacOS/leapmux).
//   - windows: in the cli subdirectory beside the sidecar (cli\leapmux.exe).
//   - linux: beside the sidecar (unbundled runs) and ../../bin (/usr/bin/leapmux
//     from /usr/lib/<product>/). An AppImage ships no CLI, so it resolves to "".
//   - dev mode (allowDevLayout): four levels up at the repository root
//     (<repo>/desktop/go/bin/<sidecar> -> <repo>/leapmux).
func resolveBundledCliFor(goos string, allowDevLayout bool, exe string) string {
	for _, candidate := range bundledCliCandidatesFor(goos, allowDevLayout, exe) {
		if isRegularExecutableFor(goos, candidate) {
			return filepath.Clean(candidate)
		}
	}
	return ""
}

// parentN returns the directory n levels above path, or "" if the chain
// bottoms out at the filesystem root before n hops complete. filepath.Dir
// is idempotent at "/" and at a drive root, so the loop detects that itself
// rather than trust a fixed number of hops.
func parentN(path string, n int) string {
	for range n {
		next := filepath.Dir(path)
		if next == path {
			return ""
		}
		path = next
	}
	return path
}

// isRegularExecutable reports whether path is an executable regular file on the
// host OS.
func isRegularExecutable(path string) bool {
	return isRegularExecutableFor(runtime.GOOS, path)
}

// isRegularExecutableFor reports whether path is a regular file, after
// following symlinks, that goos can run: one with an exec bit set on Unix, or
// any regular file on Windows, which has no exec bits. Directories named
// leapmux and unreadable entries are rejected.
func isRegularExecutableFor(goos, path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return goos == "windows" || info.Mode().Perm()&0o111 != 0
}
