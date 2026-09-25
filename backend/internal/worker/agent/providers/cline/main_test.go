package cline

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isolatedHome is the home directory of every test of this package.
var isolatedHome string

// clineHelperEnv marks the process of the fake `cline` of the start tests,
// which runs this test binary (start_unix_test.go).
const clineHelperEnv = "GO_WANT_HELPER_PROCESS_CLINE"

// TestMain runs the tests of this package under a home directory of their own,
// with no variable that moves Cline's data. The provider reads
// `~/.cline/data/settings/providers.json` when nothing moves it, and the
// user's own directory holds a real Cline account. A test that forgets its
// directories then reads an empty home, never the user's.
//
// The fake `cline` of the start tests runs this binary too, and it keeps the
// environment of the test that started it.
func TestMain(m *testing.M) {
	if os.Getenv(clineHelperEnv) == "1" {
		os.Exit(m.Run())
	}
	home, err := os.MkdirTemp("", "cline-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create the test home:", err)
		os.Exit(2)
	}
	isolatedHome = home
	for _, key := range []string{"HOME", "USERPROFILE"} {
		if err := os.Setenv(key, home); err != nil {
			fmt.Fprintln(os.Stderr, "set the test home:", err)
			os.Exit(2)
		}
	}
	for _, key := range append([]string{"ENV", "BASH_ENV"}, dataEnvKeys...) {
		if err := os.Unsetenv(key); err != nil {
			fmt.Fprintln(os.Stderr, "clear a variable of the test environment:", err)
			os.Exit(2)
		}
	}
	code := m.Run()
	_ = os.RemoveAll(home)
	os.Exit(code)
}

func TestClineTestsRunUnderAnIsolatedHome(t *testing.T) {
	t.Parallel()
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	assert.Equal(t, isolatedHome, home)
	for _, key := range dataEnvKeys {
		assert.Empty(t, os.Getenv(key), key)
	}
	selection, err := readProviderSelection(providerSettingsPath(os.Getenv, home))
	require.NoError(t, err)
	assert.Equal(t, providerSelection{Provider: defaultClineProvider}, selection, "the isolated home holds no Cline settings")
}
