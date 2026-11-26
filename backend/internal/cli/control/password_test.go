package control

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/cli"
)

// RequirePassword is shared by the offline `recover` verbs and the online
// `admin user create`, so a password no longer has to be typed on
// the command line where the shell history and the process table record it.
func TestRequirePasswordReturnsTheFlagValueWithoutPrompting(t *testing.T) {
	var errOut bytes.Buffer
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	got, err := RequirePassword(ctx, "s3cret", "Password: ", nil, &errOut)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got, "a supplied password is used as-is; no prompt")
	assert.Empty(t, errOut.String(), "bypassing the prompt must write nothing to errOut")
}

// `go test` runs with a non-terminal stdin, so this exercises the refusal
// path: a piped password is a password in a script or a log, and reading
// one would defeat the point of prompting.
func TestRequirePasswordRefusesWhenStdinIsNotATerminal(t *testing.T) {
	_, err := RequirePassword(t.Context(), "", "Password: ", nil, nil)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code, "the missing flag is a usage error")
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}

// TestRequirePasswordRefusesWhenTheCallerRefusedThePrompt pins the second
// half of the same rule: one variable answers for every prompt this CLI can
// open, so a caller that states "I am a script" is never asked to type a
// secret -- and the refusal states the variable rather than the terminal,
// which is what that caller can change.
func TestRequirePasswordRefusesWhenTheCallerRefusedThePrompt(t *testing.T) {
	t.Setenv(noPromptEnv, "1")
	_, err := RequirePassword(t.Context(), "", "Password: ", nil, nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--password is required")
	assert.Contains(t, err.Error(), noPromptEnv)

	// A supplied password still needs no prompt, so the variable changes
	// nothing for a caller that passed one.
	got, err := RequirePassword(t.Context(), "s3cret", "Password: ", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got)
}

func TestPromptPassword_ReturnsImmediatelyWhenContextAlreadyCanceled(t *testing.T) {
	cause := errors.New("caller canceled before prompt")
	ctx, cancel := context.WithCancelCause(t.Context())
	cancel(cause)

	got, err := PromptPassword(ctx, "Password: ", nil, nil)
	assert.Empty(t, got)
	require.ErrorIs(t, err, cause)
}

func TestPromptPassword_RefusesNonTerminalInjectedFile(t *testing.T) {
	t.Setenv(noPromptEnv, "")
	r, w, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = r.Close()
		_ = w.Close()
	})

	var errOut bytes.Buffer
	got, err := PromptPassword(t.Context(), "Password: ", r, &errOut)
	assert.Empty(t, got)
	require.Error(t, err)
	assert.Equal(t, cli.CodeUsage, cli.FromError(err).Code)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
	assert.Empty(t, errOut.String(), "a refused prompt must write nothing to errOut")
}
