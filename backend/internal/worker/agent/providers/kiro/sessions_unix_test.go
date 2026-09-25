//go:build unix

package kiro

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The probe's workspace and the group name that Kiro's engine gave it. A path
// of this form exists on unix alone, because Windows lowers the case and adds
// a drive.
func TestKiroWorkspaceKeyMatchesTheEngine(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "54ee328e98494a56", kiroWorkspaceKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kiro/work"))
	assert.Equal(t, "54ee328e98494a56", kiroWorkspaceKey("/Users/trustin/Workspaces/leapmux/.tmp/probe/kiro/work/"))
}

func TestKiroNormalizePathReplacesABackslashAsTheEngineDoes(t *testing.T) {
	t.Parallel()
	// The engine replaces a backslash on every platform, so a unix directory
	// whose name holds one takes the group of the slashed path.
	assert.Equal(t, "/w/a/b", kiroNormalizePath(`/w/a\b`))
}

// A symlink needs a privilege on Windows that a test runner lacks, so this test
// runs on unix alone.
func TestKiroSkipsASymlinkedSessionDirectory(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dir := filepath.Join(home, "project")
	group := kiroGroup(home, dir)
	writeKiroSession(t, filepath.Join(home, "store"), "sess_linked", kiroSessionFixture("sess_linked", dir, "Linked", time.Now()), kiroMessagesFixture)
	require.NoError(t, os.MkdirAll(group, 0o755))
	require.NoError(t, os.Symlink(filepath.Join(home, "store", "sess_linked"), filepath.Join(group, "sess_linked")))

	assert.Empty(t, listKiroSessions(t, home, dir, 0), "Kiro writes real directories, so a link is not its session")
}
