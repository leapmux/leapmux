package amp

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
)

func TestLaunchArgs(t *testing.T) {
	t.Parallel()
	common := []string{
		"--execute", "--stream-json", "--stream-json-input", "--stream-json-thinking",
		"--settings-file", "/state/settings.json",
		"--no-ide", "--no-notifications", "--no-color", "--no-remote-control-terminal",
	}

	newThread := launchArgs("", agentModeHigh, "/state/settings.json")
	assert.Equal(t, append(append([]string{}, common...), "--mode", "high", "--no-archive-after-execute"), newThread,
		"a new thread states its mode, and stays resumable")

	resumed := launchArgs("T-1", agentModeHigh, "/state/settings.json")
	assert.Equal(t, append([]string{"threads", "continue", "T-1"}, common...), resumed,
		"a resumed thread keeps its own mode, and Amp archives only a thread it created")
}

func TestProcessEnv(t *testing.T) {
	t.Parallel()
	c := launchConfig{helperEnv: contracts.EnvAgentHelper + "=/state/helper.json"}
	env := c.processEnv([]string{
		"PATH=/usr/bin",
		"AMP_API_KEY=sk-test",
		"AMP_URL=http://127.0.0.1:9",
		"AMP_THREAD_ID=T-parent",
		"AMP_CURRENT_THREAD_ID=T-parent",
		"AGENT_THREAD_ID=T-parent",
		"AGENT_TOOL_NAME=shell_command",
		contracts.EnvAgentHelper + "=/somebody/else.json",
	})

	values := map[string][]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		values[key] = append(values[key], value)
	}
	assert.Equal(t, []string{"/usr/bin"}, values["PATH"])
	assert.Equal(t, []string{"sk-test"}, values["AMP_API_KEY"], "the login the user set reaches Amp")
	assert.Equal(t, []string{"http://127.0.0.1:9"}, values["AMP_URL"])
	for _, key := range []string{"AMP_THREAD_ID", "AMP_CURRENT_THREAD_ID", "AGENT_THREAD_ID"} {
		assert.NotContainsf(t, values, key, "%s would make the new thread a child of another", key)
	}
	assert.NotContains(t, values, contracts.EnvAgentHelper, "no inherited helper spec reaches the shell")
	assert.NotContains(t, values, envSkipUpdateCheck, "the shell sets the switch after the profile")

	assert.Equal(t, []string{contracts.EnvAgentHelper + "=/state/helper.json", envSkipUpdateCheck + "=1"}, c.setEnv(),
		"the shell sets this agent's helper spec and the switch after the profile, which cannot replace them")
}

func TestExitMessage(t *testing.T) {
	t.Parallel()
	fp := newFakeProc("agent-1", true)
	_, _ = fp.StderrWriterForTest().Write([]byte("Error: Thread not found or you don't have access.\n"))
	message := exitMessage(fp.ampProcess, "T-gone", fp.Stderr())
	assert.Contains(t, message, `could not resume session "T-gone"`)
	assert.Contains(t, message, "Thread not found")
	assert.Contains(t, message, "send /clear", "a resume that fails before its init line says how to start fresh")

	fp.sawInit.Store(true)
	assert.NotContains(t, exitMessage(fp.ampProcess, "T-gone", fp.Stderr()), "/clear", "a thread that opened is not a failed resume")

	fresh := newFakeProc("agent-1", false)
	assert.NotContains(t, exitMessage(fresh.ampProcess, "", fresh.Stderr()), "/clear")
}

func TestAdoptRefusesAProcessAfterStop(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.agent.Stop()
	fp := newFakeProc("agent-1", false)
	err := h.agent.adopt(fp.ampProcess)
	assert.True(t, errors.Is(err, errAgentStopped))
	assert.True(t, fp.ending())
	assert.True(t, fp.stdin.closed, "the refused process is stopped")
}

func TestAgentErrorsAreDistinct(t *testing.T) {
	t.Parallel()
	all := []error{errAgentStopped, errTurnInterrupted, errProcessExited, errTurnEnded, agent.ErrAgentBusy}
	for i, a := range all {
		for j, b := range all {
			if i != j {
				assert.NotErrorIs(t, a, b)
			}
		}
	}
}

// An error that ended a turn continues the thread at once, unless the process
// was a resume that never opened its thread: the same command would only fail
// again, and add a second copy of the same error.
func TestResumesThreadAfterExit(t *testing.T) {
	t.Parallel()
	fresh := newFakeProc("agent-1", false)
	assert.True(t, fresh.resumesThreadAfterExit(true), "a turn that the exit failed")
	assert.False(t, fresh.resumesThreadAfterExit(false), "an exit that failed no turn")
	fresh.resumeAfterExit.Store(true)
	assert.True(t, fresh.resumesThreadAfterExit(false), "an error result that ended a turn")

	opened := newFakeProc("agent-1", true)
	opened.sawInit.Store(true)
	assert.True(t, opened.resumesThreadAfterExit(true), "a resume that opened its thread")

	failedResume := newFakeProc("agent-1", true)
	assert.False(t, failedResume.resumesThreadAfterExit(true), "a resume that failed before its init line")
	failedResume.resumeAfterExit.Store(true)
	assert.False(t, failedResume.resumesThreadAfterExit(false), "a resume that failed before its init line")
}
