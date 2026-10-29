package providerkit

import (
	"errors"
	"fmt"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent"
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
