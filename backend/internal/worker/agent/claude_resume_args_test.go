package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// claudeResumeArgs is the only place in the repository where a session ID
// becomes an argv element, so it is where the token rule is enforced. The
// value it reads is NOT the one OpenAgent validated: agentOutputSink.
// UpdateSessionID writes whatever the agent process reports into the
// `agent_session_id` column, and resolveResumeSessionID hands that column back
// here on every restart.
func TestClaudeResumeArgs(t *testing.T) {
	t.Parallel()

	t.Run("passes an ordinary session ID", func(t *testing.T) {
		args, err := claudeResumeArgs("3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f")
		require.NoError(t, err)
		assert.Equal(t, []string{"--resume", "3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f"}, args)
	})

	t.Run("passes nothing when there is nothing to resume", func(t *testing.T) {
		args, err := claudeResumeArgs("")
		require.NoError(t, err)
		assert.Nil(t, args)
	})

	// The case the guard exists for. `--resume` takes an optional value, so a
	// hyphen-prefixed token is not read as that value: it parses as a flag of
	// its own. Each of these is ONE argv element, so quoting does not help --
	// the CLI, not the shell, is what reads it as syntax.
	t.Run("refuses a token that argv reads as a flag", func(t *testing.T) {
		for _, id := range []string{
			"-x",
			"--dangerously-skip-permissions",
			"--mcp-config",
			"-",
			"--",
		} {
			args, err := claudeResumeArgs(id)
			require.Errorf(t, err, "%q must not reach argv: it parses as a flag, not as the value of --resume", id)
			assert.Nil(t, args)
		}
	})

	// Everything else the token rule refuses is refused here too, because the
	// guard calls that one rule rather than testing the hyphen alone. A second,
	// narrower copy here would be a second source of truth.
	t.Run("refuses every other token the shared rule refuses", func(t *testing.T) {
		for _, id := range []string{
			"has\x00nul",
			"has\nnewline",
			"has\u202eoverride",
			"has\ufeffbom",
			" leading-space",
			"trailing-space ",
			"has\xffinvalid-utf8",
			strings.Repeat("a", 129),
		} {
			args, err := claudeResumeArgs(id)
			require.Errorf(t, err, "%q must not reach argv", id)
			assert.Nil(t, args)
		}
	})

	// A refused token fails the START. Dropping `--resume` and launching anyway
	// opens a fresh session that looks like the resumed one, so the user loses
	// the conversation with no report of why. The message states the handle and
	// the command that replaces it.
	t.Run("reports the refused handle and how to recover the tab", func(t *testing.T) {
		_, err := claudeResumeArgs("--dangerously-skip-permissions")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--dangerously-skip-permissions")
		assert.Contains(t, err.Error(), "/clear")
	})

	// The guard must not become stricter than the rule it delegates to. A
	// hyphen INSIDE a UUID is the common case, and refusing it would stop every
	// resume.
	t.Run("keeps resuming the identifiers real providers issue", func(t *testing.T) {
		for _, id := range []string{
			"3f9a1c2e-77b4-4d81-9e0f-5a6b7c8d9e0f",
			"01JAV8Q3ZP9K2M4N6R8T0W2Y4B",
			"sess_abc.def~123/xyz+QQ==",
			"a--b",
			"abc-123-",
		} {
			got, err := claudeResumeArgs(id)
			require.NoErrorf(t, err, "%q is a legitimate identifier and must still resume", id)
			assert.Equal(t, []string{"--resume", id}, got)
		}
	})
}
