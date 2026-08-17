package control

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RequirePassword is shared by the offline `recover` verbs and the online
// `control admin user create`, so a password no longer has to be typed on
// the command line where the shell history and the process table record it.
func TestRequirePasswordReturnsTheFlagValueWithoutPrompting(t *testing.T) {
	got, err := RequirePassword("s3cret", "Password: ")
	require.NoError(t, err)
	assert.Equal(t, "s3cret", got, "a supplied password is used as-is; no prompt")
}

// `go test` runs with a non-terminal stdin, so this exercises the refusal
// path: a piped password is a password in a script or a log, and reading
// one would defeat the point of prompting.
func TestRequirePasswordRefusesWhenStdinIsNotATerminal(t *testing.T) {
	_, err := RequirePassword("", "Password: ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stdin is not a terminal")
}
