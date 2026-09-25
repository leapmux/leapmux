package testutil

import (
	"fmt"
	"os"
	"testing"
)

// RunWithEmptyHome runs the tests of a package whose tests start login shells,
// with HOME at an empty directory, and returns the exit code for os.Exit.
//
// A terminal starts the user's shell as a login shell (`-i -l`), and a login
// shell reads the login files under HOME. On a developer's machine those files
// are whatever the developer's tools installed. For example, Kiro CLI adds a
// block to each of them that moves the shell into its own terminal wrapper, and
// then a test sees a process tree that it never started. An empty HOME makes the
// shell read no login file but the system's own. ENV and BASH_ENV go too,
// because an interactive `sh` and a non-interactive bash read the file that
// each one names.
//
// Call it from TestMain, before any test runs: it changes the environment of
// the whole test binary.
func RunWithEmptyHome(m *testing.M) int {
	restore, err := useEmptyHome()
	if err != nil {
		fmt.Fprintf(os.Stderr, "isolate HOME for the tests: %v\n", err)
		return 1
	}
	defer restore()
	return m.Run()
}

// useEmptyHome points HOME at a new empty directory and unsets ENV and
// BASH_ENV. restore undoes all three and removes the directory.
func useEmptyHome() (restore func(), err error) {
	home, err := os.MkdirTemp("", "leapmux-test-home-")
	if err != nil {
		return nil, err
	}
	saved := map[string]*string{}
	for _, key := range []string{"HOME", "ENV", "BASH_ENV"} {
		if value, ok := os.LookupEnv(key); ok {
			saved[key] = &value
		} else {
			saved[key] = nil
		}
	}
	restore = func() {
		for key, value := range saved {
			if value == nil {
				_ = os.Unsetenv(key)
			} else {
				_ = os.Setenv(key, *value)
			}
		}
		_ = os.RemoveAll(home)
	}
	if err := os.Setenv("HOME", home); err != nil {
		restore()
		return nil, err
	}
	for _, key := range []string{"ENV", "BASH_ENV"} {
		if err := os.Unsetenv(key); err != nil {
			restore()
			return nil, err
		}
	}
	return restore, nil
}
