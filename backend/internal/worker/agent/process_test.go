package agent

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Every provider reports a failed resume through this one helper, so the text a
// user reads does not depend on which provider refused, or on whether the
// refusal came from a local rule or from the agent itself.
func TestResumeFailedError(t *testing.T) {
	t.Parallel()

	cause := errors.New("no such session")
	err := resumeFailedError("sess-gone", cause)

	require.Error(t, err)
	assert.ErrorIs(t, err, cause, "the reason the resume failed must survive the wrapping")
	assert.Contains(t, err.Error(), "sess-gone", "the handle that failed is what the user has to replace")
	assert.Contains(t, err.Error(), "no such session")
	assert.Contains(t, err.Error(), "/clear", "the message must state the one command that recovers the tab")
}

func TestProcessExitCompletion(t *testing.T) {
	t.Parallel()

	process := &processBase{}
	assert.Equal(t, MessageCompletionError, process.processExitCompletion())
	process.noteIntentionalStop()
	assert.Equal(t, MessageCompletionInterrupted, process.processExitCompletion())
	assert.Equal(t, MessageCompletionInterrupted, (&processBase{stopped: true}).processExitCompletion())
}

func TestProcessExitCompletionDoesNotChangeAfterExit(t *testing.T) {
	t.Parallel()

	process := &processBase{}
	process.recordProcessExit(errors.New("provider crashed"))
	process.noteIntentionalStop()

	assert.Equal(t, MessageCompletionError, process.processExitCompletion())
}
