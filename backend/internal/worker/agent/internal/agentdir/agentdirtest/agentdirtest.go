// Package agentdirtest builds agent directories for the tests of the
// providers. The directories lie under a base of the test, never under the
// user's own $XDG_RUNTIME_DIR, $TMPDIR or /tmp.
package agentdirtest

import (
	"context"
	"os"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent/internal/agentdir"
)

// ShortBase creates a base directory for a test and removes it when the test
// ends. t.TempDir() holds the test's name, and a socket path under it can pass
// the platform's limit of about 104 bytes, so on Unix the base lies in /tmp.
func ShortBase(t testing.TB) string {
	t.Helper()
	parent := ""
	if runtime.GOOS != "windows" {
		parent = "/tmp"
	}
	dir, err := os.MkdirTemp(parent, "ad")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// NewDirs returns the agent directories of specs under bases, or under one
// ShortBase when bases is empty. It returns once the sweep ended.
func NewDirs(t testing.TB, specs []agentdir.Spec, bases ...string) *agentdir.Dirs {
	t.Helper()
	if len(bases) == 0 {
		bases = []string{ShortBase(t)}
	}
	dirs, err := agentdir.Start(context.Background(), agentdir.Config{Specs: specs, Bases: bases})
	require.NoError(t, err)
	<-dirs.Swept()
	return dirs
}

// NewDir returns one agent directory of spec, which the end of the test
// closes.
func NewDir(t testing.TB, spec agentdir.Spec) *agentdir.Dir {
	t.Helper()
	dir, err := NewDirs(t, []agentdir.Spec{spec}).New(context.Background(), spec)
	require.NoError(t, err)
	t.Cleanup(func() { _ = dir.Close() })
	return dir
}
