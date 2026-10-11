package copilot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/generated/contracts"
	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCopilotNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	workingDir := t.TempDir()
	path := filepath.Join(directory, "1790904357508-copilot-tool-output-42-11111111-1111-4111-8111-111111111111.txt")
	complete := "native first42\n" + strings.Repeat("native middle77\n", 1000) + "native tail42"
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	a, sink := newNativeCopilotForEvents(t)
	// The fake native process supplies the configured creator PID. This test starts no process.
	command := &exec.Cmd{Process: &os.Process{Pid: 42}, Env: []string{"TMPDIR=" + directory, "TEMP=" + directory, "TMP=" + directory}}
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{AgentID: "copilot-full-output", Ctx: t.Context(), Cmd: command})
	a.opts = agent.Options{WorkingDir: workingDir}
	a.HandleOutput(nativeCopilotEvent(t, "", contracts.CopilotEventToolStarted, map[string]any{
		"toolCallId": "native-full-output", "toolName": contracts.CopilotToolBash, "arguments": map[string]string{"command": "native command"},
	}))
	excerpt := "native first42\n... output omitted ...\nnative tail42"
	raw := nativeCopilotEvent(t, "", contracts.CopilotEventToolCompleted, map[string]any{
		"toolCallId": "native-full-output", "success": true,
		"result": map[string]any{
			"content": excerpt, "detailedContent": excerpt,
			"contents": []any{map[string]any{"type": "shell_exit", "shellId": "0", "exitCode": 0, "cwd": workingDir, "outputTruncated": true, "outputFilePath": path}},
		},
	})
	a.HandleOutput(raw)
	messages := sink.Messages()
	require.Len(t, messages, 2)
	result := messages[1]
	assert.Equal(t, string(raw), string(result.Content))
	assert.Equal(t, "native-full-output", result.SpanID)
	assert.True(t, result.Closing)
	assert.NotContains(t, string(result.Content), "native middle77")
	assert.NotContains(t, string(result.SupplementalContent), "native middle77", "external output text must not enter Worker storage")
	assert.Contains(t, string(result.Content), path)
}
