package providerkit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every provider reports a failed resume through this one helper, so the text a
// user reads does not depend on which provider refused, or on whether the
// refusal came from a local rule or from the agent itself.
func TestResumeFailedError(t *testing.T) {
	t.Parallel()

	cause := errors.New("no such session")
	err := ResumeFailedError("sess-gone", cause)

	require.Error(t, err)
	assert.ErrorIs(t, err, cause, "the reason the resume failed must survive the wrapping")
	assert.Contains(t, err.Error(), "sess-gone", "the handle that failed is what the user has to replace")
	assert.Contains(t, err.Error(), "no such session")
	assert.Contains(t, err.Error(), "/clear", "the message must state the one command that recovers the tab")
}

func TestProcessExitCompletion(t *testing.T) {
	t.Parallel()

	process := &Process{}
	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
	assert.False(t, process.ProcessExited())
	process.NoteIntentionalStop()
	assert.False(t, process.ProcessExited(), "a stop request does not record an exit")
	assert.Equal(t, agent.MessageCompletionInterrupted, process.ProcessExitCompletion())
	assert.Equal(t, agent.MessageCompletionInterrupted, (&Process{stopped: true}).ProcessExitCompletion())
}

func TestProcessExitCompletionDoesNotChangeAfterExit(t *testing.T) {
	t.Parallel()

	process := &Process{}
	process.recordProcessExit(errors.New("provider crashed"))
	assert.True(t, process.ProcessExited())
	process.NoteIntentionalStop()

	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
}

func TestProcessExitCompletionKeepsItsFirstClassificationWhenDiagnosticsChange(t *testing.T) {
	t.Parallel()
	process := &Process{}
	cause := errors.New("provider failed")
	process.recordProcessExit(cause)
	process.Mu.Lock()
	process.stopped = true
	process.Mu.Unlock()
	enriched := fmt.Errorf("startup diagnostic: %w", cause)
	process.recordProcessExit(enriched)
	assert.Equal(t, agent.MessageCompletionError, process.ProcessExitCompletion())
	assert.Same(t, enriched, process.waitErr, "the final error must retain the diagnostic details")
}

func TestProcessExitErrorReadsWhileStartupDiagnosticsChange(t *testing.T) {
	t.Parallel()
	process := &Process{}
	process.recordProcessExit(errors.New("native failure"))
	start := make(chan struct{})
	read := make(chan bool, 1)
	go func() {
		<-start
		valid := true
		for range 1000 {
			valid = valid && process.ProcessExitError().Error() == "agent process exited unexpectedly"
		}
		read <- valid
	}()
	close(start)
	diagnostic := errors.New("startup diagnostic")
	for range 1000 {
		process.recordProcessExit(diagnostic)
	}
	assert.True(t, <-read)
}

func TestProfileCommandPreambleAfterAnUnterminatedBanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell profile")
	}
	shell := filepath.Join(t.TempDir(), "profile-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf 'profile message'\nexec /bin/sh \"$@\"\n")
	const payload = "provider first line"
	prepared, wrapErr := launch.Wrap(t.Context(), launch.WrapSpec{
		Shell: shell, Launch: launch.Spec{Program: "/bin/echo"},
		BaseArgs: []string{payload}, EnvGated: &launch.EnvGatedArgs{EnvVars: []string{"LEAPMUX_TEST_HIDE"}, MetaKey: "check"},
		SetEnv: []string{"LEAPMUX_TEST_HIDE=1"},
	})
	require.NoError(t, wrapErr)
	t.Cleanup(func() { require.NoError(t, prepared.Close()) })
	cmd, delimiter, prefix := prepared.Cmd, prepared.Delimiter, prepared.MetaPrefix

	output, err := cmd.Output()
	require.NoError(t, err)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	process := NewProcessFrom(ProcessConfig{PreambleDelimiter: delimiter, PreambleMetaPrefix: prefix})
	process.SkipPreambleForTest(scanner)
	assert.Equal(t, "profile message", process.PreambleOutput())
	assert.Equal(t, "false", process.PreambleMetaValue("check"))
	require.True(t, scanner.Scan(), "%q", output)
	assert.Equal(t, payload, scanner.Text())
}

func TestFinalizeStartupCauseKeepsTheProtocolErrorUnlessSelectionFailed(t *testing.T) {
	t.Parallel()
	process := &Process{}
	timeout := errors.New("test request timed out")
	process.recordProcessExit(errors.New("cleanup terminated shell"))
	assert.ErrorIs(t, process.FinalizeStartupCause(timeout), timeout)
	process.recordProcessExit(fmt.Errorf("the shell did not find first or second: %w", launch.ErrNoCandidate))
	assert.ErrorIs(t, process.FinalizeStartupCause(errors.New("broken pipe")), launch.ErrNoCandidate)
}
