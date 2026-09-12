//go:build unix

package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/stretchr/testify/require"
)

func TestCopilotNativeConnection(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	installFakeACPCLI(t, fakeACPCLISpec{
		binary: "copilot", helperRun: "TestHelperCopilotNativeConnection",
		wantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", argsFile: argsFile,
	})
	notifications := make(chan []byte, 1)
	connection, err := startCopilotConnection(t.Context(), Options{
		AgentID: "copilot-native", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		StartupTimeout: 5 * time.Second, APITimeout: time.Second,
	}, func(line *parsedLine) { notifications <- line.Raw })
	require.NoError(t, err)
	t.Cleanup(func() { connection.Stop(); _ = connection.Wait() })
	args, err := os.ReadFile(argsFile)
	require.NoError(t, err)
	require.Contains(t, string(args), "--server --stdio")
	require.NotContains(t, string(args), "--acp")
	require.NotContains(t, string(args), "--disable-builtin-mcps")
	result, err := connection.sendRequest("probe.echo", json.RawMessage(`{"text":"한글"}`), time.Second)
	require.NoError(t, err)
	require.JSONEq(t, `{"text":"한글"}`, string(result))
	for _, value := range []string{
		`{"kind":"completed","message":"Switched to interactive mode."}`,
		`{"code":429,"message":"This is tool output, not a protocol error."}`,
		`null`, `false`, `0`, `""`,
	} {
		result, err := connection.sendRequest("probe.echo", json.RawMessage(value), time.Second)
		require.NoError(t, err, value)
		require.JSONEq(t, value, string(result))
	}
	select {
	case raw := <-notifications:
		require.Equal(t, " {\"jsonrpc\":\"2.0\",\"method\":\"probe.notification\", \"params\":{\"counter\":9007199254740993}} ", string(raw))
	case <-time.After(time.Second):
		t.Fatal("The connection lost a native notification during startup")
	}
}

func TestCopilotNativeConnectionRejectsAnUnknownProtocol(t *testing.T) {
	installFakeACPCLI(t, fakeACPCLISpec{
		binary: "copilot", helperRun: "TestHelperCopilotNativeConnection",
		wantEnv: "LEAPMUX_TEST_COPILOT_NATIVE", env: []string{"LEAPMUX_TEST_COPILOT_PROTOCOL=99"},
	})
	_, err := startCopilotConnection(t.Context(), Options{
		AgentID: "copilot-native", WorkingDir: t.TempDir(), Shell: testutil.TestShell(),
		StartupTimeout: 5 * time.Second, APITimeout: time.Second,
	}, func(*parsedLine) {})
	require.ErrorContains(t, err, "protocol 99")
}

func TestHelperCopilotNativeConnection(t *testing.T) {
	if os.Getenv("LEAPMUX_TEST_COPILOT_NATIVE") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	headers := textproto.NewReader(reader)
	send := func(raw []byte) {
		_, err := fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n", len(raw))
		require.NoError(t, err)
		_, err = os.Stdout.Write(raw)
		require.NoError(t, err)
	}
	for {
		header, err := headers.ReadMIMEHeader()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		length, err := strconv.Atoi(header.Get("Content-Length"))
		require.NoError(t, err)
		payload := make([]byte, length)
		_, err = io.ReadFull(reader, payload)
		require.NoError(t, err)
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		require.NoError(t, json.Unmarshal(payload, &request))
		result := request.Params
		if request.Method == "status.get" {
			send([]byte(" {\"jsonrpc\":\"2.0\",\"method\":\"probe.notification\", \"params\":{\"counter\":9007199254740993}} "))
			protocol := 3
			if value := os.Getenv("LEAPMUX_TEST_COPILOT_PROTOCOL"); value != "" {
				protocol, err = strconv.Atoi(value)
				require.NoError(t, err)
			}
			result = json.RawMessage(fmt.Sprintf(`{"version":"probe","protocolVersion":%d}`, protocol))
		}
		send([]byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, request.ID, result)))
	}
	os.Exit(0)
}
