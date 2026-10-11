package testutil

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The test changes the process environment, which a parallel test would see,
// so it does not run in parallel.
func TestUseEmptyHome(t *testing.T) {
	realHome, hadHome := os.LookupEnv("HOME")
	t.Setenv("ENV", "/nonexistent/env-file")
	t.Setenv("BASH_ENV", "/nonexistent/bash-env-file")

	restore, err := useEmptyHome()
	require.NoError(t, err)
	home := os.Getenv("HOME")
	assert.NotEqual(t, realHome, home)
	entries, err := os.ReadDir(home)
	require.NoError(t, err, "HOME is an existing directory")
	assert.Empty(t, entries, "HOME holds no login file")
	_, hasEnv := os.LookupEnv("ENV")
	_, hasBashEnv := os.LookupEnv("BASH_ENV")
	assert.False(t, hasEnv)
	assert.False(t, hasBashEnv)

	restore()
	restoredHome, stillHasHome := os.LookupEnv("HOME")
	assert.Equal(t, hadHome, stillHasHome)
	assert.Equal(t, realHome, restoredHome)
	assert.Equal(t, "/nonexistent/env-file", os.Getenv("ENV"))
	assert.Equal(t, "/nonexistent/bash-env-file", os.Getenv("BASH_ENV"))
	_, statErr := os.Stat(home)
	assert.True(t, os.IsNotExist(statErr), "restore removes the directory")
}

// A variable that the environment did not hold before is absent again after
// restore, not set to an empty value. A login shell reads an empty HOME
// differently from an absent one. Not parallel: it changes the process
// environment.
func TestUseEmptyHomeRestoresAnAbsentVariableAsAbsent(t *testing.T) {
	// t.Setenv registers the cleanup that restores the real values, and the
	// unset then makes each variable absent for the test.
	for _, key := range []string{"HOME", "ENV", "BASH_ENV"} {
		t.Setenv(key, "")
		require.NoError(t, os.Unsetenv(key))
	}

	restore, err := useEmptyHome()
	require.NoError(t, err)
	home := os.Getenv("HOME")
	assert.DirExists(t, home)

	restore()
	for _, key := range []string{"HOME", "ENV", "BASH_ENV"} {
		_, present := os.LookupEnv(key)
		assert.Falsef(t, present, "%s is absent again", key)
	}
	assert.NoDirExists(t, home)
}
