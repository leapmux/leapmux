package agent

import (
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

	const home = "/home/pi"

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
		assert.Equal(t, []string{"--session", handle}, piResumeArgs("agent-1", handle, home))
	})

	// The half the worker refused before. `--session` resolves an ID against
	// the sessions of this working directory, so the identifier Pi reports in
	// its own interface resumes as well as the file path does.
	t.Run("passes a bare session ID", func(t *testing.T) {
		const id = "018f4a2b-0c1d-7e3f-9a5b-6c7d8e9f0a1b"
		assert.Equal(t, []string{"--session", id}, piResumeArgs("agent-1", id, home))
	})

	t.Run("passes a tilde path, which Pi expands itself", func(t *testing.T) {
		const handle = "~/.pi/agent/sessions/--p--/2026-08-28T03-13-15-703Z_018f4a2b.jsonl"
		assert.Equal(t, []string{"--session", handle}, piResumeArgs("agent-1", handle, home))
	})

	t.Run("passes nothing when there is nothing to resume", func(t *testing.T) {
		assert.Nil(t, piResumeArgs("agent-1", "", home))
	})

	// A stored handle that breaks the rule loses the resume and says so in the
	// log. Losing a resume is recoverable; handing argv an unvalidated value is
	// not, and `--session` takes the next argv element whatever it holds.
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
			assert.Nilf(t, piResumeArgs("agent-1", handle, home), "%q must not reach argv", handle)
		}
	})
}
