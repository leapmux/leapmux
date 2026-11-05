//go:build !windows

package procutil

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shutdownSignalsHelperEnv makes the helper process print its shutdown signal
// list and exit.
const shutdownSignalsHelperEnv = "LEAPMUX_TEST_SHUTDOWN_SIGNALS_HELPER"

// TestHelperShutdownSignals is the process the ignored-hang-up test starts. It
// is inert in the test process itself.
func TestHelperShutdownSignals(t *testing.T) {
	if os.Getenv(shutdownSignalsHelperEnv) != "1" {
		return
	}
	fmt.Println(signalNames(ShutdownSignals()))
	os.Exit(0)
}

func signalNames(signals []os.Signal) string {
	names := make([]string, len(signals))
	for i, sig := range signals {
		names[i] = sig.String()
	}
	return strings.Join(names, ",")
}

// The test process holds the default SIGHUP disposition, so a hang-up must be
// in the list and must start the orderly shutdown.
func TestShutdownSignalsIncludeHangUp(t *testing.T) {
	signals := ShutdownSignals()

	assert.Contains(t, signals, syscall.SIGINT)
	assert.Contains(t, signals, syscall.SIGTERM)
	assert.Contains(t, signals, syscall.SIGHUP)
	assert.Len(t, signals, 3)
}

// A trap with an empty handler sets SIG_IGN, and exec preserves it. signal.Notify
// would override that deliberate ignore, so ShutdownSignals must omit SIGHUP:
// a nohup'd command must survive the hang-up of its terminal.
func TestShutdownSignalsSkipAnIgnoredHangUp(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c",
		`trap "" HUP; exec "$0" "$@"`,
		os.Args[0], "-test.run=^TestHelperShutdownSignals$")
	cmd.Env = append(os.Environ(), shutdownSignalsHelperEnv+"=1")
	output, err := cmd.Output()
	require.NoError(t, err, "%s", string(output))

	signals := strings.Split(strings.TrimSpace(string(output)), ",")
	assert.NotContains(t, signals, syscall.SIGHUP.String())
	assert.Contains(t, signals, syscall.SIGINT.String())
	assert.Contains(t, signals, syscall.SIGTERM.String())
}
