//go:build unix

package launch

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/util/procutil"
)

// TestShellEnvOutputHolder is the subprocess fixture that retains inherited
// stdout until the parent releases its connection.
func TestShellEnvOutputHolder(t *testing.T) {
	if os.Getenv("LEAPMUX_SHELL_ENV_HOLDER") != "1" {
		return
	}
	conn, err := net.DialTimeout("tcp", os.Getenv("LEAPMUX_SHELL_ENV_READY"), 30*time.Second)
	if err != nil {
		os.Exit(2)
	}
	_, _ = fmt.Fprintln(conn, os.Getpid())
	_, _ = io.Copy(io.Discard, conn)
	_ = conn.Close()
	os.Exit(0)
}

func TestShellEnvReturnsWhileADescendantRetainsOutput(t *testing.T) {
	t.Parallel()
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancelled=%v", cancelled), func(t *testing.T) {
			t.Parallel()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			finished := make(chan struct{})
			started := false
			var descendant procutil.ProcessIdentity
			var connection net.Conn
			t.Cleanup(func() {
				cancel()
				_ = listener.Close()
				if connection != nil {
					_ = connection.Close()
				}
				if !descendant.IsZero() {
					_, _ = descendant.Kill()
					assert.Eventually(t, func() bool { return !descendant.Runs() }, 30*time.Second, time.Millisecond)
				}
				if started {
					select {
					case <-finished:
					case <-time.After(30 * time.Second):
						t.Error("the environment probe did not finish during cleanup")
					}
				}
			})
			executable, err := os.Executable()
			require.NoError(t, err)
			shell := filepath.Join(t.TempDir(), "sh")
			require.NoError(t, os.WriteFile(shell, []byte("#!/bin/sh\nLEAPMUX_SHELL_ENV_HOLDER=1 \"$LEAPMUX_SHELL_ENV_EXECUTABLE\" -test.run='^TestShellEnvOutputHolder$' &\nexec /bin/sh \"$@\"\n"), 0o700))
			type result struct {
				values map[string]string
				probe  ProbeResult
			}
			outcome := make(chan result, 1)
			started = true
			go func() {
				defer close(finished)
				values, probe := ShellEnv(ctx, ShellEnvOptions{
					Shell: shell, Variables: []string{"LEAPMUX_SHELL_ENV_VALUE"},
					Env: append(os.Environ(), "LEAPMUX_SHELL_ENV_EXECUTABLE="+executable,
						"LEAPMUX_SHELL_ENV_READY="+listener.Addr().String(), "LEAPMUX_SHELL_ENV_VALUE=complete"),
				})
				outcome <- result{values: values.Values, probe: probe}
			}()
			ready := make(chan net.Conn, 1)
			go func() {
				conn, _ := listener.Accept()
				ready <- conn
			}()
			select {
			case connection = <-ready:
				require.NotNil(t, connection)
			case <-time.After(30 * time.Second):
				t.Fatal("the descendant did not connect")
			}
			require.NoError(t, connection.SetReadDeadline(time.Now().Add(30*time.Second)))
			pidLine, err := bufio.NewReader(connection).ReadString('\n')
			require.NoError(t, err)
			pid, err := strconv.Atoi(pidLine[:len(pidLine)-1])
			require.NoError(t, err)
			var identified bool
			descendant, identified = procutil.IdentifyProcess(pid)
			require.True(t, identified)
			if cancelled {
				cancel()
			}
			select {
			case got := <-outcome:
				if cancelled {
					assert.Equal(t, ProbeUnknown, got.probe)
				} else {
					assert.Equal(t, ProbeYes, got.probe)
					assert.Equal(t, "complete", got.values["LEAPMUX_SHELL_ENV_VALUE"])
				}
			case <-time.After(30 * time.Second):
				t.Fatal("the probe waited for the descendant to release stdout")
			}
		})
	}
}
