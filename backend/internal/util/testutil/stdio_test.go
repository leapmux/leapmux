package testutil

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCaptureStderrReturnsWhatTheBodyWrote(t *testing.T) {
	out := CaptureStderr(t, func() { _, _ = fmt.Fprint(os.Stderr, "hello from the body") })
	assert.Equal(t, "hello from the body", out)
}

func TestCaptureStdoutAndStderrAreIndependent(t *testing.T) {
	out := CaptureStdout(t, func() {
		_, _ = fmt.Fprint(os.Stdout, "to stdout")
		_, _ = fmt.Fprint(os.Stderr, "to stderr")
	})
	assert.Equal(t, "to stdout", out, "stderr must not leak into the stdout capture")
}

// The capture MUST survive a body that unwinds. t.Fatal and a panic both exit
// through runtime.Goexit/panic rather than returning, so a restore that only
// runs on the happy path would leave the process's stderr pointed at a closed
// pipe -- silently swallowing every later test's output in the same binary.
func TestCaptureStderrRestoresWhenTheBodyPanics(t *testing.T) {
	orig := os.Stderr

	require.Panics(t, func() {
		CaptureStderr(t, func() { panic("boom") })
	})

	assert.Same(t, orig, os.Stderr, "stderr must be restored even when the body panicked")
}
