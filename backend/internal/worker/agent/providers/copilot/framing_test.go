package copilot

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/util/shellquote"
	"github.com/leapmux/leapmux/internal/util/testutil"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/require"
)

func TestCopilotFramingPreservesPreambleAndPayloads(t *testing.T) {
	first := []byte(" {\n\"id\":9007199254740993,\"text\":\"한글\"\n} ")
	second := []byte(`{"method":"session.event","params":{}}`)
	data := append([]byte("shell output\nREADY\r\n"), frameCopilotJSON(first)...)
	data = append(data, frameCopilotJSON(second)...)
	scanner := newCopilotScanner(bytes.NewReader(data), "READY", 1024)
	base := providerkit.NewProcessFrom(providerkit.ProcessConfig{PreambleDelimiter: "READY"})
	base.SkipPreambleForTest(scanner)
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

func TestProfileCommandCopilotFramingAfterAnUnterminatedBanner(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a POSIX shell profile")
	}
	dir := t.TempDir()
	shell := filepath.Join(dir, "profile-shell")
	testutil.WriteExecutable(t, shell, "#!/bin/sh\nprintf 'profile message'\nexec /bin/sh \"$@\"\n")
	payload := []byte("{\n\"method\":\"session.event\",\"params\":{\"text\":\"한글\"}\n}")
	nativeFrame := fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(payload), payload)
	testutil.WriteExecutable(t, filepath.Join(dir, copilotBinaryName),
		"#!/bin/sh\nprintf '%s' "+shellquote.POSIX.Quote(nativeFrame)+"\n"+
			"IFS= read -r ack && [ \"$ack\" = received ]\n")
	t.Setenv("PATH", dir)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	connection, err := startCopilotConnection(ctx, agent.Options{Shell: shell, WorkingDir: dir})
	require.NoError(t, err)
	defer connection.Stop()
	frames := make(chan []byte, 2)
	connection.startReading(func(line *providerkit.ParsedLine) { frames <- line.Raw })

	deadline := testutil.DeadlineContext(t)
	select {
	case frame := <-frames:
		require.Equal(t, payload, frame)
	case <-connection.ProcessDone():
		t.Fatalf("Copilot exited before receiving the acknowledgment: %v", connection.Wait())
	case <-deadline.Done():
		t.Fatal("the first native frame did not arrive while Copilot was waiting for stdin")
	}
	require.True(t, connection.Ready())
	require.Equal(t, "profile message", connection.PreambleOutput())
	require.NoError(t, connection.WriteStdin([]byte("received\n")))

	select {
	case <-connection.ProcessDone():
		require.NoError(t, connection.Wait())
		require.Empty(t, frames)
	case <-deadline.Done():
		t.Fatal("Copilot did not finish after receiving the acknowledgment")
	}
}
