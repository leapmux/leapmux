package agent

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJSONRPCUsesConfiguredFramingForEveryWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		send func(*jsonrpcBase) error
		want string
	}{
		{"notification", func(b *jsonrpcBase) error { return b.sendNotification("ping", json.RawMessage(`{"count":0}`)) }, `{"jsonrpc":"2.0","method":"ping","params":{"count":0}}`},
		{"response", func(b *jsonrpcBase) error {
			return b.sendResponse(json.RawMessage(`"001"`), map[string]bool{"ok": true})
		}, `{"jsonrpc":"2.0","id":"001","result":{"ok":true}}`},
		{"error", func(b *jsonrpcBase) error {
			return b.sendErrorResponse(json.RawMessage(`0`), -32601, "Unsupported method")
		}, `{"jsonrpc":"2.0","id":0,"error":{"code":-32601,"message":"Unsupported method"}}`},
		{"raw input", func(b *jsonrpcBase) error { return b.SendRawInput([]byte(" {\"id\":9007199254740993}\n")) }, " {\"id\":9007199254740993}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			base := jsonrpcBase{processBase: processBase{stdin: nopWriteCloser{&output}}, frameMessage: frameCopilotJSON}
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
