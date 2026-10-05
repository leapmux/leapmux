package junie

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
)

const junieOutputPeerEnv = "LEAPMUX_JUNIE_OUTPUT_PEER"

// The two cumulative snapshots that Junie 26.9.22 streams for one running
// shell command. They are the rawOutput.output values that a probe read from
// the real CLI: the command printed one segment, held, and then printed a
// second one.
const (
	junieOutputFirst  = "NATIVEFIRST\nxxxx\n"
	junieOutputSecond = junieOutputFirst + "NATIVESECOND\nyyyy\n"
)

// TestMain runs the fake ACP peer before the Go test runner parses its flags.
// Start launches this test binary with the real `--acp=true` flags, so the
// production wiring of the Junie provider reads the peer's frames.
func TestMain(m *testing.M) {
	if os.Getenv(junieOutputPeerEnv) == "1" {
		runJunieOutputPeer()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// runJunieOutputPeer answers the handshake and then streams a running command
// the way Junie does: a tool_call with a terminal block, and cumulative
// rawOutput.output snapshots in in_progress updates that carry no content.
func runJunieOutputPeer() {
	scanner := bufio.NewScanner(os.Stdin)
	writer := bufio.NewWriter(os.Stdout)
	write := func(line string) bool {
		if _, err := fmt.Fprintln(writer, line); err != nil {
			return false
		}
		return writer.Flush() == nil
	}
	update := func(body string) string {
		return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"session-260101-000000-peer","update":` + body + `}}`
	}
	for scanner.Scan() {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil || len(request.ID) == 0 {
			continue
		}
		result := `{}`
		switch request.Method {
		case "initialize":
			result = `{"protocolVersion":1,"agentInfo":{"name":"junie-test","version":"1"},"agentCapabilities":{}}`
		case "session/new":
			result = `{"sessionId":"session-260101-000000-peer"}`
		}
		if !write(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request.ID, result)) {
			os.Exit(2)
		}
		if request.Method != "session/new" {
			continue
		}
		running := func(output string) string {
			body, _ := json.Marshal(map[string]any{
				"sessionUpdate": "tool_call_update", "toolCallId": "tool-1", "kind": "execute", "status": "in_progress",
				"content": []any{}, "rawOutput": map[string]any{"output": output},
			})
			return update(string(body))
		}
		for _, line := range []string{
			update(`{"sessionUpdate":"tool_call","toolCallId":"tool-1","title":"node","kind":"execute","status":"in_progress","content":[{"type":"terminal","terminalId":"tool-1"}],"rawInput":{"command":"node"}}`),
			running(junieOutputFirst),
			running(junieOutputSecond),
		} {
			if !write(line) {
				os.Exit(2)
			}
		}
	}
	if scanner.Err() != nil {
		os.Exit(2)
	}
}

// Junie streams the output of a running shell command in the cumulative
// rawOutput.output of its in_progress updates and leaves their content empty.
// The shared content path reads only content, so the Worker must read
// rawOutput through the provider's own hook. Without it the byte counter of a
// running command stays at zero until the command ends, and the row draws no
// live output.
func TestStartCountsTheLiveOutputOfARunningShellCommand(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "work")
	require.NoError(t, os.MkdirAll(work, 0o700))
	executable, err := os.Executable()
	require.NoError(t, err)

	previous := junieLocator
	junieLocator = launch.Custom(func(_ context.Context, _ string, _ bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, Env: []string{junieOutputPeerEnv + "=1"}}, launch.Found
	})
	t.Cleanup(func() { junieLocator = previous })

	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID: "junie-output-test", WorkingDir: work, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 10 * time.Second,
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	t.Cleanup(started.Stop)

	var totals []int64
	var tails []string
	require.Eventually(t, func() bool {
		totals, tails = nil, nil
		for _, update := range sink.ProgressUpdates() {
			if update.ScopeID != "tool-1" {
				continue
			}
			if update.Operation == agent.ProgressOutputTotal {
				totals = append(totals, update.Value)
			}
			if update.Operation == agent.ProgressOutputTail {
				tails = append(tails, update.Text)
			}
		}
		return len(totals) >= 2 && len(tails) >= 2
	}, 10*time.Second, 20*time.Millisecond, "the live output of the running command reaches the progress counter")

	assert.Equal(t, []int64{int64(len(junieOutputFirst)), int64(len(junieOutputSecond))}, totals,
		"each snapshot is the exact output so far")
	assert.Equal(t, []string{junieOutputFirst, junieOutputSecond}, tails,
		"the row draws the output that the command printed so far")
	assert.Equal(t, int64(len(junieOutputSecond)), sink.ProgressSnapshot().OutputBytes)
}
