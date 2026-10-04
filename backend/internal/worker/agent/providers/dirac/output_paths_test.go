package dirac

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/leapmux/leapmux/internal/worker/agent"
	"github.com/leapmux/leapmux/internal/worker/agent/agenttest"
	"github.com/leapmux/leapmux/internal/worker/agent/internal/launch"
	"github.com/leapmux/leapmux/internal/worker/terminal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiracNativeOutputPathPreservesPreviewWithoutSavedText(t *testing.T) {
	home := t.TempDir()
	workingDir := t.TempDir()
	temporaryDir := t.TempDir()
	path := filepath.Join(temporaryDir, "dirac", "large-output-1790904331563-6032d56.log")
	complete := "native first42\n\n" + strings.Repeat("native middle77 文\n", 1000) + "native tail42\n"
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(complete), 0o600))
	executable, err := os.Executable()
	require.NoError(t, err)
	previous := diracLocator
	diracLocator = launch.Custom(func(context.Context, string, bool) (launch.Spec, launch.Resolution) {
		return launch.Spec{Program: executable, Env: []string{
			diracStartupHelperEnv + "=1",
			diracStartupLogEnv + "=" + filepath.Join(home, "requests.jsonl"),
		}}, launch.Found
	})
	t.Cleanup(func() { diracLocator = previous })
	sink := &agenttest.Sink{}
	started, err := Start(t.Context(), agent.Options{
		AgentID: "dirac-full-output-test", WorkingDir: workingDir, HomeDir: home,
		Shell: terminal.ResolveDefaultShell(), StartupTimeout: 30 * time.Second,
		ExtraEnv: []string{"TMPDIR=" + temporaryDir, "TEMP=" + temporaryDir, "TMP=" + temporaryDir},
	}, agent.NewProviderServices(sink))
	require.NoError(t, err)
	defer func() {
		started.Stop()
		assert.NoError(t, started.Wait(), "the controlled native peer must close without another cleanup failure")
	}()

	a, ok := started.(*Agent)
	require.True(t, ok, "production Start must return the Dirac agent")
	sessionID := a.CurrentSessionID()
	require.Equal(t, "dirac-test-session", sessionID, "the private ACP peer must own the supplied native frames")
	callID := "1790904331507-2"
	opening := map[string]any{
		"sessionUpdate": "tool_call", "toolCallId": callID, "status": "pending",
		"kind": "execute", "name": "execute_command", "title": "Executing 1 of 1: Node script",
		"rawInput": map[string]any{"tool": "execute_command", "command": "node native-script.js", "language": "node", "displayName": "Node script"},
	}
	excerpt := "Command executed successfully (exit code 0).\nOutput:\nnative first42\n\n... [Output truncated to 10.0 KB to avoid context flooding (7.2 KB truncated). Use more specific commands if you need to see more output.] ...\n\nnative tail42\nFull output saved to: " + path
	closing := map[string]any{
		"sessionUpdate": "tool_call_update", "toolCallId": callID, "status": "completed",
		"kind": "execute", "name": "execute_command", "title": "Executed: Node script",
		"rawOutput": map[string]any{"output": excerpt, "userRejected": false, "exitCode": 0, "signal": nil},
	}
	for _, update := range []map[string]any{opening, closing} {
		frame, encodeErr := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "method": "session/update",
			"params": map[string]any{"sessionId": sessionID, "update": update},
		})
		require.NoError(t, encodeErr)
		started.HandleOutput(frame)
	}
	var found bool
	for _, message := range sink.Messages() {
		if message.SpanID != callID || !message.Closing {
			continue
		}
		found = true
		original, encodeErr := json.Marshal(closing)
		require.NoError(t, encodeErr)
		assert.JSONEq(t, string(original), string(message.Content))
		assert.NotContains(t, string(message.Content), "native middle77")
		assert.NotContains(t, string(message.SupplementalContent), "native middle77", "external output text must not enter Worker storage")
		assert.Contains(t, string(message.Content), path)
	}
	require.True(t, found, "the native generated call must retain its own closing result")
}
