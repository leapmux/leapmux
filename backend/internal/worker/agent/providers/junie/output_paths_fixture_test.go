package junie

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type junieOutputPathFixture struct {
	home, cwd, sessionID, callID, taskID, path, output string
	original                                           []byte
	event                                              map[string]any
}

func newJunieOutputPathFixture(t *testing.T) junieOutputPathFixture {
	t.Helper()
	fixture := junieOutputPathFixture{home: t.TempDir(), sessionID: "session-261003-225241-1cba", callID: "310805cf-f0d5-4ffd-928b-db543ac74998", taskID: "task-261003-225242-1bq0"}
	fixture.cwd = filepath.Join(fixture.home, "project")
	fixture.path = filepath.Join(fixture.home, "sessions", fixture.sessionID, fixture.taskID, "terminal-output", "terminal-output-123.txt")
	fixture.output = "head" + strings.Repeat("x", 70000) + "tail"
	preview := fixture.output[:32768] + "\n\n... [output is too long, the middle part is omitted; the beginning and the end of the output are shown] ...\n\n" + fixture.output[len(fixture.output)-32768:]
	require.NoError(t, os.MkdirAll(filepath.Dir(fixture.path), 0o700))
	require.NoError(t, os.WriteFile(fixture.path, []byte(fixture.output), 0o600))
	summary, err := json.Marshal(map[string]any{"sessionId": fixture.sessionID, "projectDir": fixture.cwd})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(fixture.home, "sessions", fixture.sessionID, "summary.json"), summary, 0o600))
	fixture.event = map[string]any{"kind": "SessionA2uxEvent", "taskId": fixture.taskID, "event": map[string]any{"agentEvent": map[string]any{
		"kind": "TerminalBlockUpdatedEvent", "stepId": fixture.callID, "status": "COMPLETED", "command": "node exact-script.js", "output": preview, "exitCode": 0,
		"agent": map[string]any{"kind": "MainAgent", "id": "main"}, "outputFile": map[string]any{"relativePath": fixture.path},
	}}}
	writeJunieOutputPathEvents(t, fixture, fixture.event)
	fixture.original, err = json.Marshal(map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": fixture.callID, "kind": "execute", "status": "completed", "content": []any{},
		"rawInput": map[string]any{"command": "node exact-script.js", "cwd": fixture.cwd}, "rawOutput": map[string]any{"output": preview},
		"_meta": map[string]any{"terminal_exit": map[string]any{"terminal_id": fixture.callID, "exit_code": 0, "signal": nil}},
	})
	require.NoError(t, err)
	return fixture
}

func writeJunieOutputPathEvents(t *testing.T, fixture junieOutputPathFixture, events ...map[string]any) {
	t.Helper()
	var data []byte
	for _, event := range events {
		raw, err := json.Marshal(event)
		require.NoError(t, err)
		data = append(data, raw...)
		data = append(data, '\n')
	}
	require.NoError(t, os.WriteFile(filepath.Join(fixture.home, "sessions", fixture.sessionID, "events.jsonl"), data, 0o600))
}
