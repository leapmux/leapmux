package main

import (
	"os"
	"path/filepath"
	"runtime"
)

// cliBinaryName is the basename of the leapmux CLI that the desktop app
// ships, and of the CLI on PATH.
var cliBinaryName = func() string {
	if runtime.GOOS == "windows" {
		return "leapmux.exe"
	}
	return "leapmux"
}()

// bundledCliPath returns the leapmux CLI that this desktop install ships, or
// "" when it ships none. The Worker that the sidecar runs links it into the
// PATH of each agent and terminal (solo.Config.ControlCLI), and the macOS
// install button links it into /usr/local/bin.
func bundledCliPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return resolveBundledCli(exe)
}

// resolveBundledCli returns the leapmux CLI that ships beside the sidecar
// whose executable is exe, or "". It tries each layout that a build produces,
// in this order, and the first CLI that exists wins:
//
//  1. Beside the sidecar: the macOS .app puts both in Contents/MacOS.
//  2. In the cli subdirectory beside the sidecar: the Windows installer
//     ships cli\leapmux.exe in the install directory and adds that directory
//     to the user's PATH.
//  3. In ../../bin from the sidecar: the Linux .deb installs the sidecar as a
//     resource under /usr/lib/<product>/ and the CLI as /usr/bin/leapmux.
//  4. Four levels up, at the repository root: a development build runs the
//     sidecar from <repo>/desktop/go/bin/, and `task build-backend` writes the
//     CLI to <repo>/leapmux.
//
// The installed layouts come first, so a real install never resolves to a
// stray leapmux file four levels up its parent chain. An AppImage ships no
// CLI, so it resolves to "".
func resolveBundledCli(exe string) string {
	dir := filepath.Dir(exe)
	candidates := []string{
		filepath.Join(dir, cliBinaryName),
		filepath.Join(dir, "cli", cliBinaryName),
		filepath.Join(dir, "..", "..", "bin", cliBinaryName),
	}
	if root := parentN(exe, 4); root != "" {
		candidates = append(candidates, filepath.Join(root, cliBinaryName))
	}
	for _, candidate := range candidates {
		if isRegularExecutable(candidate) {
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

// isRegularExecutable reports whether path is a regular file, after
// following symlinks, that the system can run: one with an exec bit set, or
// any regular file on Windows, which has no exec bits. Directories named
// leapmux and unreadable entries are rejected.
func isRegularExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return runtime.GOOS == "windows" || info.Mode().Perm()&0o111 != 0
}
