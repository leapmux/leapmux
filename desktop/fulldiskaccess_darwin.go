//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
)

// checkFullDiskAccess returns true if the app has Full Disk Access on macOS.
// It probes well-known TCC-protected files; a permission-denied error means
// FDA is not granted.
func checkFullDiskAccess() bool {
	// Use os/user to get the real home directory (works in sandboxed apps).
	u, err := user.Current()
	if err != nil {
		return true // can't determine; don't block the user
	}
	home := u.HomeDir

	testPaths := []string{
		filepath.Join(home, "Library", "Safari", "CloudTabs.db"),
		filepath.Join(home, "Library", "Safari", "Bookmarks.plist"),
		filepath.Join(home, "Library", "Application Support", "com.apple.TCC", "TCC.db"),
		"/Library/Preferences/com.apple.TimeMachine.plist",
	}

	result := true // indeterminate → don't block the user
	for _, path := range testPaths {
		if _, err := os.Stat(path); err != nil {
			continue // file doesn't exist, try next
		}
		// File exists — try to open it.
		f, err := os.Open(path)
		if err == nil {
			_ = f.Close()
			return true // readable → FDA granted
		}
		if errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.EACCES) {
			result = false // denied, but keep checking
		}
	}

	return result
}

// openFullDiskAccessSettings opens System Settings to the Full Disk Access pane.
func openFullDiskAccessSettings() error {
	// macOS Ventura+ URL scheme
	err := exec.Command("open", "x-apple.systempreferences:com.apple.settings.PrivacySecurity.extension?Privacy_AllFiles").Start()
	if err != nil {
		// Fallback for older macOS versions
		err = exec.Command("open", "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles").Start()
	}
	return err
}
