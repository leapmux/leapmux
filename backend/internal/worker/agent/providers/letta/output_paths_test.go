package letta

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/leapmux/leapmux/internal/worker/agent/providers/internal/providerkit"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLettaNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	workingDir := t.TempDir()
	project := strings.TrimPrefix(filepath.Clean(workingDir), string(filepath.Separator))
	project = strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(project)
	path := filepath.Join(home, ".letta", "projects", project, "agent-tools", "bash-11111111-1111-4111-8111-111111111111.txt")
	complete := "native first42\n" + strings.Repeat("native middle77\n", 1000) + "native tail42"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	a, sink := newLettaChildTestAgent(t)
	a.Process = providerkit.NewProcessFrom(providerkit.ProcessConfig{
		AgentID: "letta-full-output", Ctx: t.Context(), Cmd: &exec.Cmd{Env: []string{"HOME=" + home, "USERPROFILE=" + home}},
	})
	a.workingDir = workingDir
	start, err := json.Marshal(map[string]any{
		"type": "stream_delta", "runtime": map[string]string{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"},
		"delta": map[string]any{"id": "start-id", "run_id": "run-1", "message_type": "client_tool_start", "tool_call_id": "native-full-output", "tool_name": "Bash", "tool_args": map[string]string{"command": "native command"}},
	})
	require.NoError(t, err)
	a.HandleOutput(start)
	excerpt := "native first42\n[Output truncated: showing 2,000 of 18,000 characters.]\n[Full output written to: " + path + "]"
	delta := map[string]any{"type": "message", "id": "result-id", "run_id": "run-1", "message_type": "tool_return_message", "tool_call_id": "native-full-output", "status": "success", "tool_return": excerpt}
	raw, err := json.Marshal(map[string]any{
		"type": "stream_delta", "runtime": map[string]string{"agent_id": "agent-local-1", "conversation_id": "local-conv-1"}, "delta": delta,
	})
	require.NoError(t, err)
	a.HandleOutput(raw)
	a.HandleOutput([]byte(lettaLiveTurnFinished))
	var resultFound bool
	for _, message := range sink.Messages() {
		if message.SpanID != "letta-tool-native-full-output" || !message.Closing {
			continue
		}
		resultFound = true
		expected, err := json.Marshal(delta)
		require.NoError(t, err)
		assert.JSONEq(t, string(expected), string(message.Content))
		assert.NotContains(t, string(message.Content), "native middle77")
		assert.NotContains(t, string(message.SupplementalContent), "native middle77", "external output text must not enter Worker storage")
		assert.Contains(t, string(message.Content), path)
	}
	require.True(t, resultFound, "the actual final tool return must reach its native span")
}
