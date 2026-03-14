//go:build linux || windows

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

var errAlreadyRunning = errors.New("another instance is already running")

func socketPath() string {
	if runtime.GOOS == "linux" {
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			return filepath.Join(dir, "leapmux-desktop.sock")
		}
		return filepath.Join(os.TempDir(), fmt.Sprintf("leapmux-desktop-%d.sock", os.Getuid()))
	}

	// Windows: use LocalAppData for a short, user-specific path.
	if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
		return filepath.Join(dir, "leapmux", "instance.sock")
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "leapmux", "instance.sock")
}

// acquireSingleInstance ensures only one instance of the desktop app runs at a
// time. If another instance is already running, it signals that instance to
// bring its window to the front and returns errAlreadyRunning. Otherwise it
// starts a background listener that invokes onActivate when a subsequent
// instance tries to start, and returns a release function to clean up.
func acquireSingleInstance(onActivate func()) (release func(), err error) {
	path := socketPath()

	// Try connecting to an existing instance.
	conn, err := net.Dial("unix", path)
	if err == nil {
		_, _ = conn.Write([]byte("activate"))
		conn.Close()
		return nil, errAlreadyRunning
	}

	// No existing instance — clean up any stale socket and start listening.
	_ = os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return func() {}, nil
	}

	ln, err := net.Listen("unix", path)
	if err != nil {
		// Cannot listen (e.g. permission error) — proceed without
		// single-instance enforcement rather than blocking the user.
		return func() {}, nil
	}

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return // listener closed
			}
			buf := make([]byte, 32)
			n, _ := c.Read(buf)
			c.Close()
			if string(buf[:n]) == "activate" && onActivate != nil {
				onActivate()
			}
		}
	}()

	return func() {
		ln.Close()
		_ = os.Remove(path)
	}, nil
}
