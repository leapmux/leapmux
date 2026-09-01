package agent

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// piResumeArgs is where a Pi resume handle becomes an argv element, so it is
// where the rule is enforced. The value it reads is NOT the one OpenAgent
// validated: agentOutputSink.UpdateSessionID writes whatever Pi reports into
// the `agent_session_id` column, and resolveResumeSessionID hands that column
// back here on every restart.
func TestPiResumeArgs(t *testing.T) {
	t.Parallel()

	// Per host, for the reason the ZCode fixtures state: filepath.IsAbs wants a
	// VOLUME on Windows, so a POSIX literal here makes SanitizePath refuse the
	// tilde case and piResumeArgs fail the start. The shared corpus carries
	// the same pair (testdata/pi_resume_handle_conformance.json, homeDir).
	home := "/home/pi"
	if runtime.GOOS == "windows" {
		home = `C:\Users\pi`
	}

	t.Run("passes a session file path", func(t *testing.T) {
		// The shape Pi reports, per host. Both run past the 128-byte TOKEN cap,
		// and the Windows one holds the backslash the token class bans.
		var handle string
		if runtime.GOOS == "windows" {
			handle = `C:\Users\pi\AppData\Roaming\pi\agent\sessions\--C-src-app--\` + strings.Repeat("a", 80) + ".jsonl"
		} else {
			handle = "/home/pi/.pi/agent/sessions/--home-pi-src-app--/" + strings.Repeat("a", 80) + ".jsonl"
		}
		require.Greater(t, len(handle), 128, "the case is only meaningful past the token cap")
		args, err := piResumeArgs(handle, home)
		require.NoError(t, err)
		assert.Equal(t, []string{"--session", handle}, args)
	})

	// The half the worker refused before. `--session` resolves an ID against
	// the sessions of this working directory, so the identifier Pi reports in
	// its own interface resumes as well as the file path does.
	t.Run("passes a bare session ID", func(t *testing.T) {
		const id = "018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"
		args, err := piResumeArgs(id, home)
		require.NoError(t, err)
		assert.Equal(t, []string{"--session", id}, args)
	})

	// Pi expands `~` itself, so either spelling resumes. The worker sends the
	// EXPANDED one, because that is the string its own rule approved: the path
	// rule normalizes as it checks, and sending the typed string instead is
	// what let a stray character reach argv unseen.
	t.Run("expands a tilde path against the home directory", func(t *testing.T) {
		const handle = "~/.pi/agent/sessions/--p--/2026-08-28T03-13-15-703Z_018f4a2b.jsonl"
		got, err := piResumeArgs(handle, home)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.Equal(t, "--session", got[0])
		assert.NotContains(t, got[1], "~", "argv carries no tilde for a shell to leave alone")
		assert.True(t, filepath.IsAbs(got[1]), "the expanded handle is absolute on this host")
		assert.Contains(t, got[1], "2026-08-28T03-13-15-703Z_018f4a2b.jsonl")
	})

	// The defect this shape exists to remove: the rule strips a control
	// character before it judges, so the approved string and the stored string
	// differ, and argv used to get the stored one. Pi opens a session file
	// without requiring that it exists, so the user silently got an empty
	// conversation at a filename they never typed.
	t.Run("sends the cleaned path, not the stored one", func(t *testing.T) {
		root := "/tmp/pi/sessions"
		if runtime.GOOS == "windows" {
			root = `C:\pi\sessions`
		}
		stored := root + string(filepath.Separator) + "a\x07b.jsonl"
		got, err := piResumeArgs(stored, home)
		require.NoError(t, err)
		require.Len(t, got, 2)
		assert.NotContains(t, got[1], "\x07", "the control character must not reach the process")
		assert.NotEqual(t, stored, got[1])
	})

	t.Run("passes nothing when there is nothing to resume", func(t *testing.T) {
		args, err := piResumeArgs("", home)
		require.NoError(t, err)
		assert.Nil(t, args)
	})

	// A stored handle that breaks the rule fails the START. Dropping `--session`
	// and launching anyway opens a fresh session that looks like the resumed one,
	// so the user loses the conversation with no report of why. And `--session`
	// takes the next argv element whatever it holds, so an unvalidated handle
	// must never reach it.
	t.Run("refuses a handle the provider rule refuses", func(t *testing.T) {
		for _, handle := range []string{
			"--dangerously-skip-permissions",
			"-x",
			"has\x00nul",
			"has\nnewline",
			" leading-space",
			strings.Repeat("a", 129),
			"relative/path.jsonl",
			"session.jsonl",
			"~/../../etc/shadow.jsonl",
			"/tmp/" + strings.Repeat("a", 1024) + ".jsonl",
		} {
			args, err := piResumeArgs(handle, home)
			require.Errorf(t, err, "%q must not reach argv", handle)
			assert.Nil(t, args)
		}
	})

	t.Run("reports the refused handle and how to recover the tab", func(t *testing.T) {
		_, err := piResumeArgs("relative/path.jsonl", home)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "relative/path.jsonl")
		assert.Contains(t, err.Error(), "/clear")
	})
}
