package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConsentAndNoninteractiveSetup drives the interactive prompts and
// the noninteractive refusals: y/yes/mixed case accept; empty input,
// EOF, and a cancelled read decline; a noninteractive caller missing
// the startup choice fails without mutating anything.
func TestConsentAndNoninteractiveSetup(t *testing.T) {
	t.Run("y and yes accept, case-insensitively", func(t *testing.T) {
		for _, answer := range []string{"y\n", "yes\n", "Y\n", "YES\n", "Yes\n"} {
			accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader(answer))
			require.NoError(t, err)
			assert.True(t, accepted, "answer %q accepts", strings.TrimSpace(answer))
		}
	})

	t.Run("empty input declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader("\n"))
		require.NoError(t, err)
		assert.False(t, accepted)
	})

	t.Run("end of input declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader(""))
		require.NoError(t, err, "EOF declines rather than failing")
		assert.False(t, accepted)
	})

	t.Run("anything else declines", func(t *testing.T) {
		accepted, err := promptApply(&bytes.Buffer{}, strings.NewReader("no\n"))
		require.NoError(t, err)
		assert.False(t, accepted)
	})

	t.Run("the startup choice defaults to login", func(t *testing.T) {
		var out bytes.Buffer
		choice, err := promptStartupChoice(&out, strings.NewReader("\n"))
		require.NoError(t, err)
		assert.Equal(t, "login", string(choice))
		assert.Contains(t, out.String(), "When you log in")
		assert.Contains(t, out.String(), "When the computer starts")
	})

	t.Run("choice 2 selects boot", func(t *testing.T) {
		choice, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader("2\n"))
		require.NoError(t, err)
		assert.Equal(t, "boot", string(choice))
	})

	t.Run("an unknown choice refuses", func(t *testing.T) {
		_, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader("7\n"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "1 or 2")
	})

	t.Run("end of input on the startup choice names the flags", func(t *testing.T) {
		_, err := promptStartupChoice(&bytes.Buffer{}, strings.NewReader(""))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--startup")
	})

	t.Run("a noninteractive enable with no saved choice fails without mutation", func(t *testing.T) {
		// --yes must not select a startup mode when none is configured:
		// the verb fails with the exact options needed and writes no
		// specification.
		dataDir := t.TempDir()
		err := runModeCommand("worker", []string{"enable", "--yes", "--data-dir", dataDir})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "--startup")
		assert.NoFileExists(t, dataDir+"/.lifecycle/service.yaml",
			"the refusal changed nothing")
	})
}
