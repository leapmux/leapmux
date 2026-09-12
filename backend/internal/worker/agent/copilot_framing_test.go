package agent

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCopilotFramingPreservesPreambleAndPayloads(t *testing.T) {
	first := []byte(" {\n\"id\":9007199254740993,\"text\":\"한글\"\n} ")
	second := []byte(`{"method":"session.event","params":{}}`)
	data := append([]byte("shell output\n READY \n"), frameCopilotJSON(first)...)
	data = append(data, frameCopilotJSON(second)...)
	scanner := newCopilotScanner(bytes.NewReader(data), "READY", 1024)
	base := processBase{preambleDelimiter: "READY"}
	base.skipPreamble(scanner)
	require.Equal(t, "shell output", base.PreambleOutput())
	require.True(t, scanner.Scan())
	require.Equal(t, first, scanner.Bytes())
	require.True(t, scanner.Scan())
	require.Equal(t, second, scanner.Bytes())
	require.False(t, scanner.Scan())
	require.NoError(t, scanner.Err())
}

func TestCopilotFramingUsesBytes(t *testing.T) {
	payload := []byte(`{"text":"한글"}`)
	frame := frameCopilotJSON(payload)
	require.Equal(t, fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload), string(frame))
	scanner := newCopilotScanner(bytes.NewReader(frame), "", len(payload))
	require.True(t, scanner.Scan())
	require.Equal(t, payload, scanner.Bytes())
	require.False(t, scanner.Scan())
	require.NoError(t, scanner.Err())
}

func TestCopilotFramingRejectsInvalidFrames(t *testing.T) {
	for _, test := range []struct {
		name    string
		input   string
		message string
	}{
		{"missing length", "Content-Type: application/json\r\n\r\n{}", "Content-Length"},
		{"negative length", "Content-Length: -1\r\n\r\n", "Content-Length"},
		{"signed length", "Content-Length: +2\r\n\r\n{}", "Content-Length"},
		{"empty length", "Content-Length: \r\n\r\n", "Content-Length"},
		{"zero length", "Content-Length: 0\r\n\r\n", "Content-Length"},
		{"overflow", "Content-Length: 99999999999999999999999999\r\n\r\n", "Content-Length"},
		{"duplicate length", "Content-Length: 2\r\nContent-Length: 3\r\n\r\n{}", "Content-Length"},
		{"large payload", "Content-Length: 101\r\n\r\n", "limit"},
		{"truncated payload", "Content-Length: 3\r\n\r\n{}", "incomplete"},
		{"truncated header", "Content-Length: 3\r\n", "incomplete"},
		{"large header", strings.Repeat("X", 9000), "limit"},
	} {
		t.Run(test.name, func(t *testing.T) {
			scanner := newCopilotScanner(strings.NewReader(test.input), "", 100)
			require.False(t, scanner.Scan())
			require.ErrorContains(t, scanner.Err(), test.message)
		})
	}
}

func TestCopilotFramingAcceptsHeaderCaseAndContentType(t *testing.T) {
	scanner := newCopilotScanner(strings.NewReader("content-length: 2\r\nContent-Type: application/vscode-jsonrpc; charset=utf-8\r\n\r\n{}"), "", 100)
	require.True(t, scanner.Scan())
	require.Equal(t, "{}", scanner.Text())
	require.False(t, scanner.Scan())
	require.NoError(t, scanner.Err())
}
