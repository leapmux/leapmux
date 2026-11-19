//go:build unix

package copilot

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/quartz"
	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const sessionFailurePeerAddress = "LEAPMUX_TEST_COPILOT_SESSION_FAILURE_PEER"

func TestCopilotSessionFailurePeer(t *testing.T) {
	address := os.Getenv(sessionFailurePeerAddress)
	if address == "" {
		return
	}
	scanner := newCopilotScanner(os.Stdin, "", 4096)
	require.True(t, scanner.Scan())
	var request struct {
		ID     json.RawMessage
		Method string
	}
	require.NoError(t, json.Unmarshal(scanner.Bytes(), &request))
	require.Equal(t, "status.get", request.Method)
	require.NoError(t, os.Stdin.Close())
	peer, err := net.Dial("tcp4", address)
	require.NoError(t, err)
	reply := fmt.Sprintf("{\"jsonrpc\":\"2.0\",\"id\":%s,\"result\":{\"protocolVersion\":3}}", request.ID)
	_, err = os.Stdout.Write(frameCopilotJSON([]byte(reply)))
	require.NoError(t, err)

	var release [1]byte
	_, err = io.ReadFull(peer, release[:])
	require.NoError(t, err)
	_, err = os.Stderr.WriteString("native session diagnostic during shutdown")
	require.NoError(t, err)
	require.NoError(t, peer.Close())
	os.Exit(0)
}

func TestCopilotSessionFailureIncludesTheFinalNativeDiagnostic(t *testing.T) {
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	t.Cleanup(func() { _ = listener.Close() })
	require.NoError(t, listener.SetDeadline(time.Now().Add(30*time.Second)))
	executable, err := os.Executable()
	require.NoError(t, err)
	dir := t.TempDir()
	testutil.WriteExecutable(t, filepath.Join(dir, "copilot"), "#!/bin/sh\nexec "+
		shellquote.POSIX.Quote(executable)+" -test.run=^TestCopilotSessionFailurePeer$\n")
	t.Setenv("PATH", dir)
	t.Setenv(sessionFailurePeerAddress, listener.Addr().String())
	clock := testutil.NewQuartzMock(t)
	stderr := clock.Trap().NewTimer("stderr-drain")
	stop := clock.Trap().NewTimer(providerkit.ProcessStopGraceTimerTag)
	t.Cleanup(stderr.Close)
	t.Cleanup(stop.Close)
	deadline := testutil.DeadlineContext(t)
	type timerEvent struct {
		kind  string
		delay time.Duration
	}
	events := make(chan timerEvent, 2)
	for name, trap := range map[string]*quartz.Trap{"stderr": stderr, "stop": stop} {
		go func() {
			call, err := trap.Wait(deadline)
			if err == nil && call.Release(deadline) == nil {
				events <- timerEvent{kind: name, delay: call.Duration}
			}
		}()
	}
	opts := agent.Options{AgentID: "copilot-session-failure", WorkingDir: dir, HomeDir: dir, Shell: "/bin/sh",
		APITimeout: 30 * time.Second, StartupTimeout: 30 * time.Second}
	outcome := make(chan error, 1)
	go func() {
		started, err := startNativeCopilotWithClock(deadline, opts, agent.NewProviderServices(&agenttest.Sink{}), clock)
		if started != nil {
			started.Stop()
			_ = started.Wait()
		}
		outcome <- err
	}()
	peer, err := listener.AcceptTCP()
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	select {
	case event := <-events:
		if event.kind == "stderr" {
			clock.Advance(event.delay).MustWait(deadline)
			select {
			case next := <-events:
				require.Equal(t, "stop", next.kind)
			case <-deadline.Done():
				t.Fatal("Copilot did not start shutdown after the diagnostic snapshot")
			}
		}
	case <-deadline.Done():
		t.Fatal("Copilot did not reach session failure cleanup")
	}
	_, err = peer.Write([]byte{1})
	require.NoError(t, err)
	select {
	case err := <-outcome:
		require.ErrorContains(t, err, "native session initialization")
		assert.ErrorContains(t, err, "native session diagnostic during shutdown")
	case <-deadline.Done():
		t.Fatal("Copilot did not return its session initialization error")
	}
}
