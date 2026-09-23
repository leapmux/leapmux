package copilot

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

func TestJSONRPCUsesConfiguredFramingForEveryWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(*providerkit.JSONRPCProcess) error
		want string
	}{
		{"notification", func(b *providerkit.JSONRPCProcess) error {
			return b.SendNotification("ping", json.RawMessage(`{"count":0}`))
		}, `{"jsonrpc":"2.0","method":"ping","params":{"count":0}}`},
		{"response", func(b *providerkit.JSONRPCProcess) error {
			return b.SendResponse(json.RawMessage(`"001"`), map[string]bool{"ok": true})
		}, `{"jsonrpc":"2.0","id":"001","result":{"ok":true}}`},
		{"error", func(b *providerkit.JSONRPCProcess) error {
			return b.SendErrorResponse(json.RawMessage(`0`), -32601, "Unsupported method")
		}, `{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"Unsupported method"}}`},
		{"raw input", func(b *providerkit.JSONRPCProcess) error {
			return b.SendRawInput([]byte(" {\"id\":9007199254740993}\n"))
		}, " {\"id\":9007199254740993}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			base := providerkit.JSONRPCProcess{Process: providerkit.NewProcessFrom(providerkit.ProcessConfig{Stdin: agenttest.NopStdin(&output)}), FrameMessage: frameCopilotJSON}
			require.NoError(t, test.send(&base))
			scanner := newCopilotScanner(bytes.NewReader(output.Bytes()), "", 1024)
			require.True(t, scanner.Scan())
			if test.name == "raw input" {
				require.Equal(t, test.want, scanner.Text())
			} else {
				require.JSONEq(t, test.want, scanner.Text())
			}
			require.False(t, scanner.Scan())
			require.NoError(t, scanner.Err())
		})
	}
}
