package machinelabel_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/machinelabel"
)

// mustHost reads the hostname the label composes with, failing the test when
// the platform withholds it: every case here needs the host half to assert
// the whole label.
func mustHost(t *testing.T) string {
	t.Helper()
	host, err := os.Hostname()
	require.NoError(t, err)
	require.NotEmpty(t, host)
	return host
}

func TestInstallationName_JoinsTheAccountAndTheHost(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("USERNAME", "")
	assert.Equal(t, "alice@"+mustHost(t), machinelabel.InstallationName())
}

func TestInstallationName_PrefersUSEROverUSERNAME(t *testing.T) {
	t.Setenv("USER", "alice")
	t.Setenv("USERNAME", "bob")
	assert.Equal(t, "alice@"+mustHost(t), machinelabel.InstallationName())
}

func TestInstallationName_FallsBackToUSERNAME(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "winuser")
	assert.Equal(t, "winuser@"+mustHost(t), machinelabel.InstallationName())
}

// TestInstallationName_FallsBackToTheHostname exercises the case where
// neither USER nor USERNAME is set (containers, minimal CI runners). The
// result must still identify the machine -- never be empty.
func TestInstallationName_FallsBackToTheHostname(t *testing.T) {
	t.Setenv("USER", "")
	t.Setenv("USERNAME", "")
	assert.Equal(t, mustHost(t), machinelabel.InstallationName())
}
